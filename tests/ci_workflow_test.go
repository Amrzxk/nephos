package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNativeDockerWorkflowRunsVPCSubnetSmokeAfterBootstrap(t *testing.T) {
	path := filepath.Join("..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			RunsOn string `yaml:"runs-on"`
			Steps  []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	job, ok := workflow.Jobs["appliance-smoke"]
	if !ok || job.RunsOn != "ubuntu-24.04" {
		t.Fatalf("native Docker job missing or wrong runner: %+v", job)
	}
	bootstrapAt, networkAt, instanceAt := -1, -1, -1
	for i, step := range job.Steps {
		command := strings.TrimSpace(step.Run)
		if command == "bash tests/appliance-cli-smoke.sh" {
			bootstrapAt = i
		}
		if command == "bash tests/vpc-subnet-smoke.sh" {
			networkAt = i
		}
		if command == "bash tests/instance-ping-smoke.sh" {
			instanceAt = i
		}
	}
	if bootstrapAt < 0 || networkAt <= bootstrapAt {
		t.Fatalf("VPC/subnet smoke must run after bootstrap: bootstrap=%d network=%d", bootstrapAt, networkAt)
	}
	if instanceAt <= networkAt {
		t.Fatalf("instance packet smoke must run after VPC/subnet smoke: network=%d instances=%d", networkAt, instanceAt)
	}
}

// make e2e and the native-Docker CI job must run the same real-appliance
// suites in the same order, after the in-appliance integration tests.
func TestNativeDockerWorkflowMatchesMakeE2E(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join("..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	var scripts []string
	inList := false
	for _, line := range strings.Split(string(makefile), "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "\\"))
		switch {
		case strings.HasPrefix(line, "E2E_SCRIPTS :="):
			inList = true
		case inList && trimmed == "":
			inList = false
		case inList:
			scripts = append(scripts, "bash "+trimmed)
		}
		if inList && !strings.HasSuffix(strings.TrimSpace(line), "\\") {
			inList = false
		}
	}
	if len(scripts) < 7 || scripts[len(scripts)-1] != "bash tests/m1-e2e.sh" {
		t.Fatalf("E2E_SCRIPTS must list every suite and end with the M1 demo: %v", scripts)
	}
	data, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	var ci []string
	integrationAt := -1
	for _, step := range workflow.Jobs["appliance-smoke"].Steps {
		command := strings.TrimSpace(step.Run)
		if command == "make test-integration" {
			integrationAt = len(ci)
		}
		if strings.HasPrefix(command, "bash tests/") && strings.HasSuffix(command, ".sh") {
			ci = append(ci, command)
		}
	}
	if strings.Join(ci, "\n") != strings.Join(scripts, "\n") {
		t.Fatalf("CI suites differ from make e2e:\nCI:   %v\nmake: %v", ci, scripts)
	}
	if integrationAt != 0 {
		t.Fatalf("integration tests must run before every real-appliance suite (at %d)", integrationAt)
	}
}
