package appliance

import (
	"testing"

	"github.com/moby/moby/api/types/network"
)

func TestContainerConfigNeverUsesHostNamespaces(t *testing.T) {
	cfg, host := ContainerConfig(Limits{MemoryBytes: 4 << 30, NanoCPUs: 2e9, PIDs: 4096})
	if cfg.Image != "nephos-appliance:dev" || !host.Privileged {
		t.Fatal("wrong appliance image or missing privilege")
	}
	if host.NetworkMode == "host" || host.PidMode == "host" || host.CgroupnsMode != "private" {
		t.Fatal("host isolation lost")
	}
	if len(host.Binds) != 0 || len(host.Mounts) != 1 || host.Mounts[0].Source != "nephos-data" || host.Mounts[0].Target != "/var/lib/nephos" {
		t.Fatal("unexpected mount")
	}
	port := network.MustParsePort("7788/tcp")
	if len(host.PortBindings[port]) != 1 || host.PortBindings[port][0].HostIP.String() != "127.0.0.1" || host.PortBindings[port][0].HostPort != "7788" {
		t.Fatal("API is not loopback-only")
	}
}

func TestContainerConfigEnforcesLimitsAndOwnership(t *testing.T) {
	cfg, host := ContainerConfig(Limits{MemoryBytes: 4 << 30, NanoCPUs: 2e9, PIDs: 4096})
	if host.Memory != 4<<30 || host.NanoCPUs != 2e9 || host.PidsLimit == nil || *host.PidsLimit != 4096 {
		t.Fatal("appliance resource limits changed")
	}
	if cfg.Labels[ownerLabel] != "true" {
		t.Fatal("appliance ownership label missing")
	}
}
