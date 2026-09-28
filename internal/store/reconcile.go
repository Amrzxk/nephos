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

// ErrStaleSnapshot means desired state changed while an engine call ran.
// The caller must reread SQLite and reconcile again.
var ErrStaleSnapshot = errors.New("desired resource snapshot changed during reconciliation")

// ListSubnetsByVPC reads all committed children of a VPC.
func (s *Store) ListSubnetsByVPC(ctx context.Context, vpcID string) ([]model.Subnet, error) {
	rows, err := sqlc.New(s.db).ListSubnetsByVPC(ctx, vpcID)
	if err != nil {
		return nil, fmt.Errorf("list subnets for VPC %s: %w", vpcID, err)
	}
	items := make([]model.Subnet, 0, len(rows))
	for i := range rows {
		item, err := subnetFromRow(rows[i])
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// RecordTopology atomically records the observed result of one VPC engine
// operation. Success clears failure reasons, completes child deletes, or
// removes a deleted VPC. Failure retains generations and deletion intent.
func (s *Store) RecordTopology(ctx context.Context, vpc model.VPC, subnets []model.Subnet, effectErr error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.WithTx(ctx, func(tx *Tx) error {
		current, err := tx.GetVPC(ctx, vpc.WorkspaceID, vpc.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrStaleSnapshot
		}
		if err != nil {
			return err
		}
		if current.Generation != vpc.Generation || current.DeletionRequested != vpc.DeletionRequested || current.ShortIndex != vpc.ShortIndex {
			return ErrStaleSnapshot
		}
		children, err := tx.ListSubnetsByVPC(ctx, vpc.ID)
		if err != nil {
			return err
		}
		if !sameDesiredChildren(children, subnets) {
			return ErrStaleSnapshot
		}
		now := time.Now().UTC().Unix()
		if vpc.DeletionRequested && effectErr == nil {
			if len(children) != 0 {
				return fmt.Errorf("cannot delete VPC %s with subnets", vpc.ID)
			}
			if err := tx.q.DeleteVPC(ctx, sqlc.DeleteVPCParams{ID: vpc.ID, WorkspaceID: vpc.WorkspaceID}); err != nil {
				return fmt.Errorf("delete VPC row %s: %w", vpc.ID, err)
			}
			return tx.AppendEvent(ctx, Event{WorkspaceID: vpc.WorkspaceID, ResourceType: "vpc", ResourceID: vpc.ID,
				Action: "deleted", State: "deleted", Generation: vpc.Generation, CreatedAt: now})
		}
		state, reason := model.StateAvailable, ""
		if effectErr != nil {
			state, reason = model.StateFailed, effectErr.Error()
		}
		if err := tx.recordVPCStatus(ctx, current, state, reason, effectErr == nil, now); err != nil {
			return err
		}
		for i := range children {
			child := &children[i]
			if child.DeletionRequested && effectErr == nil {
				if err := tx.q.DeleteSubnet(ctx, sqlc.DeleteSubnetParams{ID: child.ID, WorkspaceID: child.WorkspaceID}); err != nil {
					return fmt.Errorf("delete subnet row %s: %w", child.ID, err)
				}
				if err := tx.AppendEvent(ctx, Event{WorkspaceID: child.WorkspaceID, ResourceType: "subnet", ResourceID: child.ID,
					Action: "deleted", State: "deleted", Generation: child.Generation, CreatedAt: now}); err != nil {
					return err
				}
				continue
			}
			if err := tx.recordSubnetStatus(ctx, *child, state, reason, effectErr == nil, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func sameDesiredChildren(current, applied []model.Subnet) bool {
	if len(current) != len(applied) {
		return false
	}
	for i := range current {
		if current[i].ID != applied[i].ID || current[i].Generation != applied[i].Generation ||
			current[i].DeletionRequested != applied[i].DeletionRequested || current[i].ShortIndex != applied[i].ShortIndex {
			return false
		}
	}
	return true
}

func (tx *Tx) recordVPCStatus(ctx context.Context, vpc model.VPC, state model.State, reason string, observed bool, now int64) error {
	generation := vpc.ObservedGeneration
	if observed {
		generation = vpc.Generation
	}
	if vpc.State == state && vpc.StateReason == reason && vpc.ObservedGeneration == generation {
		return nil
	}
	if err := tx.q.UpdateVPCStatus(ctx, sqlc.UpdateVPCStatusParams{
		State: string(state), StateReason: reason, ObservedGeneration: generation,
		UpdatedAt: now, ID: vpc.ID, WorkspaceID: vpc.WorkspaceID,
	}); err != nil {
		return fmt.Errorf("update VPC %s status: %w", vpc.ID, err)
	}
	return tx.AppendEvent(ctx, Event{WorkspaceID: vpc.WorkspaceID, ResourceType: "vpc", ResourceID: vpc.ID,
		Action: "status", State: string(state), Generation: vpc.Generation, Message: reason, CreatedAt: now})
}

func (tx *Tx) recordSubnetStatus(ctx context.Context, subnet model.Subnet, state model.State, reason string, observed bool, now int64) error {
	generation := subnet.ObservedGeneration
	if observed {
		generation = subnet.Generation
	}
	if subnet.State == state && subnet.StateReason == reason && subnet.ObservedGeneration == generation {
		return nil
	}
	if err := tx.q.UpdateSubnetStatus(ctx, sqlc.UpdateSubnetStatusParams{
		State: string(state), StateReason: reason, ObservedGeneration: generation,
		UpdatedAt: now, ID: subnet.ID, WorkspaceID: subnet.WorkspaceID,
	}); err != nil {
		return fmt.Errorf("update subnet %s status: %w", subnet.ID, err)
	}
	return tx.AppendEvent(ctx, Event{WorkspaceID: subnet.WorkspaceID, ResourceType: "subnet", ResourceID: subnet.ID,
		Action: "status", State: string(state), Generation: subnet.Generation, Message: reason, CreatedAt: now})
}
