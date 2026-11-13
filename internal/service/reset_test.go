package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/store"
)

// fakeVerifier records whether the workspace was empty when verification ran.
type fakeVerifier struct {
	store  *store.Store
	result model.LeakResult
	err    error
	calls  int
	empty  bool
}

func (v *fakeVerifier) Verify(ctx context.Context, workspace string) (model.LeakResult, error) {
	v.calls++
	state, err := v.store.DesiredSnapshot(ctx, workspace)
	if err != nil {
		return model.LeakResult{}, err
	}
	v.empty = len(state.Instances)+len(state.Subnets)+len(state.VPCs)+len(state.ENIs) == 0
	return v.result, v.err
}

func clean() model.LeakResult {
	return model.LeakResult{Complete: true, Clean: true, Findings: []model.LeakFinding{}, InspectionErrors: []string{}}
}

// runFakeReconciler stands in for both controllers: it physically removes
// rows marked for deletion, with the same store calls the reconcilers make.
func runFakeReconciler(ctx context.Context, s *store.Store) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			state, err := s.DesiredSnapshot(ctx, "default")
			if err == nil {
				for i := range state.Instances {
					if state.Instances[i].DeletionRequested {
						_ = s.FinishInstanceTermination(ctx, state.Instances[i])
					}
				}
				for i := range state.VPCs {
					if children, err := s.ListSubnetsByVPC(ctx, state.VPCs[i].ID); err == nil {
						_ = s.RecordTopology(ctx, state.VPCs[i], children, nil)
					}
				}
			}
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	return func() { cancel(); wg.Wait() }
}

func resetFixture(t *testing.T, verifier *fakeVerifier) (*Reset, *Network, *Instances, *store.Store) {
	t.Helper()
	network, s, _ := networkFixture(t)
	instances := NewInstances(s, nil, nil)
	guard := &ResetGuard{}
	network.GuardResets(guard)
	instances.GuardResets(guard)
	verifier.store = s
	r := NewReset(s, network, instances, guard, verifier)
	r.poll = time.Millisecond
	return r, network, instances, s
}

func populate(t *testing.T, network *Network, instances *Instances) {
	t.Helper()
	ctx := context.Background()
	for v := 0; v < 2; v++ {
		vpc, err := network.CreateVPC(ctx, CreateVPCInput{Name: fmt.Sprintf("vpc-%d", v), CIDRBlock: "10.0.0.0/16"}, "")
		if err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 2; n++ {
			subnet, err := network.CreateSubnet(ctx, CreateSubnetInput{Name: fmt.Sprintf("subnet-%d-%d", v, n), VPCID: vpc.ID,
				CIDRBlock: fmt.Sprintf("10.0.%d.0/24", n+1), AvailabilityZone: "local-1a"}, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := instances.Run(ctx, RunInstanceInput{Name: fmt.Sprintf("i-%d-%d", v, n), SubnetID: subnet.ID}, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestResetEmptiesWorkspaceInDependencyOrderThenVerifies(t *testing.T) {
	verifier := &fakeVerifier{result: clean()}
	r, network, instances, s := resetFixture(t, verifier)
	populate(t, network, instances)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stop := runFakeReconciler(ctx, s)
	defer stop()
	got, err := r.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Workspace != "default" || !got.LeakCheck.Clean || verifier.calls != 1 || !verifier.empty {
		t.Fatalf("result=%+v calls=%d emptyAtVerify=%v", got, verifier.calls, verifier.empty)
	}
	// Ordering: no subnet may be marked before every instance was gone, and
	// no VPC before every subnet, or the delete paths would have refused.
	var events []store.Event
	for after := int64(0); ; {
		page, err := s.EventsAfter(ctx, after, 100)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, page...)
		if len(page) < 100 {
			break
		}
		after = page[len(page)-1].ID
	}
	lastInstanceDeleted, firstSubnetDeleting, lastSubnetDeleted, firstVPCDeleting := int64(0), int64(-1), int64(0), int64(-1)
	for _, e := range events {
		switch {
		case e.ResourceType == "instance" && e.Action == "deleted":
			lastInstanceDeleted = e.ID
		case e.ResourceType == "subnet" && e.Action == "deleting" && firstSubnetDeleting < 0:
			firstSubnetDeleting = e.ID
		case e.ResourceType == "subnet" && e.Action == "deleted":
			lastSubnetDeleted = e.ID
		case e.ResourceType == "vpc" && e.Action == "deleting" && firstVPCDeleting < 0:
			firstVPCDeleting = e.ID
		}
	}
	if firstSubnetDeleting < lastInstanceDeleted || firstVPCDeleting < lastSubnetDeleted {
		t.Fatalf("out of order: lastInstanceDeleted=%d firstSubnetDeleting=%d lastSubnetDeleted=%d firstVPCDeleting=%d",
			lastInstanceDeleted, firstSubnetDeleting, lastSubnetDeleted, firstVPCDeleting)
	}
}

func TestResetOfEmptyWorkspaceIsIdempotent(t *testing.T) {
	verifier := &fakeVerifier{result: clean()}
	r, _, _, _ := resetFixture(t, verifier)
	for i := 0; i < 2; i++ {
		if _, err := r.Run(context.Background()); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if verifier.calls != 2 {
		t.Fatalf("verify calls=%d", verifier.calls)
	}
}

func TestResetGuardRefusesCreatesAndConcurrentReset(t *testing.T) {
	verifier := &fakeVerifier{result: clean()}
	r, network, instances, _ := resetFixture(t, verifier)
	if !r.guard.begin() {
		t.Fatal("guard unexpectedly held")
	}
	ctx := context.Background()
	_, vpcErr := network.CreateVPC(ctx, CreateVPCInput{Name: "late", CIDRBlock: "10.0.0.0/16"}, "")
	_, subnetErr := network.CreateSubnet(ctx, CreateSubnetInput{Name: "late", VPCID: "vpc-0", CIDRBlock: "10.0.1.0/24", AvailabilityZone: "local-1a"}, "")
	_, runErr := instances.Run(ctx, RunInstanceInput{Name: "late", SubnetID: "subnet-0"}, "")
	_, resetErr := r.Run(ctx)
	for name, err := range map[string]error{"vpc": vpcErr, "subnet": subnetErr, "instance": runErr, "reset": resetErr} {
		var domain *Error
		if !errors.As(err, &domain) || domain.Code != "ResetInProgress" || domain.Status != 409 {
			t.Fatalf("%s: got %v, want ResetInProgress", name, err)
		}
	}
	r.guard.end()
	if _, err := network.CreateVPC(ctx, CreateVPCInput{Name: "after", CIDRBlock: "10.0.0.0/16"}, ""); err != nil {
		t.Fatalf("create after reset: %v", err)
	}
}

func TestResetTimeoutReportsRemainingResourcesAndReleasesGuard(t *testing.T) {
	verifier := &fakeVerifier{result: clean()}
	r, network, instances, _ := resetFixture(t, verifier)
	populate(t, network, instances)
	// No reconciler runs, so nothing is ever physically removed.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := r.Run(ctx)
	var domain *Error
	if !errors.As(err, &domain) || domain.Code != "ResetTimeout" || domain.Status != 504 {
		t.Fatalf("got %v, want ResetTimeout", err)
	}
	if want := "4 instances, 4 subnets and 2 VPCs"; !strings.Contains(domain.Message, want) {
		t.Fatalf("message %q lacks %q", domain.Message, want)
	}
	if verifier.calls != 0 {
		t.Fatal("verified a workspace that never emptied")
	}
	if r.guard.check() != nil {
		t.Fatal("guard still held after timeout")
	}
}

func TestResetReportsLeaksAndIncompleteObservation(t *testing.T) {
	for name, result := range map[string]model.LeakResult{
		"residue":    {Complete: true, Findings: []model.LeakFinding{{Kind: "namespace", Identity: "nx-v1", Reason: "owned named namespace remains"}}},
		"incomplete": {Complete: false, InspectionErrors: []string{"runtime: socket closed"}},
	} {
		t.Run(name, func(t *testing.T) {
			r, _, _, _ := resetFixture(t, &fakeVerifier{result: result})
			_, err := r.Run(context.Background())
			var leak *LeakError
			if !errors.As(err, &leak) || leak.Result.Complete != result.Complete {
				t.Fatalf("got %v, want LeakError", err)
			}
		})
	}
}

func TestResetVerifierErrorIsNotReportedAsClean(t *testing.T) {
	r, _, _, _ := resetFixture(t, &fakeVerifier{result: clean(), err: errors.New("gate sealed")})
	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("verifier failure reported as success")
	}
}
