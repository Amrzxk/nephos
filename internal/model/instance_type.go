package model

import "slices"

// InstanceTypeSpec is one entry of the fixed M2 instance-type catalogue. vCPU
// and memory follow the AWS t3 family; the task limit scales with memory.
// Burstable CPU credits are not simulated: vCPUs are a hard CFS quota.
type InstanceTypeSpec struct {
	Name        string
	VCPUs       int64
	MemoryBytes int64
	Tasks       int64
}

// InstanceTypes is ordered smallest first.
var InstanceTypes = []InstanceTypeSpec{
	{Name: "t3.nano", VCPUs: 2, MemoryBytes: 512 << 20, Tasks: 256},
	{Name: "t3.micro", VCPUs: 2, MemoryBytes: 1 << 30, Tasks: 512},
	{Name: "t3.small", VCPUs: 2, MemoryBytes: 2 << 30, Tasks: 1024},
	{Name: "t3.medium", VCPUs: 2, MemoryBytes: 4 << 30, Tasks: 2048},
}

// DefaultInstanceType is used when a run request names none.
const DefaultInstanceType = "t3.micro"

// LookupInstanceType returns the catalogue entry for name.
func LookupInstanceType(name string) (InstanceTypeSpec, bool) {
	i := slices.IndexFunc(InstanceTypes, func(spec InstanceTypeSpec) bool { return spec.Name == name })
	if i < 0 {
		return InstanceTypeSpec{}, false
	}
	return InstanceTypes[i], true
}
