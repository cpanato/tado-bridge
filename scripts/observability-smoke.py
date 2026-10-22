#!/usr/bin/env python3
"""Exercise the optional Compose stack using only generated climate data."""

import argparse
import base64
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


ROOT = Path(__file__).resolve().parents[1]
LOCAL_HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return str(listener.getsockname()[1])


def room_value(value):
    return json.dumps(value, ensure_ascii=False)[1:-1]


def get_json(url, headers=None, body=None, allow_error=False):
    data = None if body is None else json.dumps(body).encode()
    request = urllib.request.Request(url, data=data, headers=headers or {})
    try:
        with LOCAL_HTTP.open(request, timeout=10) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        if not allow_error:
            raise
        return json.load(error)


def wait_for(check, description):
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        try:
            if check():
                return
        except (OSError, ValueError, KeyError):
            pass
        time.sleep(1)
    raise RuntimeError("Timed out waiting for " + description)


def compose_command(directory, project):
    return ["docker", "compose", "--project-name", project, "--env-file", "/dev/null",
            "-f", str(ROOT / "compose.yaml"), "-f", str(ROOT / "compose.history.yaml"),
            "-f", str(ROOT / "compose.observability.yaml"), "-f", str(directory / "preview.json")]


def stop(state_file):
    directory = state_file.absolute().parent
    state = json.loads(state_file.read_text())
    if (state.get("purpose") != "tado-bridge-synthetic-preview"
            or state.get("project") != directory.name
            or not directory.name.startswith("tado-observability-test-")):
        raise ValueError("Not a retained synthetic preview")
    subprocess.run(compose_command(directory, state["project"]) + ["down", "--volumes", "--timeout", "15"],
                   cwd=ROOT, env=dict(os.environ, **state["environment"]), check=True)
    shutil.rmtree(directory)
    print("Synthetic preview stopped and its temporary state removed.")


def run(screenshot=None, keep=False):
    if os.getuid() == 0:
        raise RuntimeError("Run as a nonroot host user")
    directory = Path(tempfile.mkdtemp(prefix="tado-observability-test-"))
    os.chmod(directory, 0o700)
    credentials = directory / "credentials"
    credentials.mkdir(mode=0o700)
    project = directory.name.lower()
    values = {
        "TADO_BRIDGE_UID": str(os.getuid()), "TADO_BRIDGE_GID": str(os.getgid()),
        "TADO_CREDENTIALS_DIR": str(credentials), "TADO_BRIDGE_IMAGE": "ko.local:fixture",
        "OBSERVABILITY_DIR": str(directory / "state"),
        "TADO_BRIDGE_PORT": free_port(), "PROMETHEUS_PORT": free_port(), "GRAFANA_PORT": free_port(),
        "API_TOKEN": secrets.token_hex(24), "HISTORY_POSTGRES_PASSWORD": secrets.token_hex(24),
        "GRAFANA_ADMIN_PASSWORD": secrets.token_hex(24), "GRAFANA_DB_PASSWORD": secrets.token_hex(24),
        "HISTORY_DATABASE_URL": "", "HISTORY_RETENTION": "2160h", "METRICS_ENABLED": "true",
        "TRMNL_ENABLED": "false", "TRMNL_WEBHOOK_URL": "", "TRMNL_ROOMS": "",
        "POLL_INTERVAL": "30m", "MAX_SNAPSHOT_AGE": "2h", "TADO_DAILY_REQUEST_BUDGET": "90",
    }
    env = dict(os.environ, **values)
    override = directory / "preview.json"
    override.write_text(json.dumps({"services": {"tado-bridge": {
        "command": ["preview"],
        "healthcheck": {"test": ["CMD", "/ko-app/tadofake", "preview-healthcheck"], "interval": "1s"},
    }}}))
    compose = compose_command(directory, project)
    retained = False

    def command(args, **kwargs):
        return subprocess.run(compose + args, cwd=ROOT, env=env, check=True, **kwargs)

    try:
        subprocess.run([sys.executable, str(ROOT / "scripts/prepare-observability.py"), values["OBSERVABILITY_DIR"]], env=env, check=True)
        command(["config", "--quiet"])
        command(["up", "--detach", "--wait", "--wait-timeout", "180"])
        containers = command(["ps", "--all", "--quiet"], capture_output=True, text=True).stdout.split()
        for container in containers:
            bindings = subprocess.run(["docker", "inspect", "--format", "{{json .HostConfig.PortBindings}}", container], env=env, check=True, capture_output=True, text=True)
            for addresses in (json.loads(bindings.stdout) or {}).values():
                assert all(address["HostIp"] == "127.0.0.1" for address in addresses or []), "Test service is publicly bound"
        bridge = "http://127.0.0.1:" + values["TADO_BRIDGE_PORT"]
        prometheus = "http://127.0.0.1:" + values["PROMETHEUS_PORT"]
        grafana = "http://127.0.0.1:" + values["GRAFANA_PORT"]
        bearer = {"Authorization": "Bearer " + values["API_TOKEN"]}
        wait_for(lambda: get_json(bridge + "/readyz")["status"] == "ready", "preview cache")
        snapshot = get_json(bridge + "/api/v1/snapshot", bearer)
        assert snapshot["provider"] == "synthetic-preview" and len(snapshot["homes"][0]["zones"]) == 8
        try:
            get_json(bridge + "/metrics")
            raise AssertionError("Unauthenticated metrics were accepted")
        except urllib.error.HTTPError as error:
            assert error.code == 401
        auth = {"Authorization": "Basic " + base64.b64encode(("admin:" + values["GRAFANA_ADMIN_PASSWORD"]).encode()).decode(), "Content-Type": "application/json"}
        wait_for(lambda: get_json(prometheus + "/api/v1/query?" + urllib.parse.urlencode({"query": 'count_over_time(up{job="tado-bridge"}[1m]) >= 2'}))["data"]["result"], "two authenticated Prometheus scrapes")
        dashboard = get_json(grafana + "/api/dashboards/uid/tado-bridge", auth)["dashboard"]
        choices = get_json(prometheus + "/api/v1/query?" + urllib.parse.urlencode({"query": 'tado_bridge_current_room_info{job="tado-bridge",instance="tado-bridge:8080"}'}))["data"]["result"]
        rooms = {item["metric"]["room"] for item in choices}
        assert rooms == {"All rooms (mean)"} | {"Synthetic home / Room " + str(i) for i in range(1, 9)}, "Room discovery did not find all eight rooms"
        query_count = 0
        for panel in dashboard["panels"]:
            for target in panel.get("targets", []):
                query = dict(target)
                if "expr" in target:
                    query["expr"] = target["expr"].replace('${job:regex}', "tado-bridge").replace('${instance:regex}', "tado-bridge:8080").replace('$room', room_value("All rooms (mean)")).replace("$__range", "24h")
                if "rawSql" in target:
                    query["rawSql"] = target["rawSql"].replace("$__interval", "15m")
                query.update({"intervalMs": 900000, "maxDataPoints": 1000})
                result = get_json(grafana + "/api/ds/query", auth, {"from": str(int((time.time()-86400)*1000)), "to": str(int(time.time()*1000)), "queries": [query]})
                frames = result["results"]["A"]
                assert not frames.get("error"), "Grafana datasource query failed"
                assert any(frame.get("data", {}).get("values") and any(frame["data"]["values"]) for frame in frames.get("frames", [])), "Grafana panel returned no data"
                query_count += 1
        assert query_count == 11
        # Selected gauges must equal that room, including absent target/humidity.
        for zone in snapshot["homes"][0]["zones"]:
            room = "Synthetic home / " + zone["name"]
            for metric, field in [("temperature_celsius", "temperature_c"), ("target_temperature_celsius", "target_temperature_c"), ("humidity_percent", "humidity_percent")]:
                expr = 'tado_bridge_current_' + metric + '{job="tado-bridge",instance="tado-bridge:8080",room="' + room_value(room) + '"}'
                response = get_json(prometheus + "/api/v1/query?" + urllib.parse.urlencode({"query": expr}))["data"]["result"]
                if zone[field] is None:
                    assert response == [], "Missing room reading became a value"
                else:
                    assert len(response) == 1 and float(response[0]["value"][1]) == zone[field], "Gauge selected the wrong room"
        # The provisioned account can query history but cannot mutate it.
        sql = "UPDATE tado_bridge_snapshots SET payload = payload WHERE false"
        denied = get_json(grafana + "/api/ds/query", auth, {"queries": [{"refId": "A", "datasource": {"uid": "tado-bridge-postgres", "type": "grafana-postgresql-datasource"}, "rawSql": sql, "format": "table"}], "from": "now-1h", "to": "now"}, allow_error=True)
        assert denied["results"]["A"].get("error"), "Grafana database role can write history"
        privileges = get_json(grafana + "/api/ds/query", auth, {"queries": [{"refId": "A", "datasource": {"uid": "tado-bridge-postgres", "type": "grafana-postgresql-datasource"}, "rawQuery": True, "rawSql": "SELECT current_user, has_table_privilege(current_user, 'tado_bridge_snapshots', 'SELECT') AS can_read, has_table_privilege(current_user, 'tado_bridge_snapshots', 'INSERT,UPDATE,DELETE,TRUNCATE') AS can_write", "format": "table"}], "from": "now-1h", "to": "now"})
        assert privileges["results"]["A"]["frames"][0]["data"]["values"] == [["grafana_reader"], [True], [False]], "Unexpected Grafana role privileges"
        # Existing-volume bootstrap is idempotent and keeps history readable.
        command(["exec", "-T", "postgres", "sh", "/docker-entrypoint-initdb.d/20-grafana-reader.sh"], stdout=subprocess.DEVNULL)
        if screenshot:
            config = directory / "capture.json"
            config.write_text(json.dumps({"purpose": "tado-bridge-synthetic-preview", "url": grafana, "password": values["GRAFANA_ADMIN_PASSWORD"]}))
            os.chmod(config, 0o600)
            subprocess.run([os.environ.get("NODE", "node"), str(ROOT / "scripts/capture-grafana.mjs"), str(config), str(Path(screenshot).resolve())], check=True)
        print("Optional Compose: 11 Grafana queries, eight discovered rooms, nullable room gauges, authenticated scrapes and read-only history passed.")
        if keep:
            state_file = directory / "preview-state.json"
            with open(state_file, "x", opener=lambda path, flags: os.open(path, flags, 0o600)) as output:
                json.dump({"purpose": "tado-bridge-synthetic-preview", "project": project,
                           "environment": values, "grafana_url": grafana + "/d/tado-bridge",
                           "grafana_user": "admin", "grafana_password": values["GRAFANA_ADMIN_PASSWORD"]}, output)
            retained = True
            print("Grafana preview: " + grafana + "/d/tado-bridge")
            print("Private login and cleanup state: " + str(state_file))
            print("Stop with: python3 scripts/observability-smoke.py --stop " + str(state_file))
    except Exception:
        # Do not print Compose config or environment: both contain credentials.
        subprocess.run(compose + ["logs", "--tail", "30"], cwd=ROOT, env=env, check=False)
        raise
    finally:
        if not retained:
            subprocess.run(compose + ["down", "--volumes", "--timeout", "15"], cwd=ROOT, env=env, check=False, stdout=subprocess.DEVNULL)
            shutil.rmtree(directory)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--screenshot", help="capture synthetic Grafana PNG (Node and Chrome required)")
    parser.add_argument("--keep", action="store_true", help="leave a validated synthetic preview running for manual review")
    parser.add_argument("--stop", type=Path, help="stop a retained preview using its private preview-state.json file")
    args = parser.parse_args()
    if args.stop:
        if args.keep or args.screenshot:
            parser.error("--stop cannot be combined with --keep or --screenshot")
        stop(args.stop)
    else:
        run(args.screenshot, args.keep)
