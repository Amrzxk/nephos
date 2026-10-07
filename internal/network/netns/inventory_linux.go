package netns

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Observation deliberately includes malformed reserved-name candidates.
// An observation is never, on its own, permission to unmount a namespace.
type Observation struct {
	Name           string
	Inode          uint64
	Owned          bool
	OwnershipError string
}

// Inventory observes reserved paths including invalid mounts and aliases,
// plus valid foreign namespace mounts for read-only marker discovery.
// Failure to enumerate the directory is a required inspection error.
func Inventory(ctx context.Context) ([]Observation, error) {
	return observeDirectory(ctx, namespaceDir)
}

func observeDirectory(ctx context.Context, dir string) ([]Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inventory namespace paths: %w", err)
	}
	items := make([]Observation, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		reserved := strings.HasPrefix(entry.Name(), "nx-")
		item := Observation{Name: entry.Name()}
		path := filepath.Join(dir, entry.Name())
		identity, proofErr := namespaceMountIdentity(ctx, path)
		if !reserved {
			if proofErr != nil {
				if errors.Is(proofErr, unix.EACCES) || errors.Is(proofErr, unix.EPERM) {
					return nil, fmt.Errorf("inspect foreign namespace path %s: %w", path, proofErr)
				}
				continue // Unmarked regular files are not namespace candidates.
			}
			item.Inode = identity.Ino
			items = append(items, item) // Read-only inspect for markers; never ours by name.
			continue
		}
		if proofErr == nil {
			item.Inode = identity.Ino
			proofErr = validateName(entry.Name())
		}
		if proofErr == nil {
			proofErr = requireNamespaceMount(ctx, path)
		}
		if proofErr != nil {
			item.OwnershipError = proofErr.Error()
		} else {
			item.Owned = true
		}
		items = append(items, item)
	}
	return items, nil
}
