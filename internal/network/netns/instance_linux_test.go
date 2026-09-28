package netns

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestInstanceTarget(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		pid int
		id  string
	}{
		{0, strings.Repeat("a", 64)}, {-1, strings.Repeat("a", 64)}, {os.Getpid(), ""},
		{os.Getpid(), "../foreign"}, {os.Getpid(), strings.Repeat("a", 64)},
		{2147483647, strings.Repeat("a", 64)},
	} {
		target, err := OpenInstance(ctx, tc.pid, tc.id)
		if err == nil {
			target.Close()
			t.Errorf("accepted pid=%d id=%q", tc.pid, tc.id)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := OpenInstance(ctx, os.Getpid(), strings.Repeat("a", 64)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled open=%v", err)
	}
}

func TestInstanceTargetCgroupAssociation(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{"0::/libpod_parent/libpod-" + id + "\n", true},
		{"0::/machine.slice/libpod-" + id + ".scope\n", true},
		{"0::/libpod_parent/libpod-" + id + "suffix\n", false},
		{"0::/libpod_parent/libpod-" + strings.Repeat("b", 64) + "\n", false},
		{"0::/init\n", false}, {"1:cpu:/libpod-" + id + "\n", false},
		{"0::/libpod-" + id + "\n0::/foreign\n", false}, {"0::relative/libpod-" + id + "\n", false},
		{"garbage", false},
	} {
		if got := runtimeCgroupMatches([]byte(tc.raw), id); got != tc.valid {
			t.Errorf("cgroup %q: got %v want %v", tc.raw, got, tc.valid)
		}
	}
}

func TestInstanceTargetMappedRoot(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{"0 200000 1024\n", true}, {"0 200000 65536\n", true},
		{"0 0 4294967295\n", false}, {"1 200000 1024\n", false},
		{"0 200000 0\n", false}, {"0 200000 -1\n", false},
		{"garbage", false}, {"0 200000 65536 extra\n", false},
	} {
		if err := validateRootMapping([]byte(tc.raw)); (err == nil) != tc.valid {
			t.Errorf("map %q: err=%v", tc.raw, err)
		}
	}
}

func TestWithInstanceHandle(t *testing.T) {
	called := false
	fn := func(*netlink.Handle) error { called = true; return nil }
	if err := WithInstanceHandle(context.Background(), nil, fn); err == nil {
		t.Fatal("nil target accepted")
	}
	target := &InstanceTarget{}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	if err := WithInstanceHandle(context.Background(), target, fn); err == nil {
		t.Fatal("closed target accepted")
	}
	if called {
		t.Fatal("invalid target called netlink callback")
	}
}
