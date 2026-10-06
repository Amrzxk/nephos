package podman

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
)

func TestExecSupervisorIdentity(t *testing.T) {
	ref := testReference
	id := strings.Repeat("f", 64)
	args := []string{"/usr/bin/conmon", "-c", string(ref.ID), "-u", id, "-n", ref.Identity.InstanceID,
		"-b", "/var/lib/nephos/containers/overlay-containers/" + string(ref.ID) + "/userdata/" + id, "-e"}
	for _, kind := range []string{"exact", "main-instance", "another-exec", "another-container", "duplicate", "wrong-bundle", "exit-arg-spoof"} {
		t.Run(kind, func(t *testing.T) {
			candidate := append([]string(nil), args...)
			switch kind {
			case "main-instance":
				candidate = candidate[:len(candidate)-1]
			case "another-exec":
				candidate[4] = strings.Repeat("e", 64)
			case "another-container":
				candidate[2] = strings.Repeat("b", 64)
			case "duplicate":
				candidate = append(candidate, "-c", string(ref.ID))
			case "wrong-bundle":
				candidate[8] = "/tmp/foreign"
			case "exit-arg-spoof":
				candidate = append(candidate[:len(candidate)-1], "--exit-command-arg", "-e")
			}
			if got := matchesExecSupervisor(candidate, ref, id); got != (kind == "exact") {
				t.Fatalf("%s matched=%v", kind, got)
			}
			if got := execSupervisorCandidate(candidate, ref, id); got != (kind != "another-exec" && kind != "another-container") {
				t.Fatalf("ambiguous session candidate lost: %s observed=%v", kind, got)
			}
		})
	}
}

type supervisorFixture struct {
	key, parent                    string
	done, terminated, transitioned bool
	inspectionErr, signalErr       error
	order                          *[]string
}

func (s *supervisorFixture) keyID() string       { return s.key }
func (s *supervisorFixture) parentID() string    { return s.parent }
func (s *supervisorFixture) gone() (bool, error) { return s.done, s.inspectionErr }
func (s *supervisorFixture) terminate(context.Context) error {
	if s.transitioned {
		return errors.New("executable changed; refusing signal")
	}
	if s.signalErr != nil {
		return s.signalErr
	}
	s.terminated = true
	*s.order = append(*s.order, s.key)
	s.done = true
	return nil
}
func (*supervisorFixture) close() error { return nil }

func TestExecSupervisorsJoinChildBeforeParent(t *testing.T) {
	var order []string
	parent := &supervisorFixture{key: "parent", order: &order}
	child := &supervisorFixture{key: "child", parent: "parent", order: &order}
	passes := 0
	scan := func(context.Context, compute.Reference, string) ([]execSupervisor, error) {
		passes++
		if child.done {
			parent.done = true
			return nil, nil
		}
		if passes == 1 {
			return []execSupervisor{parent}, nil
		} // fork between scans
		return []execSupervisor{parent, child}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := joinExecSupervisors(ctx, testReference, "test", scan); err != nil {
		t.Fatal(err)
	}
	if parent.terminated || !reflect.DeepEqual(order, []string{"child"}) {
		t.Fatalf("unordered kill or missing child: %v", order)
	}
}

func TestExecSupervisorInspectionFailure(t *testing.T) {
	for _, kind := range []string{"denied", "pidfd", "executable-transition", "not-reaped"} {
		t.Run(kind, func(t *testing.T) {
			var order []string
			parent := &supervisorFixture{key: "parent", order: &order}
			child := &supervisorFixture{key: "child", parent: "parent", order: &order}
			scan := func(context.Context, compute.Reference, string) ([]execSupervisor, error) {
				if kind == "denied" {
					return nil, errors.New("denied inspection")
				}
				return []execSupervisor{parent, child}, nil
			}
			child.transitioned = kind == "executable-transition"
			if kind == "pidfd" {
				child.signalErr = errors.New("pidfd unavailable")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			if err := joinExecSupervisors(ctx, testReference, "test", scan); err == nil {
				t.Fatal("uncertain supervisor closure reported success")
			}
			if parent.terminated {
				t.Fatal("killed parent instead of waiting for reaping")
			}
		})
	}
}
