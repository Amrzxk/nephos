package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevelopmentAMIPingDoesNotRequireNETRAW(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "images", "dev-ami", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{"libcap2-bin", "setcap -r /usr/bin/ping"} {
		if !strings.Contains(text, required) {
			t.Errorf("development AMI must contain %q for unprivileged ICMP", required)
		}
	}
}

func TestAppliancePrivatePodmanService(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "images", "appliance", "entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"umask 077", "/run/podman", "podman system service --time 0 unix:///run/podman/podman.sock", "--unix-socket /run/podman/podman.sock", "trap cleanup 0"} {
		if !strings.Contains(string(data), required) {
			t.Errorf("missing private runtime startup contract %q", required)
		}
	}
}

func TestApplianceFreshRuntimePreservesGraphroot(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "images", "appliance", "entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	firstPodman := strings.Index(text, "podman ")
	for _, path := range []string{"/var/lib/nephos/runroot", "/run/libpod", "/run/crun"} {
		mount := strings.Index(text, "tmpfs "+path)
		if mount < 0 || mount > firstPodman {
			t.Errorf("fresh private tmpfs at %s must precede every Podman command", path)
		}
		if !strings.Contains(text, "mount --make-private "+path) {
			t.Errorf("runtime tmpfs at %s must be private", path)
		}
	}
	storage, err := os.ReadFile(filepath.Join("..", "images", "appliance", "storage.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{`runroot = "/var/lib/nephos/runroot"`, `graphroot = "/var/lib/nephos/containers"`} {
		if !strings.Contains(string(storage), line) {
			t.Errorf("changed stored runtime path: %s", line)
		}
	}
	if strings.Contains(text, "tmpfs /var/lib/nephos/containers") {
		t.Fatal("persistent graphroot hidden by ephemeral mount")
	}
	containers, err := os.ReadFile(filepath.Join("..", "images", "appliance", "containers.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(containers), `tmp_dir = "/run/libpod"`) {
		t.Fatal("libpod temp path must remain pinned to validated rootful default")
	}
}
