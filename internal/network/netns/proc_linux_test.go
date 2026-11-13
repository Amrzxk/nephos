package netns

import (
	"context"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestKnownNamespaceObservationFindsTaskAndDuplicateFD(t *testing.T) {
	f, err := os.Open("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		t.Fatal(err)
	}
	refs, err := observeKnown(context.Background(), "/proc", []uint64{stat.Ino}, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	var task, fd bool
	for _, ref := range refs {
		task = task || ref.Kind == "task"
		fd = fd || ref.Kind == "namespace-fd"
	}
	if !task || !fd {
		t.Fatalf("missing independent references: %+v", refs)
	}
	if _, err := observeKnown(context.Background(), t.TempDir()+"/missing", []uint64{stat.Ino}, 0); err == nil {
		t.Fatal("failed proc inspection became clean")
	}
}
