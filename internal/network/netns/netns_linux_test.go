package netns

import (
	"context"
	"errors"
	"testing"
)

func TestNameUsesDatabaseIndexAndFitsKernelNameBudget(t *testing.T) {
	tests := []struct {
		index int64
		want  string
	}{
		{1, "nx-vpc-1"},
		{42, "nx-vpc-42"},
		{99999999, "nx-vpc-99999999"},
	}
	for _, tt := range tests {
		got, err := Name(tt.index)
		if err != nil || got != tt.want || len(got) > 15 {
			t.Fatalf("Name(%d)=%q, %v; want %q with <=15 bytes", tt.index, got, err, tt.want)
		}
	}
	for _, index := range []int64{-1, 0, 100000000} {
		if _, err := Name(index); err == nil {
			t.Fatalf("Name(%d) unexpectedly succeeded", index)
		}
	}
}

func TestNamesRejectTraversalAndForeignNamespaces(t *testing.T) {
	for _, name := range []string{
		"", "../nx-vpc-1", "nx-vpc-1/../../host", "nx-edge",
		"nx-vpc-0", "nx-vpc-01", "nx-vpc-abc", "nx-vpc-100000000",
		"nx-vpc-1\n", "nx-nat-1",
	} {
		if err := validateName(name); err == nil {
			t.Errorf("validateName(%q) unexpectedly succeeded", name)
		}
	}
	if err := validateName("nx-vpc-123"); err != nil {
		t.Fatalf("valid name rejected: %v", err)
	}
}

func TestCanceledNamespaceOperationsStopBeforeTouchingKernel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, run := range map[string]func() error{
		"ensure": func() error { return Ensure(ctx, "nx-vpc-1") },
		"do":     func() error { return Do(ctx, "nx-vpc-1", func() error { t.Fatal("callback ran"); return nil }) },
		"delete": func() error { return Delete(ctx, "nx-vpc-1") },
	} {
		if err := run(); !errors.Is(err, context.Canceled) {
			t.Errorf("%s returned %v, want context.Canceled", name, err)
		}
	}
}
