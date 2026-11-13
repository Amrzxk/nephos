package appliance

import (
	"context"
	"errors"
	"fmt"
)

// PurgeResult reports what a purge removed and the limits the old appliance
// actually ran with, so a hard reset can recreate it unchanged.
type PurgeResult struct {
	RemovedContainer bool
	RemovedVolume    bool
	Limits           Limits // zero unless RemovedContainer
}

// Purge deletes the Nephos container and its data volume, which removes every
// instance root, the database and the API token. It does not use the API or
// the database, so it works when the appliance is broken. It refuses before
// deleting anything unless both objects carry the Nephos ownership label, it
// removes the container by the ID it inspected, and it removes the volume
// only if its creation time is unchanged. Missing objects are skipped.
func (m Manager) Purge(ctx context.Context) (PurgeResult, error) {
	var result PurgeResult
	if err := m.Engine.Ping(ctx); err != nil {
		return result, err
	}
	c, err := m.Engine.Inspect(ctx)
	containerFound := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return result, err
	}
	if containerFound {
		if err := verifyContainer(c); err != nil {
			return result, fmt.Errorf("refusing to purge: %w", err)
		}
		if c.ID == "" {
			return result, fmt.Errorf("refusing to purge: appliance container has no ID")
		}
	}
	volume, err := m.Engine.InspectVolume(ctx)
	volumeFound := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return result, err
	}
	if volumeFound && !volume.Owned {
		return result, fmt.Errorf("refusing to purge: docker volume %q is not owned by Nephos", volumeName)
	}

	if containerFound {
		if c.Running {
			if err := m.Engine.Stop(ctx); err != nil && !errors.Is(err, ErrNotFound) {
				return result, err
			}
		}
		if err := m.Engine.Remove(ctx, c.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return result, err
		}
		result.RemovedContainer = true
		result.Limits = c.Limits
	}
	if volumeFound {
		again, err := m.Engine.InspectVolume(ctx)
		switch {
		case errors.Is(err, ErrNotFound):
		case err != nil:
			return result, err
		case !again.Owned || again.CreatedAt != volume.CreatedAt:
			return result, fmt.Errorf("refusing to purge: docker volume %q changed during the purge", volumeName)
		default:
			if err := m.Engine.RemoveVolume(ctx); err != nil && !errors.Is(err, ErrNotFound) {
				return result, err
			}
			result.RemovedVolume = true
		}
	}
	if err := m.Credentials.Remove(); err != nil {
		return result, err
	}
	return result, nil
}

// HardReset purges the appliance and starts a fresh one. Limits the caller
// did not set explicitly keep the values the old container actually used.
func (m Manager) HardReset(ctx context.Context, requested Limits) (PurgeResult, error) {
	purged, err := m.Purge(ctx)
	if err != nil {
		return purged, err
	}
	limits := requested
	if purged.RemovedContainer {
		if !requested.Explicit.Memory && purged.Limits.MemoryBytes > 0 {
			limits.MemoryBytes = purged.Limits.MemoryBytes
		}
		if !requested.Explicit.CPUs && purged.Limits.NanoCPUs > 0 {
			limits.NanoCPUs = purged.Limits.NanoCPUs
		}
		if !requested.Explicit.PIDs && purged.Limits.PIDs > 0 {
			limits.PIDs = purged.Limits.PIDs
		}
	}
	if err := m.Up(ctx, limits); err != nil {
		return purged, fmt.Errorf("purge finished, but the fresh appliance did not start: %w", err)
	}
	return purged, nil
}
