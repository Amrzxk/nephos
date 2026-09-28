package credentials

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreatePersistsRestrictedToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "api-token")
	first, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadOrCreate(path)
	if err != nil || first != again || len(first) < 43 {
		t.Fatalf("token not stable or too short: len=%d err=%v", len(again), err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode: %v err=%v", info, err)
	}
}

func TestLoadOrCreateRejectsInsecureExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-token")
	if err := os.WriteFile(path, []byte("insecure"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(path); err == nil {
		t.Fatal("accepted an over-permissive token file")
	}
}

func TestLoadOrCreateRejectsEmptyExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-token")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(path); err == nil {
		t.Fatal("accepted an empty token file")
	}
}
