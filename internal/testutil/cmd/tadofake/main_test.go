package main

import "testing"

func TestOAuthAttemptContract(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		expected    int
		valid       bool
	}{
		{"first refresh", "1", 1, true},
		{"transport retry before refresh", "2", 1, true},
		{"refresh never attempted", "0", 1, false},
		{"persisted token reused", "0", 0, true},
		{"unnecessary refresh after restart", "1", 0, false},
		{"ten refreshes cannot match zero", "10", 0, false},
		{"floating point representation", "1.0", 1, true},
		{"invalid sample", "invalid", 1, false},
		{"NaN is not an attempt", "NaN", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(`tado_bridge_upstream_requests_total{kind="oauth"} ` + tc.value + "\n")
			if err := checkOAuthAttempts(data, tc.expected); (err == nil) != tc.valid {
				t.Fatalf("valid=%t, error=%v", tc.valid, err)
			}
		})
	}
	if err := checkOAuthAttempts(nil, 0); err == nil {
		t.Fatal("missing metric must not imply token reuse")
	}
}
