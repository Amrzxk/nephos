package appliance

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func writeDockerContext(t *testing.T, configDir, name, host string) {
	t.Helper()
	digest := sha256.Sum256([]byte(name))
	dir := filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(digest[:]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := `{"Name":"` + name + `","Endpoints":{"docker":{"Host":"` + host + `","SkipTLSVerify":false}}}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDockerContextHost(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, ".docker")
	writeDockerContext(t, configDir, "colima", "unix:///Users/learner/.colima/default/docker.sock")
	writeDockerContext(t, configDir, "desktop-linux", "unix:///home/learner/.docker/desktop/docker.sock")
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"currentContext":"colima"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		env     map[string]string
		home    string
		want    string
		wantErr bool
	}{
		{"DOCKER_HOST wins", map[string]string{"DOCKER_HOST": "tcp://127.0.0.1:2375"}, home, "", false},
		{"current context from config", nil, home, "unix:///Users/learner/.colima/default/docker.sock", false},
		{"DOCKER_CONTEXT overrides config", map[string]string{"DOCKER_CONTEXT": "desktop-linux"}, home, "unix:///home/learner/.docker/desktop/docker.sock", false},
		{"default context uses client defaults", map[string]string{"DOCKER_CONTEXT": "default"}, home, "", false},
		{"no Docker config", nil, t.TempDir(), "", false},
		{"no home", nil, "", "", false},
		{"unknown context", map[string]string{"DOCKER_CONTEXT": "missing"}, home, "", true},
		{"DOCKER_CONFIG relocates config", map[string]string{"DOCKER_CONFIG": configDir}, "", "unix:///Users/learner/.colima/default/docker.sock", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string { return tt.env[key] }
			got, err := dockerContextHost(getenv, tt.home)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("got %q, %v; want %q (error %v)", got, err, tt.want, tt.wantErr)
			}
		})
	}
}
