package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/store/sqlc"
)

func sameInstanceDecision(current, snapshot model.Instance) bool {
	return current.ID == snapshot.ID && current.WorkspaceID == snapshot.WorkspaceID &&
		current.Generation == snapshot.Generation && current.DeletionRequested == snapshot.DeletionRequested &&
		current.SubnetID == snapshot.SubnetID && current.ShortIndex == snapshot.ShortIndex &&
		current.RuntimeID == snapshot.RuntimeID && current.ENI.ID == snapshot.ENI.ID &&
		current.ENI.ShortIndex == snapshot.ENI.ShortIndex && current.ENI.PrivateIP == snapshot.ENI.PrivateIP && current.ENI.MACAddress == snapshot.ENI.MACAddress
}

// RetainTerminatingRuntimeID makes a create/terminate race recoverable if
// immediate runtime teardown fails. It never changes a live generation.
func (s *Store) RetainTerminatingRuntimeID(ctx context.Context, instanceID, runtimeID string) error {
	if runtimeID == "" {
		return fmt.Errorf("cannot retain an empty runtime ID")
	}
	count, err := sqlc.New(s.db).RetainTerminatingRuntimeID(ctx, sqlc.RetainTerminatingRuntimeIDParams{RuntimeID: sql.NullString{String: runtimeID, Valid: true}, ID: instanceID})
	if err != nil {
		return fmt.Errorf("retain terminating runtime %s: %w", instanceID, err)
	}
	if count != 1 {
		return ErrStaleSnapshot
	}
	return nil
}

// RecordInstance commits a generation-checked observed lifecycle status and event.
func (s *Store) RecordInstance(ctx context.Context, snapshot model.Instance, status model.InstanceState, reason string, observed bool) error {
	if status != model.InstanceRunning && status != model.InstanceFailed {
		return fmt.Errorf("invalid reconciled instance status %q", status)
	}
	if observed && (status != model.InstanceRunning || snapshot.RuntimeID == "" || snapshot.DeletionRequested) {
		return fmt.Errorf("cannot observe instance %s as %s", snapshot.ID, status)
	}
	return s.WithTx(ctx, func(tx *Tx) error {
		current, err := tx.GetInstance(ctx, snapshot.WorkspaceID, snapshot.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrStaleSnapshot
		}
		if err != nil {
			return err
		}
		if !sameInstanceDecision(current, snapshot) {
			return ErrStaleSnapshot
		}
		generation := current.ObservedGeneration
		provisioned := int64(0)
		if current.Provisioned || observed {
			provisioned = 1
		}
		if observed {
			generation = current.Generation
		}
		if current.State == status && current.StateReason == reason && current.ObservedGeneration == generation && current.Provisioned == (provisioned != 0) {
			return nil
		}
		now := time.Now().UTC().Unix()
		if err := tx.q.UpdateInstanceStatus(ctx, sqlc.UpdateInstanceStatusParams{State: string(status), StateReason: reason,
			ObservedGeneration: generation, UpdatedAt: now, Provisioned: provisioned, ID: current.ID, WorkspaceID: current.WorkspaceID}); err != nil {
			return fmt.Errorf("record instance %s status: %w", current.ID, err)
		}
		if !current.DeletionRequested {
			eniState := model.StateFailed
			eniGeneration := current.ENI.ObservedGeneration
			if observed {
				eniState = model.StateAvailable
				eniGeneration = current.ENI.Generation
			}
			if err := tx.q.UpdateInstanceENIStatus(ctx, sqlc.UpdateInstanceENIStatusParams{State: string(eniState),
				StateReason: reason, ObservedGeneration: eniGeneration, UpdatedAt: now,
				ID: current.ENI.ID, WorkspaceID: current.WorkspaceID}); err != nil {
				return fmt.Errorf("record ENI %s status: %w", current.ENI.ID, err)
			}
		}
		return tx.AppendEvent(ctx, Event{WorkspaceID: current.WorkspaceID, ResourceType: "instance", ResourceID: current.ID,
			Action: "status", State: string(status), Generation: current.Generation, Message: reason, CreatedAt: now})
	})
}

// FinishInstanceTermination releases its lease only after owned runtime and ENI teardown.
func (s *Store) FinishInstanceTermination(ctx context.Context, snapshot model.Instance) error {
	return s.WithTx(ctx, func(tx *Tx) error {
		current, err := tx.GetInstance(ctx, snapshot.WorkspaceID, snapshot.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrStaleSnapshot
		}
		if err != nil {
			return err
		}
		if !sameInstanceDecision(current, snapshot) || !current.DeletionRequested {
			return ErrStaleSnapshot
		}
		if err := tx.q.DeleteInstanceENI(ctx, sqlc.DeleteInstanceENIParams{ID: current.ENI.ID, WorkspaceID: current.WorkspaceID}); err != nil {
			return fmt.Errorf("delete ENI %s row: %w", current.ENI.ID, err)
		}
		if err := tx.q.DeleteInstance(ctx, sqlc.DeleteInstanceParams{ID: current.ID, WorkspaceID: current.WorkspaceID}); err != nil {
			return fmt.Errorf("delete instance %s row: %w", current.ID, err)
		}
		return tx.AppendEvent(ctx, Event{WorkspaceID: current.WorkspaceID, ResourceType: "instance", ResourceID: current.ID,
			Action: "deleted", State: "deleted", Generation: current.Generation, CreatedAt: time.Now().UTC().Unix()})
	})
}
