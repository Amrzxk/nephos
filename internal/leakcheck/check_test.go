package leakcheck

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/lifecycle"
	"github.com/Amrzxk/nephos/internal/network/topology"
	"github.com/Amrzxk/nephos/internal/store"
)

type runtimeInventory struct {
	items []compute.ObservedContainer
	err   error
}

func (r runtimeInventory) Inventory(context.Context) ([]compute.ObservedContainer, error) {
	return r.items, r.err
}

type networkInventory struct {
	items topology.Inventory
	err   error
}

func (n networkInventory) Inventory(context.Context) (topology.Inventory, error) {
	return n.items, n.err
}

type processInventory struct {
	findings []Finding
	err      error
}

func (p processInventory) Observe(context.Context, []lifecycle.Reference) ([]Finding, error) {
	return p.findings, p.err
}

func testChecker(t *testing.T) *Checker {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &Checker{Store: s, Runtime: runtimeInventory{}, Network: networkInventory{}, Registry: lifecycle.NewRegistry(), Gate: lifecycle.NewGate(), Processes: processInventory{}}
}

func TestLeakCheckRequiresCompleteInspection(t *testing.T) {
	for _, fault := range []string{"runtime", "nft/netlink", "proc", "store", "missing adapter"} {
		t.Run(fault, func(t *testing.T) {
			checker := testChecker(t)
			denied := errors.New("required inspection denied")
			switch fault {
			case "runtime":
				checker.Runtime = runtimeInventory{err: denied}
			case "nft/netlink":
				checker.Network = networkInventory{err: denied}
			case "proc":
				checker.Processes = processInventory{err: denied}
			case "store":
				if err := checker.Store.(*store.Store).Close(); err != nil {
					t.Fatal(err)
				}
			case "missing adapter":
				checker.Processes = nil
			}
			err := checker.Gate.Exclusive(context.Background(), "default", func(ctx context.Context) error {
				result, err := checker.Check(ctx, "default")
				if err == nil || result.Complete || result.Clean || len(result.InspectionErrors) == 0 {
					t.Fatalf("failed inspection became clean: %+v err=%v", result, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOutstandingEffectBlocksClean(t *testing.T) {
	checker := testChecker(t)
	lease, err := checker.Gate.Enter(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	result, err := checker.Check(context.Background(), "default")
	if err != nil || !result.Complete || result.Clean || len(result.Findings) == 0 {
		t.Fatalf("live effect became clean: %+v %v", result, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	release := checker.Registry.Track("default", "netlink-socket", "namespace:1234", nil)
	if err := checker.Gate.Exclusive(context.Background(), "default", func(ctx context.Context) error {
		result, err := checker.Check(ctx, "default")
		if err != nil || result.Clean || len(result.Findings) == 0 {
			t.Fatalf("socket became clean before actual close: %+v %v", result, err)
		}
		release()
		result, err = checker.Check(ctx, "default")
		if err != nil || !result.Complete || !result.Clean || len(result.Findings) != 0 {
			t.Fatalf("closed drained world did not become clean: %+v %v", result, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUnlinkedNamespaceReferenceBlocksClean(t *testing.T) {
	checker := testChecker(t)
	checker.Processes = processInventory{findings: []Finding{{Kind: "namespace-fd", Identity: "/proc/42/fd/7", Reason: "known namespace inode 1234 survives unlink"}}}
	if err := checker.Gate.Exclusive(context.Background(), "default", func(ctx context.Context) error {
		result, err := checker.Check(ctx, "default")
		if err != nil || !result.Complete || result.Clean || len(result.Findings) != 1 || result.Findings[0].Kind != "namespace-fd" {
			t.Fatalf("unlinked reference escaped: %+v %v", result, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticObserverCannotBorrowAnotherCallersExclusiveBarrier(t *testing.T) {
	checker := testChecker(t)
	if err := checker.Gate.Exclusive(context.Background(), "default", func(ctx context.Context) error {
		result, err := checker.Check(context.Background(), "default")
		if err != nil || result.Clean {
			t.Fatalf("unadmitted observer borrowed exclusivity: %+v %v", result, err)
		}
		result, err = checker.Check(ctx, "default")
		if err != nil || !result.Clean {
			t.Fatalf("actual exclusive caller refused: %+v %v", result, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
