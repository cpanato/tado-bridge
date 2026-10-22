package history

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestURLValidationDoesNotLeakSecrets(t *testing.T) {
	for _, value := range []string{
		"", "secret", "postgres://user:SECRET@host/db", "https://user:SECRET@host/db?sslmode=require",
		"postgres://user:SECRET@host/?sslmode=require", "postgres://user:SECRET@/db?sslmode=require",
		"postgres://user:SECRET@host/db?sslmode=prefer", "postgres://user:SECRET@host/db?sslmode=disable&sslmode=require",
	} {
		if err := ValidateURL(value); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("unsafe validation result for invalid URL")
		}
	}
	for _, mode := range []string{"disable", "require", "verify-ca", "verify-full"} {
		if err := ValidateURL("postgresql://user:SECRET@localhost/db?sslmode=" + mode); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenRejectsConfigurationWithoutLeakingIt(t *testing.T) {
	for _, tc := range []struct {
		value     string
		retention time.Duration
	}{
		{"postgres://user:SECRET@localhost/db", DefaultRetention},
		{"postgres://user:SECRET@localhost:invalid/db?sslmode=disable", DefaultRetention},
		{"postgres://user:SECRET@localhost/db?sslmode=disable", time.Hour},
	} {
		if db, err := Open(t.Context(), tc.value, tc.retention); err == nil || db != nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("invalid configuration accepted or exposed")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if db, err := Open(ctx, "postgres://user:SECRET@127.0.0.1:1/db?sslmode=disable", DefaultRetention); err == nil || db != nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("initialization ignored cancellation or exposed driver error")
	}
}

func TestQueryBounds(t *testing.T) {
	now := time.Now()
	valid := Query{From: now.Add(-time.Hour), To: now, Limit: 100}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []Query{
		{}, {From: now, To: now, Limit: 1}, {From: now, To: now.Add(-time.Hour), Limit: 1},
		{From: now.Add(-MaxRange - time.Second), To: now, Limit: 1},
		{From: valid.From, To: now, Limit: 0}, {From: valid.From, To: now, Limit: 101},
	} {
		if query.Validate() == nil {
			t.Fatal("unbounded query accepted")
		}
	}
}
