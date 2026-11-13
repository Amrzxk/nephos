package appliance

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testToken() string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
}

func tokenArchive(t *testing.T, entries ...struct{ name, kind, body string }) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, e := range entries {
		kind := byte(tar.TypeReg)
		if e.kind == "symlink" {
			kind = tar.TypeSymlink
		}
		h := &tar.Header{Name: e.name, Mode: 0o600, Typeflag: kind, Size: int64(len(e.body))}
		if kind == tar.TypeSymlink {
			h.Linkname = "elsewhere"
			h.Size = 0
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			if _, err := w.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestCredentialStoreWritesRestrictedToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".nephos", "credentials")
	s := LocalCredentialStore{Path: path}
	archive := tokenArchive(t, struct{ name, kind, body string }{"api-token", "file", testToken()})
	if err := s.Save(context.Background(), bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != testToken() {
		t.Fatalf("credential bytes=%q err=%v", data, err)
	}
	// Windows has no Unix mode bits; restrictToOwner sets a DACL there instead.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("credential mode=%v err=%v", info, err)
		}
	}
	got, err := LoadCredential(path)
	if err != nil || got != testToken() {
		t.Fatalf("LoadCredential = %q, %v", got, err)
	}
}

func TestLoadCredentialRejectsUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string, mode os.FileMode) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	tests := []struct {
		name string
		path string
		unix bool // relies on Unix permission bits
	}{
		{"missing", filepath.Join(dir, "absent"), false},
		{"malformed", write("malformed", "not-a-token", 0o600), false},
		{"oversized", write("oversized", strings.Repeat("A", 4096), 0o600), false},
		{"directory", dir, false},
		{"group readable", write("shared", testToken(), 0o640), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.unix && runtime.GOOS == "windows" {
				t.Skip("Unix permission bits do not exist on Windows; access is DACL-based")
			}
			if _, err := LoadCredential(tt.path); err == nil {
				t.Fatal("accepted an unsafe credential")
			}
		})
	}
}

func TestCredentialStoreRejectsUntrustedArchiveWithoutReplacingCredential(t *testing.T) {
	good := testToken()
	for _, tc := range []struct {
		name    string
		entries []struct{ name, kind, body string }
	}{
		{"traversal", []struct{ name, kind, body string }{{"../api-token", "file", good}}},
		{"symlink", []struct{ name, kind, body string }{{"api-token", "symlink", ""}}},
		{"two entries", []struct{ name, kind, body string }{{"api-token", "file", good}, {"extra", "file", "x"}}},
		{"empty", []struct{ name, kind, body string }{{"api-token", "file", ""}}},
		{"malformed", []struct{ name, kind, body string }{{"api-token", "file", "not-a-token"}}},
		{"oversized", []struct{ name, kind, body string }{{"api-token", "file", strings.Repeat("x", 1<<20)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".nephos", "credentials")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			s := LocalCredentialStore{Path: path}
			if err := s.Save(context.Background(), bytes.NewReader(tokenArchive(t, tc.entries...))); err == nil {
				t.Fatal("accepted malicious archive")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "old" {
				t.Fatalf("overwrote credential: %q, %v", data, err)
			}
		})
	}
}
