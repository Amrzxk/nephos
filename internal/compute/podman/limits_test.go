package podman

import (
	"testing"

	"github.com/Amrzxk/nephos/internal/model"
)

func TestResourceLimitsFollowInstanceType(t *testing.T) {
	for _, spec := range model.InstanceTypes {
		limits := resourceLimits(spec)
		memory := limits["memory"].(map[string]int64)
		cpu := limits["cpu"].(map[string]int64)
		pids := limits["pids"].(map[string]int64)
		if memory["limit"] != spec.MemoryBytes || memory["swap"] != spec.MemoryBytes {
			t.Errorf("%s memory=%v: swap must equal the limit", spec.Name, memory)
		}
		if cpu["quota"] != spec.VCPUs*cpu["period"] || pids["limit"] != spec.Tasks {
			t.Errorf("%s cpu=%v pids=%v", spec.Name, cpu, pids)
		}
	}
	micro, _ := model.LookupInstanceType("t3.micro")
	if got := resourceLimits(micro)["pids"].(map[string]int64)["limit"]; got != 512 {
		t.Fatalf("t3.micro must keep M1's 512-task limit, got %d", got)
	}
}
