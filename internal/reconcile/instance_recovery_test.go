package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/service"
	"github.com/Amrzxk/nephos/internal/store"
)

type retainedRoot struct {
	ref     compute.Reference
	marker  string
	running bool
}

// recoveryRuntime keeps an independent marker witness, never a store cache.
type recoveryRuntime struct {
	roots           map[compute.RuntimeID]*retainedRoot
	creates, starts int
	deleted         []compute.Reference
	lookupHook      func()
	lookupErr       error
}

func (r *recoveryRuntime) EnsureImage(context.Context, string) error { return nil }
func (r *recoveryRuntime) Lookup(_ context.Context, identity compute.Identity) (compute.Reference, error) {
	if r.lookupErr != nil {
		return compute.Reference{}, r.lookupErr
	}
	for _, root := range r.roots {
		if root.ref.Identity == identity {
			ref := root.ref
			if hook := r.lookupHook; hook != nil {
				r.lookupHook = nil
				hook()
			}
			return ref, nil
		}
	}
	return compute.Reference{}, compute.ErrNotFound
}
func (r *recoveryRuntime) Create(ctx context.Context, instance model.Instance) (compute.CreateResult, error) {
	identity := compute.Identity{WorkspaceID: instance.WorkspaceID, InstanceID: instance.ID}
	if ref, err := r.Lookup(ctx, identity); err == nil {
		return compute.CreateResult{Reference: ref}, nil
	} else if !errors.Is(err, compute.ErrNotFound) {
		return compute.CreateResult{}, err
	}
	r.creates++
	ref := compute.Reference{Identity: identity, ID: compute.RuntimeID(fmt.Sprintf("%064x", 100+r.creates))}
	r.roots[ref.ID] = &retainedRoot{ref: ref}
	return compute.CreateResult{Reference: ref, Created: true}, nil
}
func (r *recoveryRuntime) root(ref compute.Reference) (*retainedRoot, error) {
	root, ok := r.roots[ref.ID]
	if !ok {
		return nil, compute.ErrNotFound
	}
	if root.ref != ref {
		return nil, errors.New("wrong expected runtime identity")
	}
	return root, nil
}
func (r *recoveryRuntime) Start(_ context.Context, ref compute.Reference) error {
	root, err := r.root(ref)
	if err != nil {
		return err
	}
	r.starts++
	root.running = true
	return nil
}
func (r *recoveryRuntime) Inspect(_ context.Context, ref compute.Reference) (compute.Status, error) {
	root, err := r.root(ref)
	if err != nil {
		return compute.Status{}, err
	}
	return compute.Status{Running: root.running, PID: 1234}, nil
}
func (r *recoveryRuntime) Delete(_ context.Context, ref compute.Reference) error {
	if _, err := r.root(ref); err != nil {
		return err
	}
	r.deleted = append(r.deleted, ref)
	delete(r.roots, ref.ID)
	return nil
}
func (r *recoveryRuntime) Exec(context.Context, compute.Reference, compute.ExecRequest) (compute.ExecSession, error) {
	return nil, errors.New("unused recovery console")
}
func retainedRuntime(instance model.Instance) *recoveryRuntime {
	ref := compute.Reference{Identity: compute.Identity{WorkspaceID: instance.WorkspaceID, InstanceID: instance.ID}, ID: compute.RuntimeID(strings.Repeat("a", 64))}
	return &recoveryRuntime{roots: map[compute.RuntimeID]*retainedRoot{ref.ID: {ref: ref, marker: "preserve-my-file", running: true}}}
}

type repairNetwork struct {
	instanceTestNetwork
	repairs   int
	repairErr error
}

func (n *repairNetwork) EnsureRunning(_ context.Context, i model.Instance, ref compute.Reference, status compute.Status) error {
	if ref.Identity != (compute.Identity{WorkspaceID: i.WorkspaceID, InstanceID: i.ID}) || !status.Running || status.PID != 1234 {
		return errors.New("unverified repair")
	}
	n.repairs++
	return n.repairErr
}

func TestRediscoveredRootSurvivesStaleRuntimeID(t *testing.T) {
	for _, cache := range []string{"", strings.Repeat("b", 64)} {
		t.Run(map[bool]string{true: "empty", false: "another managed instance"}[cache == ""], func(t *testing.T) {
			ctx, s, subnet := instanceTestWorld(t)
			i := testRunInstance(ctx, t, s, subnet, "retained", "")
			r := retainedRuntime(i)
			other := compute.Reference{Identity: compute.Identity{WorkspaceID: "default", InstanceID: "i-00000000000000002"}, ID: compute.RuntimeID(strings.Repeat("b", 64))}
			r.roots[other.ID] = &retainedRoot{ref: other, marker: "other-file", running: true}
			if cache != "" {
				if err := s.RecordRuntimeID(ctx, i.ID, i.Generation, cache); err != nil {
					t.Fatal(err)
				}
			}
			n := &repairNetwork{instanceTestNetwork: instanceTestNetwork{ready: true}}
			c := NewInstances(s, n, r, time.Minute)
			if err := c.Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := s.GetInstance(ctx, "default", i.ID)
			if err != nil || got.State != model.InstanceRunning || got.RuntimeID != strings.Repeat("a", 64) || r.creates != 0 || len(r.deleted) != 0 {
				t.Fatalf("retained recovery: %+v creates=%d deletes=%+v err=%v", got, r.creates, r.deleted, err)
			}
			if r.roots[compute.RuntimeID(got.RuntimeID)].marker != "preserve-my-file" || r.roots[other.ID].marker != "other-file" {
				t.Fatal("a retained marker was lost")
			}
		})
	}
}

func TestRuntimeRebindTerminateRace(t *testing.T) {
	ctx, s, subnet := instanceTestWorld(t)
	i := testRunInstance(ctx, t, s, subnet, "retained", "")
	if err := s.RecordRuntimeID(ctx, i.ID, i.Generation, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	r := retainedRuntime(i)
	r.lookupHook = func() {
		if err := service.NewInstances(s, nil, nil).Terminate(ctx, i.ID); err != nil {
			t.Error(err)
		}
	}
	c := NewInstances(s, &repairNetwork{instanceTestNetwork: instanceTestNetwork{ready: true}}, r, time.Minute)
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if r.starts != 0 || len(r.deleted) != 0 || len(r.roots) != 1 {
		t.Fatal("failed live rebind destroyed retained root or started terminating intent")
	}
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetInstance(ctx, "default", i.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("termination did not finish: %v", err)
	}
	if len(r.deleted) != 1 || r.deleted[0].ID != compute.RuntimeID(strings.Repeat("a", 64)) {
		t.Fatalf("deleted wrong identity: %+v", r.deleted)
	}
}

func observedInstance(ctx context.Context, t *testing.T, s *store.Store, i model.Instance) model.Instance {
	t.Helper()
	if err := s.RecordRuntimeID(ctx, i.ID, i.Generation, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	i, err := s.GetInstance(ctx, "default", i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInstance(ctx, i, model.InstanceRunning, "", true); err != nil {
		t.Fatal(err)
	}
	i, err = s.GetInstance(ctx, "default", i.ID)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestProvisionedRootMissingFails(t *testing.T) {
	ctx, s, subnet := instanceTestWorld(t)
	i := observedInstance(ctx, t, s, testRunInstance(ctx, t, s, subnet, "missing", ""))
	if err := s.RecordInstance(ctx, i, model.InstanceFailed, "restart", false); err != nil {
		t.Fatal(err)
	}
	r := &recoveryRuntime{roots: make(map[compute.RuntimeID]*retainedRoot)}
	c := NewInstances(s, &repairNetwork{instanceTestNetwork: instanceTestNetwork{ready: true}}, r, time.Minute)
	for range 2 {
		if err := c.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetInstance(ctx, "default", i.ID)
	if err != nil || got.State != model.InstanceFailed || !strings.Contains(got.StateReason, "retained root") || r.creates != 0 || r.starts != 0 || len(r.deleted) != 0 {
		t.Fatalf("missing root replaced or hidden: %+v creates=%d starts=%d deletes=%+v err=%v", got, r.creates, r.starts, r.deleted, err)
	}
}

func TestRunningInstanceRepairsENI(t *testing.T) {
	for _, repairErr := range []error{nil, errors.New("ENI repair failed")} {
		t.Run(fmt.Sprint(repairErr), func(t *testing.T) {
			ctx, s, subnet := instanceTestWorld(t)
			i := observedInstance(ctx, t, s, testRunInstance(ctx, t, s, subnet, "running", ""))
			r := retainedRuntime(i)
			n := &repairNetwork{instanceTestNetwork: instanceTestNetwork{ready: true}, repairErr: repairErr}
			if err := NewInstances(s, n, r, time.Minute).Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := s.GetInstance(ctx, "default", i.ID)
			wantState := model.InstanceRunning
			if repairErr != nil {
				wantState = model.InstanceFailed
			}
			if err != nil || got.State != wantState || n.repairs != 1 || r.starts != 0 || r.creates != 0 || len(r.deleted) != 0 {
				t.Fatalf("running drift bypassed verification: %+v repairs=%d starts=%d err=%v", got, n.repairs, r.starts, err)
			}
		})
	}
}

func TestPreRunningMissingRootRetries(t *testing.T) {
	ctx, s, subnet := instanceTestWorld(t)
	i := testRunInstance(ctx, t, s, subnet, "pre-running", "")
	if err := s.RecordRuntimeID(ctx, i.ID, i.Generation, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	r := &recoveryRuntime{roots: make(map[compute.RuntimeID]*retainedRoot)}
	if err := NewInstances(s, &repairNetwork{instanceTestNetwork: instanceTestNetwork{ready: true}}, r, time.Minute).Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInstance(ctx, "default", i.ID)
	if err != nil || got.State != model.InstanceRunning || r.creates != 1 || len(r.deleted) != 0 {
		t.Fatalf("pre-running retry: %+v creates=%d deletes=%+v err=%v", got, r.creates, r.deleted, err)
	}
}
