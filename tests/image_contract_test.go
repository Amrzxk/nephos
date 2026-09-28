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
