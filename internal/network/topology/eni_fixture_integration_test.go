//go:build linux && integration

package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
)

type eniTestInstance struct {
	runtimeID string
	target    *netns.InstanceTarget
	eni       model.ENI
}

func eniCommand(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
}

func eniMustCommand(t *testing.T, args ...string) []byte {
	t.Helper()
	out, err := eniCommand(t, args...)
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return out
}

func eniTestWorld(t *testing.T) (context.Context, *Engine, model.VPC, []model.Subnet) {
	t.Helper()
	if os.Getenv("NEPHOS_IN_APPLIANCE") != "1" {
		t.Skip("requires the isolated privileged Nephos test appliance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	before := applianceRootNetworkState(t)
	t.Cleanup(func() {
		if after := applianceRootNetworkState(t); after != before {
			t.Errorf("appliance root network changed:\nbefore:%s\nafter:%s", before, after)
		}
	})
	engine := New()
	vpc := model.VPC{ID: fmt.Sprintf("vpc-%017x", unusedIndex(t)), WorkspaceID: "default", ShortIndex: unusedIndex(t), CIDRBlock: netip.MustParsePrefix("10.0.0.0/16")}
	subnets := []model.Subnet{
		{ID: "subnet-00000000000000001", WorkspaceID: "default", VPCID: vpc.ID, CIDRBlock: netip.MustParsePrefix("10.0.1.0/24")},
		{ID: "subnet-00000000000000002", WorkspaceID: "default", VPCID: vpc.ID, CIDRBlock: netip.MustParsePrefix("10.0.2.0/24")},
	}
	if err := engine.EnsureVPC(ctx, vpc, subnets); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.DeleteVPC(context.Background(), vpc); err != nil {
			t.Error(err)
		}
	})
	return ctx, engine, vpc, subnets
}

func eniTestContainer(t *testing.T, subnet model.Subnet, privateIP string) eniTestInstance {
	t.Helper()
	index := unusedIndex(t)
	name := fmt.Sprintf("i-%017x", index)
	out := eniMustCommand(t, "podman", "run", "-d", "--name", name, "--label", "io.nephos.test=eni-integration",
		"--systemd=always", "--userns=auto:size=65536", "--network=none", "--cap-add=NET_ADMIN", "--sysctl", "net.ipv4.ping_group_range=0 65535", "--memory=1g", "--cpus=2", "--pids-limit=512",
		"nephos-ubuntu:dev", "/bin/sleep", "300")
	runtimeID := strings.TrimSpace(string(out))
	t.Cleanup(func() {
		if out, err := eniCommand(t, "podman", "rm", "-f", "--time=0", runtimeID); err != nil {
			t.Errorf("remove test-owned instance %s: %v %s", runtimeID, err, out)
		}
	})
	pidText := eniMustCommand(t, "podman", "inspect", "--format", "{{.State.Pid}}", runtimeID)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidText)))
	if err != nil {
		t.Fatal(err)
	}
	target, err := netns.OpenInstance(context.Background(), pid, runtimeID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := target.Close(); err != nil {
			t.Error(err)
		}
	})
	if wrong, err := netns.OpenInstance(context.Background(), pid, strings.Repeat("f", 64)); err == nil {
		wrong.Close()
		t.Fatal("wrong runtime identity accepted")
	}
	return eniTestInstance{runtimeID: runtimeID, target: target,
		eni: model.ENI{ID: fmt.Sprintf("eni-%017x", index), WorkspaceID: "default", InstanceID: name, SubnetID: subnet.ID,
			ShortIndex: index, PrivateIP: netip.MustParseAddr(privateIP), MACAddress: fmt.Sprintf("02:00:%02x:%02x:%02x:%02x", byte(index>>24), byte(index>>16), byte(index>>8), byte(index))}}
}

func ensureTestENI(t *testing.T, ctx context.Context, engine *Engine, vpc model.VPC, subnet model.Subnet, instance eniTestInstance) {
	t.Helper()
	if err := engine.EnsureENI(ctx, vpc, subnet, instance.eni, instance.target); err != nil {
		t.Fatal(err)
	}
}

func witnessStart(t *testing.T, instance eniTestInstance) {
	t.Helper()
	eniMustCommand(t, "podman", "exec", instance.runtimeID, "nft", "add", "table", "inet", "witness")
	eniMustCommand(t, "podman", "exec", instance.runtimeID, "nft", "add", "chain", "inet", "witness", "input", "{ type filter hook input priority 0; policy accept; }")
	eniMustCommand(t, "podman", "exec", instance.runtimeID, "nft", "add", "rule", "inet", "witness", "input", "ip", "protocol", "icmp", "counter")
}

func witnessPackets(t *testing.T, instance eniTestInstance) uint64 {
	t.Helper()
	raw := eniMustCommand(t, "podman", "exec", instance.runtimeID, "nft", "-j", "list", "chain", "inet", "witness", "input")
	var doc struct {
		Nftables []struct {
			Rule *struct {
				Expr []struct{ Counter *struct{ Packets uint64 } }
			}
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var packets uint64
	for _, entry := range doc.Nftables {
		if entry.Rule != nil {
			for _, expr := range entry.Rule.Expr {
				if expr.Counter != nil {
					packets += expr.Counter.Packets
				}
			}
		}
	}
	return packets
}
