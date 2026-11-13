//go:build linux

package podman

import (
	"os"
	"testing"

	"github.com/Amrzxk/nephos/internal/compute"
)

func TestSupervisorExitsDuringPinningRetainsReapingWitness(t *testing.T) {
	for _, kind := range []string{"after-identity", "before-identity", "missing-exe-not-stat", "fully-reaped"} {
		t.Run(kind, func(t *testing.T) {
			polls, closed := 0, 0
			probe := supervisorProbe{
				open:   func(int) (int, error) { return 99, nil },
				close:  func(int) error { closed++; return nil },
				exited: func(int) (bool, error) { polls++; return kind == "before-identity" || polls > 1, nil },
				stat: func(pid int) (string, string, int, error) {
					if kind == "fully-reaped" && pid == 42 {
						return "", "", 0, os.ErrNotExist
					}
					return "Z", "123", 1, nil
				},
				identity: func(int, compute.Reference, string) (bool, error) {
					if kind == "missing-exe-not-stat" {
						return false, os.ErrNotExist
					}
					return true, nil
				},
			}
			pin, err := pinExecSupervisorUsing(42, testReference, "exec", probe)
			switch kind {
			case "after-identity":
				if err != nil || pin == nil || closed != 0 {
					t.Fatalf("exited identity-bound zombie discarded: pin=%v err=%v closes=%d", pin, err, closed)
				}
			case "fully-reaped":
				if err != nil || pin != nil || closed != 1 {
					t.Fatalf("reaped process: pin=%v err=%v closes=%d", pin, err, closed)
				}
			default:
				if err == nil || pin != nil || closed != 1 {
					t.Fatalf("unbound zombie treated as absence: pin=%v err=%v closes=%d", pin, err, closed)
				}
			}
		})
	}
}
