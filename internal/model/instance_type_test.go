package model

import "testing"

func TestInstanceTypeCatalogue(t *testing.T) {
	want := map[string]int64{"t3.nano": 512 << 20, "t3.micro": 1 << 30, "t3.small": 2 << 30, "t3.medium": 4 << 30}
	for name, memory := range want {
		spec, ok := LookupInstanceType(name)
		if !ok || spec.MemoryBytes != memory || spec.VCPUs != 2 || spec.Tasks <= 0 {
			t.Errorf("%s: %+v ok=%v", name, spec, ok)
		}
	}
	if _, ok := LookupInstanceType("t3.large"); ok {
		t.Error("t3.large is outside the M2 catalogue")
	}
	if _, ok := LookupInstanceType(DefaultInstanceType); !ok {
		t.Error("default instance type missing from catalogue")
	}
	for i := 1; i < len(InstanceTypes); i++ {
		if InstanceTypes[i].MemoryBytes <= InstanceTypes[i-1].MemoryBytes {
			t.Errorf("catalogue not ordered at %s", InstanceTypes[i].Name)
		}
	}
}
