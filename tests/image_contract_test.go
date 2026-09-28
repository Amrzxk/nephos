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
