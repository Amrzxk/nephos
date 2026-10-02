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
