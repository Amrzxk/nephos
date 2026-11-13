package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/store"
)

func serviceCode(t *testing.T, err error) string {
	t.Helper()
	var domain *Error
	if !errors.As(err, &domain) {
		t.Fatalf("not a service error: %v", err)
	}
	return domain.Code
}

func TestRunInstanceTypes(t *testing.T) {
	svc, _, _, _, subnet := instanceFixture(t, "10.0.1.0/24")
	ctx := context.Background()
	for _, tt := range []struct{ requested, stored string }{
		{"", "t3.micro"}, {"t3.nano", "t3.nano"}, {"t3.small", "t3.small"}, {"t3.medium", "t3.medium"},
	} {
		got, err := svc.Run(ctx, RunInstanceInput{Name: "i-" + tt.stored + tt.requested, SubnetID: subnet.ID, InstanceType: tt.requested}, "")
		if err != nil {
			t.Fatalf("%q: %v", tt.requested, err)
		}
		read, err := svc.Get(ctx, got.ID)
		if err != nil || got.InstanceType != tt.stored || read.InstanceType != tt.stored {
			t.Fatalf("%q: run=%s get=%s err=%v", tt.requested, got.InstanceType, read.InstanceType, err)
		}
	}
	_, err := svc.Run(ctx, RunInstanceInput{Name: "big", SubnetID: subnet.ID, InstanceType: "t3.large"}, "")
	if code := serviceCode(t, err); code != "InvalidParameterValue" {
		t.Fatalf("unsupported type: %s", code)
	}
}

func TestRunInstanceIdempotencyIncludesType(t *testing.T) {
	svc, _, _, _, subnet := instanceFixture(t, "10.0.1.0/24")
	ctx := context.Background()
	first, err := svc.Run(ctx, RunInstanceInput{Name: "web", SubnetID: subnet.ID, InstanceType: "t3.small"}, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.Run(ctx, RunInstanceInput{Name: "web", SubnetID: subnet.ID, InstanceType: "t3.small"}, "key-1")
	if err != nil || again.ID != first.ID {
		t.Fatalf("replay: %+v %v", again, err)
	}
	_, err = svc.Run(ctx, RunInstanceInput{Name: "web", SubnetID: subnet.ID, InstanceType: "t3.nano"}, "key-1")
	if code := serviceCode(t, err); code != "IdempotentParameterMismatch" {
		t.Fatalf("changed type replayed: %s", code)
	}
	// Omitting the type and naming the default are the same request, and both
	// keep the pre-M2 hash so replays recorded before the upgrade still match.
	omitted, err := svc.Run(ctx, RunInstanceInput{Name: "db", SubnetID: subnet.ID}, "key-2")
	if err != nil {
		t.Fatal(err)
	}
	named, err := svc.Run(ctx, RunInstanceInput{Name: "db", SubnetID: subnet.ID, InstanceType: model.DefaultInstanceType}, "key-2")
	if err != nil || named.ID != omitted.ID {
		t.Fatalf("explicit default did not replay: %+v %v", named, err)
	}
	legacy, err := hashPayload("db", subnet.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = svc.store.WithTx(ctx, func(tx *store.Tx) error {
		_, found, err := loadReplay[model.Instance](ctx, tx, "run-instance", "key-2", legacy, time.Now())
		if err == nil && !found {
			err = errors.New("replay not found")
		}
		return err
	})
	if err != nil {
		t.Fatalf("a default-type replay does not match the pre-M2 hash: %v", err)
	}
}

func TestRunInstanceCapacityAndQuota(t *testing.T) {
	tests := []struct {
		name   string
		limits Capacity
		runs   []string
		code   string // error expected on the last run
	}{
		// 2 GiB - 512 MiB reserve = 1.5 GiB: one micro (1 GiB) + one nano (512 MiB) fit exactly.
		{name: "exact fit", limits: Capacity{MemoryBytes: 2 << 30, ReserveBytes: 512 << 20}, runs: []string{"t3.micro", "t3.nano"}},
		{name: "over memory", limits: Capacity{MemoryBytes: 2 << 30, ReserveBytes: 512 << 20}, runs: []string{"t3.micro", "t3.micro"}, code: "InsufficientInstanceCapacity"},
		{name: "single too big", limits: Capacity{MemoryBytes: 2 << 30, ReserveBytes: 512 << 20}, runs: []string{"t3.medium"}, code: "InsufficientInstanceCapacity"},
		{name: "quota", limits: Capacity{MaxInstances: 2}, runs: []string{"t3.nano", "t3.nano", "t3.nano"}, code: "InstanceLimitExceeded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _, _, subnet := instanceFixture(t, "10.0.1.0/24")
			svc.LimitCapacity(tt.limits)
			var err error
			for i, instanceType := range tt.runs {
				_, err = svc.Run(context.Background(), RunInstanceInput{Name: fmt.Sprintf("i%d", i), SubnetID: subnet.ID, InstanceType: instanceType}, "")
				if err != nil && i != len(tt.runs)-1 {
					t.Fatalf("run %d: %v", i, err)
				}
			}
			if tt.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if code := serviceCode(t, err); code != tt.code {
				t.Fatalf("code=%s want %s (%v)", code, tt.code, err)
			}
		})
	}
}

func TestTerminatingInstanceStillHoldsCapacityUntilRemoved(t *testing.T) {
	svc, _, _, _, subnet := instanceFixture(t, "10.0.1.0/24")
	svc.LimitCapacity(Capacity{MemoryBytes: 1<<30 + 512<<20, ReserveBytes: 512 << 20})
	ctx := context.Background()
	first, err := svc.Run(ctx, RunInstanceInput{Name: "a", SubnetID: subnet.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Terminate(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Run(ctx, RunInstanceInput{Name: "b", SubnetID: subnet.ID}, "")
	if code := serviceCode(t, err); code != "InsufficientInstanceCapacity" {
		t.Fatalf("shutting-down container's memory was handed out: %s", code)
	}
}

func TestConcurrentRunsNeverOvercommit(t *testing.T) {
	svc, _, _, _, subnet := instanceFixture(t, "10.0.1.0/24")
	// Room for exactly three micros.
	svc.LimitCapacity(Capacity{MemoryBytes: 3<<30 + 512<<20, ReserveBytes: 512 << 20})
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, refused := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := svc.Run(context.Background(), RunInstanceInput{Name: fmt.Sprintf("c%d", i), SubnetID: subnet.ID}, "")
			mu.Lock()
			defer mu.Unlock()
			var domain *Error
			switch {
			case err == nil:
				ok++
			case errors.As(err, &domain) && domain.Code == "InsufficientInstanceCapacity":
				refused++
			default:
				t.Errorf("run %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if ok != 3 || refused != 5 {
		t.Fatalf("ok=%d refused=%d, want 3 and 5", ok, refused)
	}
}
