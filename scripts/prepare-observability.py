#!/usr/bin/env python3
"""Prepare private Compose state and map the importable Grafana dashboard UIDs."""

import argparse
import json
import os
from pathlib import Path
import stat
import sys


def private_directory(path):
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid():
        raise ValueError("state must be an owned directory, not a symlink")
    if stat.S_IMODE(info.st_mode) & 0o077:
        raise ValueError("state directory must have mode 0700")


def private_write(path, data):
    # Refuse symlinks and unrelated files rather than following them as secrets.
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "w") as output:
        info = os.fstat(output.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid():
            raise ValueError("generated files must be owned regular files")
        os.fchmod(output.fileno(), 0o600)
        output.truncate(0)
        output.write(data)


def prepare(directory, token):
    if not token or any(ord(char) <= 32 or ord(char) >= 127 for char in token):
        raise ValueError("API_TOKEN must contain printable ASCII without whitespace")
    directory = directory.absolute()
    private_directory(directory)
    for name in ("prometheus", "grafana", "dashboards"):
        private_directory(directory / name)
    source = Path(__file__).resolve().parents[1] / "integrations/grafana/tado-bridge.json"
    dashboard = json.loads(source.read_text())
    dashboard.pop("__inputs", None)
    data = json.dumps(dashboard, indent=2, ensure_ascii=False)
    data = data.replace("${DS_PROMETHEUS}", "tado-bridge-prometheus")
    data = data.replace("${DS_POSTGRESQL}", "tado-bridge-postgres")
    private_write(directory / "api-token", token)
    private_write(directory / "dashboards/tado-bridge.json", data + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path, help="private persistent state directory")
    args = parser.parse_args()
    try:
        prepare(args.directory, os.environ.get("API_TOKEN", ""))
    except (OSError, ValueError):
        print("Preparation failed: check API_TOKEN and owned, mode-0700 state directories.", file=sys.stderr)
        return 1
    print("Private observability state and dashboard prepared.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
