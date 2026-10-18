package credentials

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorageAndLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "credentials.json")
	token := Token{AccessToken: "fixture-access", RefreshToken: "fixture-refresh", ExpiresAt: time.Now().UTC().Truncate(time.Second)}
	unlock, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(path); err == nil {
		t.Fatal("second process acquired lock")
	}
	unlock()
	unlock, err = Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for _, refresh := range []string{"fixture-refresh", "rotated"} {
		token.RefreshToken = refresh
		if err := Save(path, token); err != nil {
			t.Fatal(err)
		}
		actual, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if actual != token {
			t.Fatal("token did not round-trip")
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatalf("permissions: %v", info.Mode())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 2 {
		t.Fatal("temporary files left behind")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("accepted publicly readable credentials")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil {
		t.Fatal("accepted symlink")
	}
}
