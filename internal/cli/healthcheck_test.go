package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthcheckDoesNotRequireCredentials(t *testing.T) {
	for _, status := range []int{200, 503, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/healthz" || r.Method != "GET" || r.Header.Get("Authorization") != "" {
					t.Error("healthcheck must use unauthenticated local liveness")
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			t.Setenv("LISTEN_ADDR", strings.TrimPrefix(server.URL, "http://"))
			t.Setenv("API_TOKEN", "")
			t.Setenv("ALLOW_NETWORK_EXPOSURE", "false")
			t.Setenv("METRICS_ENABLED", "false")
			var output, diagnostics bytes.Buffer
			err := run(context.Background(), []string{"healthcheck"}, "", &output, &diagnostics)
			if (err == nil) != (status == 200) {
				t.Fatalf("err=%v", err)
			}
			if status == 200 && output.String() != "healthy\n" {
				t.Fatal("missing health status")
			}
			if diagnostics.Len() != 0 {
				t.Fatal("healthcheck accessed provider request accounting")
			}
		})
	}
}
