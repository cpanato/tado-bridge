package packaging_test

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"
)

func render(t *testing.T, extra ...string) []map[string]any {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("Helm is required; CI runs these checks in the packaging job")
	}
	args := append([]string{"template", "test", filepath.Join("..", "..", "charts", "tado-bridge")}, extra...)
	data, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, data)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var documents []map[string]any
	for {
		var doc map[string]any
		err := decoder.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if doc != nil {
			documents = append(documents, doc)
		}
	}
	return documents
}
func object(t *testing.T, root map[string]any, keys ...string) map[string]any {
	t.Helper()
	for _, key := range keys {
		var ok bool
		root, ok = root[key].(map[string]any)
		if !ok {
			t.Fatalf("missing object %s", key)
		}
	}
	return root
}
func TestChartPrivateSingletonAndWritableCredentials(t *testing.T) {
	docs := render(t)
	if len(docs) != 2 {
		t.Fatalf("unexpected objects: %d", len(docs))
	}
	var pod map[string]any
	for _, doc := range docs {
		spec := object(t, doc, "spec")
		switch doc["kind"] {
		case "Deployment":
			if spec["replicas"] != 1 || object(t, spec, "strategy")["type"] != "Recreate" {
				t.Fatal("credential rotation requires one non-overlapping replica")
			}
			pod = object(t, spec, "template", "spec")
		case "Service":
			if spec["type"] != "ClusterIP" {
				t.Fatal("service must not expose public ingress")
			}
		default:
			t.Fatalf("unexpected resource %v", doc["kind"])
		}
	}
	if pod == nil {
		t.Fatal("missing pod")
	}
	if pod["automountServiceAccountToken"] != false {
		t.Fatal("unneeded Kubernetes credentials")
	}
	security := object(t, pod, "securityContext")
	if security["runAsNonRoot"] != true || security["runAsUser"] != 65532 || object(t, security, "seccompProfile")["type"] != "RuntimeDefault" {
		t.Fatal("unsafe pod identity or seccomp")
	}
	container := pod["containers"].([]any)[0].(map[string]any)
	security = object(t, container, "securityContext")
	if security["allowPrivilegeEscalation"] != false || security["readOnlyRootFilesystem"] != true {
		t.Fatal("unsafe container filesystem or privilege escalation")
	}
	drops := object(t, security, "capabilities")["drop"].([]any)
	if len(drops) != 1 || drops[0] != "ALL" {
		t.Fatal("capabilities must be dropped")
	}
	volumes := pod["volumes"].([]any)
	if object(t, volumes[0].(map[string]any), "persistentVolumeClaim")["claimName"] != "tado-bridge-credentials" {
		t.Fatal("credentials must use existing writable storage")
	}
	mount := container["volumeMounts"].([]any)[0].(map[string]any)
	if mount["mountPath"] != "/var/lib/tado-bridge" || mount["readOnly"] == true || mount["subPath"] != nil {
		t.Fatal("mount the whole writable credentials directory for atomic rotation")
	}
	for _, name := range []string{"startupProbe", "livenessProbe"} {
		if object(t, container, name, "httpGet")["path"] != "/healthz" {
			t.Fatal("upstream failures must not restart the pod")
		}
	}
	if object(t, container, "readinessProbe", "httpGet")["path"] != "/readyz" {
		t.Fatal("readiness must check cache freshness")
	}
	env := environment(container)
	if object(t, env["API_TOKEN"], "valueFrom", "secretKeyRef")["name"] != "tado-bridge-secrets" {
		t.Fatal("API token must reference an existing Secret")
	}
	if env["TRMNL_ENABLED"]["value"] != "false" || env["TRMNL_WEBHOOK_URL"] != nil {
		t.Fatal("TRMNL must be disabled by default")
	}
	if env["METRICS_ROOM_DETAILS"]["value"] != "false" {
		t.Fatal("room labels require explicit opt-in")
	}
	if env["HISTORY_DATABASE_URL"] != nil || env["HISTORY_RETENTION"] != nil {
		t.Fatal("history must be disabled by default")
	}
}

func TestChartHistoryUsesExistingSecret(t *testing.T) {
	docs := render(t, "--set", "history.enabled=true", "--set", "history.existingSecret=private-history", "--set", "history.databaseURLKey=connection-url", "--set", "history.retention=720h")
	for _, doc := range docs {
		if doc["kind"] != "Deployment" {
			continue
		}
		pod := object(t, doc, "spec", "template", "spec")
		container := pod["containers"].([]any)[0].(map[string]any)
		env := environment(container)
		ref := object(t, env["HISTORY_DATABASE_URL"], "valueFrom", "secretKeyRef")
		if ref["name"] != "private-history" || ref["key"] != "connection-url" || env["HISTORY_RETENTION"]["value"] != "720h" {
			t.Fatal("history must use the selected Secret and retention")
		}
		if len(pod["volumes"].([]any)) != 1 {
			t.Fatal("PostgreSQL storage belongs to the external database")
		}
	}
}
func environment(container map[string]any) map[string]map[string]any {
	env := make(map[string]map[string]any)
	for _, item := range container["env"].([]any) {
		entry := item.(map[string]any)
		env[entry["name"].(string)] = entry
	}
	return env
}
func TestChartExplicitTRMNLAndDigest(t *testing.T) {
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	docs := render(t, "--set", "trmnl.enabled=true", "--set-json", `trmnl.rooms=[{"home_id":"fixture-home","zone_id":"fixture-zone","name":"Room 1"}]`, "--set", "image.digest="+digest, "--set", "service.enabled=false")
	if len(docs) != 1 {
		t.Fatal("disabled Service must be omitted")
	}
	pod := object(t, docs[0], "spec", "template", "spec")
	container := pod["containers"].([]any)[0].(map[string]any)
	if container["image"] != "ko.local/tado-bridge@"+digest {
		t.Fatal("digest must take precedence over mutable tag")
	}
	env := environment(container)
	if object(t, env["TRMNL_WEBHOOK_URL"], "valueFrom", "secretKeyRef")["key"] != "trmnl-webhook-url" {
		t.Fatal("webhook must come from Secret")
	}
	if env["TRMNL_ROOMS"]["value"] != `[{"home_id":"fixture-home","name":"Room 1","zone_id":"fixture-zone"}]` {
		t.Fatal("incorrect explicit room selection")
	}
}
func TestChartRejectsUnsafeOrIncompleteValues(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("Helm is required")
	}
	for _, values := range []string{"replicaCount=2", "service.type=LoadBalancer", "credentials.existingClaim=", "secrets.existingSecret=", "trmnl.enabled=true", "image.digest=invalid", "config.dailyRequestBudget=0", "history.existingSecret=", "history.databaseURLKey=", "history.retention=", "config.metricsRoomDetails=true"} {
		t.Run(values, func(t *testing.T) {
			if err := exec.Command("helm", "template", "test", filepath.Join("..", "..", "charts", "tado-bridge"), "--set", values).Run(); err == nil {
				t.Fatal("unsafe or incomplete values accepted")
			}
		})
	}
}

func TestChartCustomCAKeepsPrivateKeysOutOfApplication(t *testing.T) {
	docs := render(t, "--set", "tls.existingCASecret=fixture-tls", "--set-json", `hostAliases=[{"ip":"10.96.0.50","hostnames":["my.tado.com","login.tado.com","hops.tado.com"]}]`)
	for _, doc := range docs {
		if doc["kind"] != "Deployment" {
			continue
		}
		pod := object(t, doc, "spec", "template", "spec")
		if len(pod["hostAliases"].([]any)) != 1 {
			t.Fatal("missing explicit host override")
		}
		container := pod["containers"].([]any)[0].(map[string]any)
		if environment(container)["SSL_CERT_FILE"]["value"] != "/etc/tado-bridge-ca/ca.crt" {
			t.Fatal("missing custom CA bundle")
		}
		for _, entry := range container["volumeMounts"].([]any) {
			mount := entry.(map[string]any)
			if mount["name"] == "custom-ca" && mount["readOnly"] != true {
				t.Fatal("CA mount must be read-only")
			}
		}
		for _, entry := range pod["volumes"].([]any) {
			volume := entry.(map[string]any)
			if volume["name"] != "custom-ca" {
				continue
			}
			items := object(t, volume, "secret")["items"].([]any)
			if len(items) != 1 || items[0].(map[string]any)["key"] != "ca.crt" {
				t.Fatal("application must not receive the fixture TLS private key")
			}
		}
		if object(t, container, "readinessProbe", "httpGet")["path"] != "/readyz" {
			t.Fatal("fixture must exercise production readiness")
		}
	}
}

func TestChartTRMNLSummaryRequiresHistoryAndMapsTimezone(t *testing.T) {
	docs := render(t, "--set", "trmnl.enabled=true", "--set", "history.enabled=true", "--set", "trmnl.historySummary=true", "--set", "trmnl.summaryTimezone=Europe/Berlin", "--set", "service.enabled=false", "--set-json", `trmnl.rooms=[{"home_id":"h","zone_id":"z"}]`)
	pod := object(t, docs[0], "spec", "template", "spec")
	env := environment(pod["containers"].([]any)[0].(map[string]any))
	if env["TRMNL_HISTORY_SUMMARY"]["value"] != "true" || env["TRMNL_SUMMARY_TIMEZONE"]["value"] != "Europe/Berlin" || env["HISTORY_DATABASE_URL"] == nil {
		t.Fatal("summary settings did not reach the collector")
	}
	if err := exec.Command("helm", "template", "test", filepath.Join("..", "..", "charts", "tado-bridge"), "--set", "trmnl.historySummary=true").Run(); err == nil {
		t.Fatal("summary enabled without its required consumers")
	}
}
