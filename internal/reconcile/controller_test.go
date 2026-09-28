package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
	"github.com/Amrzxk/nephos/internal/service"
	"github.com/Amrzxk/nephos/internal/store"
)

type fakeNetwork struct {
	mu                                                          sync.Mutex
	names                                                       map[string]model.VPC
	gateways                                                    map[string][]netip.Addr
	extra                                                       []string
	ensures, deletes, failEnsure, failDelete, active, maxActive int
	entered                                                     chan struct{}
	release                                                     chan struct{}
}

func newFakeNetwork() *fakeNetwork {
	return &fakeNetwork{names: map[string]model.VPC{}, gateways: map[string][]netip.Addr{}}
}

func (f *fakeNetwork) EnsureVPC(ctx context.Context, vpc model.VPC, subnets []model.Subnet) error {
	f.mu.Lock()
	f.ensures++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	if f.failEnsure > 0 {
		f.failEnsure--
		f.active--
		f.mu.Unlock()
		return errors.New("gateway add: operation not permitted")
	}
	entered, release := f.entered, f.release
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.active--; f.mu.Unlock() }()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	name, err := netns.Name(vpc.ShortIndex)
	if err != nil {
		return err
	}
	gateways := make([]netip.Addr, 0, len(subnets))
	for i := range subnets {
		subnet := &subnets[i]
		gateways = append(gateways, subnet.CIDRBlock.Addr().Next())
	}
	f.mu.Lock()
	f.names[name] = vpc
	f.gateways[name] = gateways
	f.mu.Unlock()
	return nil
}

func (f *fakeNetwork) DeleteVPC(_ context.Context, vpc model.VPC) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	if f.failDelete > 0 {
		f.failDelete--
		return errors.New("namespace busy")
	}
	name, err := netns.Name(vpc.ShortIndex)
	if err != nil {
		return err
	}
	delete(f.names, name)
	delete(f.gateways, name)
	return nil
}

func (f *fakeNetwork) ListVPCNames(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := slices.Clone(f.extra)
	for name := range f.names {
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

func setup(t *testing.T) (*store.Store, *service.Network, *fakeNetwork, *Controller) {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	f := newFakeNetwork()
	return s, service.NewNetwork(s, nil, nil), f, New(s, f, time.Hour)
}

func create(t *testing.T, svc *service.Network) (model.VPC, model.Subnet) {
	t.Helper()
	ctx := context.Background()
	vpc, err := svc.CreateVPC(ctx, service.CreateVPCInput{Name: "Lab East", CIDRBlock: "10.0.0.0/16"}, "")
	if err != nil {
		t.Fatal(err)
	}
	subnet, err := svc.CreateSubnet(ctx, service.CreateSubnetInput{Name: "App East", VPCID: vpc.ID, CIDRBlock: "10.0.1.0/24", AvailabilityZone: "local-1a"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return vpc, subnet
}

func TestSweepConvergesCommittedStateAndRepairsDrift(t *testing.T) {
	s, svc, f, c := setup(t)
	vpc, subnet := create(t, svc) // no enqueue callback
	ctx := context.Background()
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	gotVPC, err := s.GetVPC(ctx, "default", vpc.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotSubnet, err := s.GetSubnet(ctx, "default", subnet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotVPC.State != model.StateAvailable || gotVPC.ObservedGeneration != 1 || gotSubnet.State != model.StateAvailable || gotSubnet.ObservedGeneration != 1 {
		t.Fatalf("VPC=%+v subnet=%+v", gotVPC, gotSubnet)
	}
	name, _ := netns.Name(vpc.ShortIndex)
	f.mu.Lock()
	gateways := slices.Clone(f.gateways[name])
	delete(f.names, name)
	f.mu.Unlock()
	if len(gateways) != 1 || gateways[0].String() != "10.0.1.1" {
		t.Fatalf("gateways=%v", gateways)
	}
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	_, restored := f.names[name]
	ensures := f.ensures
	f.mu.Unlock()
	if !restored || ensures != 2 {
		t.Fatalf("restored=%v ensure calls=%d", restored, ensures)
	}
	events, err := s.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("idempotent resync emitted extra events: %v", events)
	}
}

func TestFailureRecordsReasonAndRetryRecovers(t *testing.T) {
	s, svc, f, c := setup(t)
	vpc, subnet := create(t, svc)
	f.failEnsure = 1
	ctx := context.Background()
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	v, _ := s.GetVPC(ctx, "default", vpc.ID)
	sn, _ := s.GetSubnet(ctx, "default", subnet.ID)
	if v.State != model.StateFailed || sn.State != model.StateFailed || v.ObservedGeneration != 0 || sn.ObservedGeneration != 0 || !strings.Contains(v.StateReason, "operation not permitted") {
		t.Fatalf("failure VPC=%+v subnet=%+v", v, sn)
	}
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	v, _ = s.GetVPC(ctx, "default", vpc.ID)
	sn, _ = s.GetSubnet(ctx, "default", subnet.ID)
	if v.State != model.StateAvailable || sn.State != model.StateAvailable || v.ObservedGeneration != 1 || sn.ObservedGeneration != 1 || v.StateReason != "" {
		t.Fatalf("recovery VPC=%+v subnet=%+v", v, sn)
	}
	events, err := s.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var failed, recovered bool
	for i, event := range events {
		if i > 0 && event.ID <= events[i-1].ID {
			t.Fatal("events out of order")
		}
		if event.ResourceID == vpc.ID && event.State == "failed" {
			failed = true
		}
		if event.ResourceID == vpc.ID && event.State == "available" {
			recovered = true
		}
	}
	if !failed || !recovered {
		t.Fatalf("missing failure/recovery events: %v", events)
	}
}

func TestSweepCollectsOnlyOwnedOrphans(t *testing.T) {
	_, svc, f, c := setup(t)
	vpc, _ := create(t, svc)
	f.extra = []string{"foreign", "nx-vpc-98765", "nx-vpc-01"}
	if err := c.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	name, _ := netns.Name(vpc.ShortIndex)
	f.mu.Lock()
	deletes := f.deletes
	_, wanted := f.names[name]
	f.mu.Unlock()
	if deletes != 1 || !wanted {
		t.Fatalf("deletes=%d desired preserved=%v", deletes, wanted)
	}
}

func TestDeleteTearsDownBeforeRemovingRows(t *testing.T) {
	s, svc, f, c := setup(t)
	vpc, subnet := create(t, svc)
	ctx := context.Background()
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteSubnet(ctx, subnet.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSubnet(ctx, "default", subnet.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("subnet not removed: %v", err)
	}
	name, _ := netns.Name(vpc.ShortIndex)
	f.mu.Lock()
	gateways := slices.Clone(f.gateways[name])
	f.mu.Unlock()
	if len(gateways) != 0 {
		t.Fatalf("stale gateways=%v", gateways)
	}
	if _, err := svc.DeleteVPC(ctx, vpc.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetVPC(ctx, "default", vpc.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("VPC not removed: %v", err)
	}
	f.mu.Lock()
	_, present := f.names[name]
	f.mu.Unlock()
	if present {
		t.Fatal("namespace survived VPC deletion")
	}
}

func TestFailedDeleteKeepsDurableIntent(t *testing.T) {
	s, svc, f, c := setup(t)
	vpc, subnet := create(t, svc)
	ctx := context.Background()
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteSubnet(ctx, subnet.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteVPC(ctx, vpc.ID); err != nil {
		t.Fatal(err)
	}
	f.failDelete = 1
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := s.GetVPC(ctx, "default", vpc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.State != model.StateFailed || !v.DeletionRequested || v.ObservedGeneration >= v.Generation || !strings.Contains(v.StateReason, "namespace busy") {
		t.Fatalf("lost deletion intent: %+v", v)
	}
	_, err = svc.CreateSubnet(ctx, service.CreateSubnetInput{
		Name: "Late", VPCID: vpc.ID, CIDRBlock: "10.0.2.0/24", AvailabilityZone: "local-1a",
	}, "")
	var domainErr *service.Error
	if !errors.As(err, &domainErr) || domainErr.Code != "DependencyViolation" {
		t.Fatalf("failed VPC teardown accepted a new subnet or returned wrong error: %v", err)
	}
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetVPC(ctx, "default", vpc.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("retry did not delete: %v", err)
	}
	f.mu.Lock()
	ensures := f.ensures
	f.mu.Unlock()
	if ensures != 2 {
		t.Fatalf("delete retry recreated namespace: ensures=%d", ensures)
	}
}

func TestQueuedReconcileDoesNotOverlap(t *testing.T) {
	s, svc, f, c := setup(t)
	vpc, _ := create(t, svc)
	f.entered = make(chan struct{}, 1)
	f.release = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	c.Enqueue(vpc.ID)
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	for i := 0; i < 100; i++ {
		c.Enqueue(vpc.ID)
	}
	close(f.release)
	deadline := time.After(time.Second)
	for {
		v, err := s.GetVPC(context.Background(), "default", vpc.ID)
		if err == nil && v.State == model.StateAvailable {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("worker did not converge: %v %v", v, err)
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	f.mu.Lock()
	maxActive := f.maxActive
	f.mu.Unlock()
	if maxActive != 1 {
		t.Fatalf("same VPC had %d overlapping reconciles", maxActive)
	}
}

func TestRunRetriesFailedVPCWithoutAnotherEnqueue(t *testing.T) {
	s, svc, f, c := setup(t)
	vpc, _ := create(t, svc)
	f.failEnsure = 1
	ctx := context.Background()
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	failed, err := s.GetVPC(ctx, "default", vpc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != model.StateFailed {
		t.Fatalf("first attempt=%+v", failed)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(runCtx) }()
	deadline := time.After(2 * time.Second)
	for {
		current, err := s.GetVPC(ctx, "default", vpc.ID)
		if err == nil && current.State == model.StateAvailable {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("backoff retry never recovered: %v %v", current, err)
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("retry worker did not stop")
	}
}

func TestConcurrentDesiredChangeDoesNotPublishStaleObservation(t *testing.T) {
	s, svc, f, c := setup(t)
	_, subnet := create(t, svc)
	f.entered = make(chan struct{}, 1)
	f.release = make(chan struct{})
	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- c.Sweep(ctx) }()
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("engine did not start")
	}
	if _, err := svc.DeleteSubnet(ctx, subnet.ID); err != nil {
		t.Fatal(err)
	}
	close(f.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("sweep did not finish")
	}
	current, err := s.GetSubnet(ctx, "default", subnet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != model.StateDeleting || current.ObservedGeneration != 0 || !current.DeletionRequested {
		t.Fatalf("stale engine result advanced subnet: %+v", current)
	}
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSubnet(ctx, "default", subnet.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("subnet did not converge after race: %v", err)
	}
}

func TestPeriodicResyncRepairsDriftWithoutEnqueue(t *testing.T) {
	_, svc, f, c := setup(t)
	vpc, _ := create(t, svc)
	ctx := context.Background()
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	name, _ := netns.Name(vpc.ShortIndex)
	f.mu.Lock()
	delete(f.names, name)
	f.mu.Unlock()
	c.interval = 10 * time.Millisecond
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- c.Run(runCtx) }()
	deadline := time.After(time.Second)
	for {
		f.mu.Lock()
		_, restored := f.names[name]
		f.mu.Unlock()
		if restored {
			break
		}
		select {
		case <-deadline:
			t.Fatal("periodic resync did not restore namespace")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("resync worker did not stop")
	}
}
