//go:build linux && integration

package hook

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/compute/podman"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
	"github.com/Amrzxk/nephos/internal/network/topology"
	"github.com/Amrzxk/nephos/internal/service"
	"github.com/Amrzxk/nephos/internal/store"
)

type observedPlumber struct {
	engine     *topology.Engine
	calls      atomic.Int32
	beforePID1 atomic.Bool
	forceError bool
}

type hookFixture struct {
	ctx      context.Context
	store    *store.Store
	runtime  *podman.Client
	instance model.Instance
	vpc      model.VPC
	plumber  *observedPlumber
}

func (p *observedPlumber) EnsureENI(ctx context.Context, vpc model.VPC, subnet model.Subnet, eni model.ENI, target *netns.InstanceTarget) error {
	p.calls.Add(1)
	if p.forceError {
		return fmt.Errorf("forced createRuntime plumbing failure")
	}
	if err := p.engine.EnsureENI(ctx, vpc, subnet, eni, target); err != nil {
		return err
	}
	if err := netns.WithInstanceHandle(ctx, target, func(h *netlink.Handle) error {
		peer, err := h.LinkByName("eth0")
		if err != nil {
			return err
		}
		if peer.Type() != "veth" {
			return fmt.Errorf("hook did not plumb a veth")
		}
		return nil
	}); err != nil {
		return err
	}
	p.beforePID1.Store(true) // createRuntime has not returned to the OCI runtime.
	return nil
}
func hookWorld(t *testing.T, forceError bool) hookFixture {
	t.Helper()
	if os.Getenv("NEPHOS_IN_APPLIANCE") != "1" {
		t.Skip("requires the isolated privileged test appliance with packaged createRuntime hook")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	engine := topology.New()
	networks := service.NewNetwork(s, nil, nil)
	vpc, err := networks.CreateVPC(ctx, service.CreateVPCInput{Name: "hook", CIDRBlock: "10.0.0.0/16"}, "")
	if err != nil {
		t.Fatal(err)
	}
	subnet, err := networks.CreateSubnet(ctx, service.CreateSubnetInput{Name: "hook", VPCID: vpc.ID, CIDRBlock: "10.0.1.0/24", AvailabilityZone: "local-1a"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.EnsureVPC(ctx, vpc, []model.Subnet{subnet}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := engine.DeleteVPC(cleanup, vpc); err != nil {
			t.Error(err)
		}
	})
	if err := s.RecordTopology(ctx, vpc, []model.Subnet{subnet}, nil); err != nil {
		t.Fatal(err)
	}
	instance, err := service.NewInstances(s, nil, nil).Run(ctx, service.RunInstanceInput{Name: "hook", SubnetID: subnet.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	plumber := &observedPlumber{engine: engine, forceError: forceError}
	ln, err := Listen(ctx, SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: NewHandler(s, plumber), ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	t.Cleanup(func() {
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		<-done
		if err := os.Remove(SocketPath); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	runtime := podman.New("/run/podman/podman.sock")
	if err := runtime.EnsureImage(ctx, "nephos-ubuntu:dev"); err != nil {
		t.Fatal(err)
	}
	id, err := runtime.Create(ctx, instance)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := runtime.Delete(cleanup, id); err != nil {
			t.Error(err)
		}
	})
	if err := s.RecordRuntimeID(ctx, instance.ID, instance.Generation, string(id)); err != nil {
		t.Fatal(err)
	}
	instance.RuntimeID = string(id)
	return hookFixture{ctx: ctx, store: s, runtime: runtime, instance: instance, vpc: vpc, plumber: plumber}
}
func TestHookPlumbingBeforePID1(t *testing.T) {
	w := hookWorld(t, false)
	ctx, s, runtime, instance, plumber := w.ctx, w.store, w.runtime, w.instance, w.plumber
	if err := runtime.Start(ctx, compute.RuntimeID(instance.RuntimeID)); err != nil {
		t.Fatal(err)
	}
	if plumber.calls.Load() != 1 || !plumber.beforePID1.Load() {
		t.Fatalf("hook calls=%d before PID1=%t", plumber.calls.Load(), plumber.beforePID1.Load())
	}
	status, err := runtime.Inspect(ctx, compute.RuntimeID(instance.RuntimeID))
	if err != nil || !status.Running {
		t.Fatalf("container %+v %v", status, err)
	}
	current, err := s.GetInstance(ctx, "default", instance.ID)
	if err != nil || current.RuntimeID != instance.RuntimeID {
		t.Fatalf("committed identity %+v %v", current, err)
	}
}
func TestHookFailureBeforePID1(t *testing.T) {
	w := hookWorld(t, true)
	ctx, s, runtime, instance, vpc, plumber := w.ctx, w.store, w.runtime, w.instance, w.vpc, w.plumber
	if err := runtime.Start(ctx, compute.RuntimeID(instance.RuntimeID)); err == nil {
		t.Fatal("failed hook started PID 1")
	}
	if plumber.calls.Load() != 1 || plumber.beforePID1.Load() {
		t.Fatalf("failure plumbing calls=%d beforePID1=%t", plumber.calls.Load(), plumber.beforePID1.Load())
	}
	status, err := runtime.Inspect(ctx, compute.RuntimeID(instance.RuntimeID))
	if err != nil || status.Running {
		t.Fatalf("false running %+v %v", status, err)
	}
	current, err := s.GetInstance(ctx, "default", instance.ID)
	if err != nil || current.ObservedGeneration != 0 || current.State == model.InstanceRunning {
		t.Fatalf("false observed state %+v %v", current, err)
	}
	name, _ := netns.Name(vpc.ShortIndex)
	link := "ve" + strconv.FormatInt(instance.ENI.ShortIndex, 10)
	if err := netns.WithHandle(ctx, name, func(h *netlink.Handle) error {
		_, err := h.LinkByName(link)
		var missing netlink.LinkNotFoundError
		if errors.As(err, &missing) {
			return nil
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("failed hook left a live ENI")
	}); err != nil {
		t.Fatal(err)
	}
}
func TestHookRejectsForeignPID(t *testing.T) {
	w := hookWorld(t, false)
	ctx, instance, plumber := w.ctx, w.instance, w.plumber
	name := fmt.Sprintf("hook-foreign-%x", time.Now().UnixNano())
	out, err := exec.CommandContext(ctx, "podman", "run", "-d", "--name", name, "--label", "io.nephos.test=hook-foreign", "--userns=auto:size=65536", "--network=none", "--cap-add=NET_ADMIN", "--memory=1g", "--pids-limit=512", "nephos-ubuntu:dev", "/bin/sleep", "120").CombinedOutput()
	if err != nil {
		t.Fatalf("start test-owned foreign process: %v %s", err, out)
	}
	foreignID := strings.TrimSpace(string(out))
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(cleanup, "podman", "rm", "-f", "--time=0", foreignID).CombinedOutput(); err != nil {
			t.Errorf("remove test-owned foreign process: %v %s", err, out)
		}
	})
	out, err = exec.CommandContext(ctx, "podman", "inspect", "--format", "{{.State.Pid}}", foreignID).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect foreign PID: %v %s", err, out)
	}
	foreignPID, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	// Root peer credentials and a valid instance/runtime ID cannot redirect
	// the pinned ENI operation into another container's live namespace.
	err = Plumb(ctx, SocketPath, Request{InstanceID: instance.ID, ContainerID: instance.RuntimeID, PID: foreignPID})
	if err == nil || plumber.calls.Load() != 0 {
		t.Fatalf("foreign PID accepted: %v", err)
	}
}
