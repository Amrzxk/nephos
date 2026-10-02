package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/service"
	"github.com/Amrzxk/nephos/internal/store"
)

type instanceTestNetwork struct {
	mu        sync.Mutex
	ready     bool
	deletes   int
	deleteErr error
}

func (n *instanceTestNetwork) Ready(context.Context, model.Instance) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ready, nil
}
func (n *instanceTestNetwork) DeleteENI(context.Context, model.VPC, model.ENI) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.deletes++
	return n.deleteErr
}

type instanceTestRuntime struct {
	mu         sync.Mutex
	creates    int
	starts     int
	deletes    int
	startErr   error
	deleteErr  error
	createHook func()
	startHook  func()
	running    map[compute.RuntimeID]bool
}

func (r *instanceTestRuntime) EnsureImage(context.Context, string) error { return nil }
func (r *instanceTestRuntime) Lookup(context.Context, compute.Identity) (compute.Reference, error) {
	return compute.Reference{}, compute.ErrNotFound
}
func (r *instanceTestRuntime) Create(_ context.Context, instance model.Instance) (compute.CreateResult, error) {
	r.mu.Lock()
	r.creates++
	hook := r.createHook
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	return compute.CreateResult{Reference: compute.Reference{Identity: compute.Identity{WorkspaceID: instance.WorkspaceID, InstanceID: instance.ID}, ID: compute.RuntimeID(fmt.Sprintf("%064x", instance.ShortIndex))}, Created: true}, nil
}
func (r *instanceTestRuntime) Start(_ context.Context, ref compute.Reference) error {
	r.mu.Lock()
	r.starts++
	hook := r.startHook
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.startErr != nil {
		return r.startErr
	}
	if r.running == nil {
		r.running = make(map[compute.RuntimeID]bool)
	}
	r.running[ref.ID] = true
	return nil
}
func (r *instanceTestRuntime) Delete(_ context.Context, ref compute.Reference) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletes++
	if r.deleteErr != nil {
		return r.deleteErr
	}
	delete(r.running, ref.ID)
	return nil
}
func (r *instanceTestRuntime) Inspect(_ context.Context, ref compute.Reference) (compute.Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return compute.Status{Running: r.running[ref.ID]}, nil
}
func (r *instanceTestRuntime) Exec(context.Context, compute.Reference, compute.ExecRequest) (compute.ExecSession, error) {
	return nil, errors.New("not used by reconciler")
}

func instanceTestWorld(t *testing.T) (context.Context, *store.Store, model.Subnet) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "instances.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	n := service.NewNetwork(s, nil, nil)
	vpc, err := n.CreateVPC(ctx, service.CreateVPCInput{Name: "vpc", CIDRBlock: "10.0.0.0/16"}, "")
	if err != nil {
		t.Fatal(err)
	}
	subnet, err := n.CreateSubnet(ctx, service.CreateSubnetInput{Name: "subnet", VPCID: vpc.ID, CIDRBlock: "10.0.1.0/24", AvailabilityZone: "local-1a"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTopology(ctx, vpc, []model.Subnet{subnet}, nil); err != nil {
		t.Fatal(err)
	}
	return ctx, s, subnet
}
func testRunInstance(ctx context.Context, t *testing.T, s *store.Store, subnet model.Subnet, name, key string) model.Instance {
	t.Helper()
	i, err := service.NewInstances(s, nil, nil).Run(ctx, service.RunInstanceInput{Name: name, SubnetID: subnet.ID}, key)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestInstancePrerequisites(t *testing.T) {
	ctx, s, subnet := instanceTestWorld(t)
	i := testRunInstance(ctx, t, s, subnet, "first", "")
	network := &instanceTestNetwork{}
	runtime := &instanceTestRuntime{}
	controller := NewInstances(s, network, runtime, time.Minute)
	if err := controller.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if runtime.creates != 0 || runtime.starts != 0 {
		t.Fatal("unready network started instance")
	}
	network.ready = true
	if err := controller.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInstance(ctx, "default", i.ID)
	if err != nil || got.State != model.InstanceRunning || got.ObservedGeneration != got.Generation || runtime.creates != 1 || runtime.starts != 1 {
		t.Fatalf("instance %+v runtime creates=%d starts=%d err=%v", got, runtime.creates, runtime.starts, err)
	}
	events, err := s.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		found = found || event.ResourceID == i.ID && event.State == string(model.InstanceRunning)
	}
	if !found {
		t.Fatal("running status missing from durable SSE events")
	}
}

func TestInstanceHookFailure(t *testing.T) {
	ctx, s, subnet := instanceTestWorld(t)
	i := testRunInstance(ctx, t, s, subnet, "first", "")
	runtime := &instanceTestRuntime{startErr: errors.New("createRuntime hook refused plumbing")}
	c := NewInstances(s, &instanceTestNetwork{ready: true}, runtime, time.Minute)
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInstance(ctx, "default", i.ID)
	if err != nil || got.State != model.InstanceFailed || got.ObservedGeneration != 0 || !strings.Contains(got.StateReason, "hook refused") || got.RuntimeID == "" {
		t.Fatalf("failed hook was hidden: %+v %v", got, err)
	}
	runtime.startErr = nil
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetInstance(ctx, "default", i.ID)
	if err != nil || got.State != model.InstanceRunning || got.ObservedGeneration != got.Generation {
		t.Fatalf("retry did not converge: %+v %v", got, err)
	}
}

func TestInstanceGenerationRace(t *testing.T) {
	ctx, s, subnet := instanceTestWorld(t)
	i := testRunInstance(ctx, t, s, subnet, "first", "")
	runtime := &instanceTestRuntime{deleteErr: errors.New("teardown temporarily failed")}
	runtime.createHook = func() {
		if err := service.NewInstances(s, nil, nil).Terminate(ctx, i.ID); err != nil {
			t.Error(err)
		}
	}
	c := NewInstances(s, &instanceTestNetwork{ready: true}, runtime, time.Minute)
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInstance(ctx, "default", i.ID)
	if err != nil || !got.DeletionRequested || runtime.starts != 0 || runtime.deletes != 1 || got.RuntimeID == "" || got.ENI.PrivateIP != i.ENI.PrivateIP {
		t.Fatalf("stale create started or freed lease: %+v starts=%d deletes=%d err=%v", got, runtime.starts, runtime.deletes, err)
	}
	runtime.deleteErr = nil
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetInstance(ctx, "default", i.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("retained runtime ID did not enable retry: %v", err)
	}
}

func TestInstanceGenerationRaceDuringStart(t *testing.T) {
	ctx, s, subnet := instanceTestWorld(t)
	i := testRunInstance(ctx, t, s, subnet, "first", "")
	runtime := &instanceTestRuntime{}
	runtime.startHook = func() {
		if err := service.NewInstances(s, nil, nil).Terminate(ctx, i.ID); err != nil {
			t.Error(err)
		}
	}
	c := NewInstances(s, &instanceTestNetwork{ready: true}, runtime, time.Minute)
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInstance(ctx, "default", i.ID)
	if err != nil || !got.DeletionRequested || got.State == model.InstanceRunning || got.RuntimeID == "" || got.ENI.PrivateIP != i.ENI.PrivateIP {
		t.Fatalf("start/terminate race falsely observed or freed lease: %+v %v", got, err)
	}
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetInstance(ctx, "default", i.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("raced runtime not torn down: %v", err)
	}
}

func TestInstanceTermination(t *testing.T) {
	ctx, s, subnet := instanceTestWorld(t)
	i := testRunInstance(ctx, t, s, subnet, "first", "")
	network := &instanceTestNetwork{ready: true}
	runtime := &instanceTestRuntime{}
	c := NewInstances(s, network, runtime, time.Minute)
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.NewInstances(s, nil, nil).Terminate(ctx, i.ID); err != nil {
		t.Fatal(err)
	}
	runtime.deleteErr = errors.New("runtime teardown failed")
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInstance(ctx, "default", i.ID)
	if err != nil || got.ENI.PrivateIP != i.ENI.PrivateIP || network.deletes != 0 {
		t.Fatalf("lease released before runtime delete: %+v %v", got, err)
	}
	runtime.deleteErr = nil
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetInstance(ctx, "default", i.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted instance remains: %v", err)
	}
	if network.deletes != 1 {
		t.Fatalf("ENI delete calls=%d", network.deletes)
	}
	next := testRunInstance(ctx, t, s, subnet, "second", "")
	if next.ENI.PrivateIP != i.ENI.PrivateIP {
		t.Fatalf("IP lease not reused after full teardown: old=%s new=%s", i.ENI.PrivateIP, next.ENI.PrivateIP)
	}
}

func TestInstanceResync(t *testing.T) {
	ctx, s, subnet := instanceTestWorld(t)
	const key = "same-invocation"
	var wg sync.WaitGroup
	ids := make(chan string, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			i, err := service.NewInstances(s, nil, nil).Run(ctx, service.RunInstanceInput{Name: "first", SubnetID: subnet.ID}, key)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- i.ID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first != "" && first != id {
			t.Fatalf("same key created two instances: %s %s", first, id)
		}
		first = id
	}
	runtime := &instanceTestRuntime{}
	c := NewInstances(s, &instanceTestNetwork{ready: true}, runtime, 10*time.Millisecond)
	for range 100 {
		c.Enqueue(first)
	}
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.Sweep(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if runtime.creates != 1 || runtime.starts != 1 {
		t.Fatalf("resync duplicated container: creates=%d starts=%d", runtime.creates, runtime.starts)
	}
	second := testRunInstance(ctx, t, s, subnet, "second", "") // no enqueue
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- c.Run(runCtx) }()
	deadline := time.Now().Add(time.Second)
	for {
		current, err := s.GetInstance(ctx, "default", second.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == model.InstanceRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("periodic resync did not find a committed instance without enqueue")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("controller did not stop on cancellation: %v", err)
	}
	if runtime.creates != 2 || runtime.starts != 2 {
		t.Fatalf("periodic resync calls creates=%d starts=%d", runtime.creates, runtime.starts)
	}
}
