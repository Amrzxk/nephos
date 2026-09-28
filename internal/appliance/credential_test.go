package appliance

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
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
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode=%v err=%v", info, err)
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
