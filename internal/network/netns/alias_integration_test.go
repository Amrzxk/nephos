//go:build linux && integration

package netns

import (
	"context"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// A canonical NSFS pathname is not ownership proof if it aliases root or
// another VPC. Ensure/Do/Delete must all refuse it before any mutation.
func TestNamespaceAliasRejected(t *testing.T) {
	for _, tc := range []string{"root", "other-vpc", "symlink", "user-namespace"} {
		t.Run(tc, func(t *testing.T) {
			ctx := context.Background()
			if err := os.MkdirAll(namespaceDir, 0o755); err != nil {
				t.Fatal(err)
			}
			name := freshTestName(t)
			path := namespacePath(name)
			source := "/proc/1/ns/net"
			if tc == "user-namespace" {
				source = "/proc/1/ns/user"
			}
			if tc == "other-vpc" || tc == "symlink" {
				original := freshTestName(t)
				if err := Ensure(ctx, original); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := Delete(ctx, original); err != nil {
						t.Error(err)
					}
				})
				source = namespacePath(original)
			}
			if tc == "symlink" {
				if err := os.Symlink(source, path); err != nil {
					t.Fatal(err)
				}
			} else {
				file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDONLY, 0o444)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				if err := unix.Mount(source, path, "none", unix.MS_BIND, ""); err != nil {
					_ = os.Remove(path)
					t.Fatal(err)
				}
			}
			aliasPresent := true
			t.Cleanup(func() {
				if !aliasPresent {
					return
				}
				if tc != "symlink" {
					if err := unix.Unmount(path, 0); err != nil {
						t.Error(err)
						return
					}
				}
				if err := os.Remove(path); err != nil {
					t.Error(err)
				}
			})
			for _, operation := range []string{"ensure", "do", "delete"} {
				var err error
				switch operation {
				case "ensure":
					err = Ensure(ctx, name)
				case "do":
					err = Do(ctx, name, func() error {
						t.Error("alias callback ran in a protected namespace")
						return nil
					})
				case "delete":
					err = Delete(ctx, name)
					if err == nil {
						aliasPresent = false
					}
				}
				want := "alias"
				switch tc {
				case "symlink":
					want = "symlink"
				case "user-namespace":
					want = "not a network namespace"
				}
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("%s accepted %s alias: %v", operation, tc, err)
				}
			}
		})
	}
}
