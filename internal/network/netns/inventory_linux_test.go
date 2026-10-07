package netns

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNamespaceInventoryDoesNotHideMalformedReservedNames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"nx-vpc-01", "nx-vpc-100000000", "nx-vpc-2", "nx-other", "foreign"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	items, err := observeDirectory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("got %d candidates, want all four reserved names: %+v", len(items), items)
	}
	for _, item := range items {
		if item.Owned || item.OwnershipError == "" {
			t.Fatalf("regular/malformed path became owned: %+v", item)
		}
	}
	if _, err := observeDirectory(context.Background(), filepath.Join(dir, "nx-other")); err == nil {
		t.Fatal("failed directory inspection returned empty inventory")
	}
}
