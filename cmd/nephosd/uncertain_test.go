package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/lifecycle"
	"github.com/Amrzxk/nephos/internal/model"
)

type uncertainStartRuntime struct{ compute.Runtime }

func (uncertainStartRuntime) Start(context.Context, compute.Reference) error {
	return compute.ErrUncertainEffect
}

type uncertainExecRuntime struct{ compute.Runtime }

func (uncertainExecRuntime) Exec(context.Context, compute.Reference, compute.ExecRequest) (compute.ExecSession, error) {
	return newShutdownSession(nil, compute.ErrUncertainEffect), nil
}

func TestEstablishedExecUncertaintyRetainsAdmission(t *testing.T) {
	ctx := context.Background()
	gate := lifecycle.NewGate()
	parent, _ := gate.Enter(ctx, "default")
	failure := make(chan error, 1)
	p := newOwnedRuntime(uncertainExecRuntime{}, gate, failure)
	session, err := p.Exec(lifecycle.WithLease(ctx, parent), compute.Reference{}, compute.ExecRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); !errors.Is(err, compute.ErrUncertainEffect) {
		t.Fatal(err)
	}
	_ = parent.Close()
	select {
	case err := <-failure:
		if !errors.Is(err, compute.ErrUncertainEffect) {
			t.Fatal(err)
		}
	default:
		t.Fatal("uncertain established exec cleanup did not fail daemon closed")
	}
	probe, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	ran := false
	err = gate.Exclusive(probe, "default", func(context.Context) error { ran = true; return nil })
	if ran || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost remove reply erased session ownership: ran=%v err=%v", ran, err)
	}
}

func TestUncertainRuntimeEffectRetainsAdmission(t *testing.T) {
	ctx := context.Background()
	gate := lifecycle.NewGate()
	parent, _ := gate.Enter(ctx, "default")
	failure := make(chan error, 1)
	p := newOwnedRuntime(uncertainStartRuntime{}, gate, failure)
	ref := compute.Reference{Identity: compute.Identity{WorkspaceID: "default", InstanceID: "i-00000000000000001"}, ID: compute.RuntimeID(strings.Repeat("a", 64))}
	if err := p.Start(lifecycle.WithLease(ctx, parent), ref); !errors.Is(err, compute.ErrUncertainEffect) {
		t.Fatal(err)
	}
	_ = parent.Close()
	select {
	case err := <-failure:
		if !errors.Is(err, compute.ErrUncertainEffect) {
			t.Fatal(err)
		}
	default:
		t.Fatal("uncertain mutation did not fail daemon closed")
	}
	// A result/error sent to the reconciler is not authoritative completion.
	probe, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	ran := false
	err := gate.Exclusive(probe, "default", func(context.Context) error { ran = true; return nil })
	if ran || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("uncertain effect erased before authoritative completion: ran=%v err=%v", ran, err)
	}
}

type inspectionRuntime struct {
	compute.Runtime
	err error
}

func (r inspectionRuntime) Lookup(context.Context, compute.Identity) (compute.Reference, error) {
	return compute.Reference{}, r.err
}
func (r inspectionRuntime) Inspect(context.Context, compute.Reference) (compute.Status, error) {
	return compute.Status{}, r.err
}
func (r inspectionRuntime) EnsureImage(context.Context, string) error       { return r.err }
func (r inspectionRuntime) Start(context.Context, compute.Reference) error  { return r.err }
func (r inspectionRuntime) Delete(context.Context, compute.Reference) error { return r.err }
func (r inspectionRuntime) Create(context.Context, model.Instance) (compute.CreateResult, error) {
	return compute.CreateResult{}, r.err
}
func (r inspectionRuntime) Exec(context.Context, compute.Reference, compute.ExecRequest) (compute.ExecSession, error) {
	return nil, r.err
}

func TestRequiredInspectionFailureClosesDaemonAdmission(t *testing.T) {
	for _, systemic := range []bool{true, false} {
		for _, method := range []string{"lookup", "inspect", "image", "start", "create", "delete", "exec"} {
			t.Run(method+"/systemic="+strconv.FormatBool(systemic), func(t *testing.T) {
				failure := make(chan error, 1)
				cause := errors.New("wrong owned resource identity")
				if systemic {
					cause = compute.ErrInspectionUnavailable
				}
				ctx := context.Background()
				gate := lifecycle.NewGate()
				parent, _ := gate.Enter(ctx, "default")
				defer parent.Close()
				ctx = lifecycle.WithLease(ctx, parent)
				p := newOwnedRuntime(inspectionRuntime{err: cause}, gate, failure)
				switch method {
				case "lookup":
					_, _ = p.Lookup(ctx, compute.Identity{})
				case "inspect":
					_, _ = p.Inspect(ctx, compute.Reference{})
				case "image":
					_ = p.EnsureImage(ctx, "test")
				case "start":
					_ = p.Start(ctx, compute.Reference{})
				case "create":
					_, _ = p.Create(ctx, model.Instance{})
				case "delete":
					_ = p.Delete(ctx, compute.Reference{})
				case "exec":
					_, _ = p.Exec(ctx, compute.Reference{}, compute.ExecRequest{})
				}
				select {
				case err := <-failure:
					if !systemic || !errors.Is(err, compute.ErrInspectionUnavailable) {
						t.Fatalf("individual resource error failed daemon: %v", err)
					}
				default:
					if systemic {
						t.Fatal("required runtime inspection failure could publish ready")
					}
				}
			})
		}
	}
}
