package store

import (
	"context"
	"fmt"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/store/sqlc"
)

// DesiredState is one transaction's complete maintenance observation. Pending
// and deletion-requested rows still own their effects until physical teardown.
type DesiredState struct {
	VPCs      []model.VPC
	Subnets   []model.Subnet
	Instances []model.Instance
	ENIs      []model.ENI
}

// DesiredSnapshot releases SQLite before any runtime or kernel inspection.
// All pages share a transaction; failures never return a partial owner set.
func (s *Store) DesiredSnapshot(ctx context.Context, workspace string) (DesiredState, error) {
	if workspace != "default" {
		return DesiredState{}, fmt.Errorf("unsupported maintenance workspace %q", workspace)
	}
	var state DesiredState
	err := s.WithTx(ctx, func(tx *Tx) error {
		const pageSize = 100
		for after := ""; ; {
			rows, err := tx.q.ListVPCPage(ctx, sqlc.ListVPCPageParams{WorkspaceID: workspace, ID: after, Limit: pageSize})
			if err != nil {
				return fmt.Errorf("snapshot VPCs: %w", err)
			}
			for i := range rows {
				row := &rows[i]
				vpc, err := vpcFromRow(*row)
				if err != nil {
					return err
				}
				state.VPCs = append(state.VPCs, vpc)
				after = row.ID
			}
			if len(rows) < pageSize {
				break
			}
		}
		for after := ""; ; {
			rows, err := tx.q.ListSubnetPage(ctx, sqlc.ListSubnetPageParams{WorkspaceID: workspace, ID: after, Limit: pageSize})
			if err != nil {
				return fmt.Errorf("snapshot subnets: %w", err)
			}
			for i := range rows {
				row := &rows[i]
				subnet, err := subnetFromRow(*row)
				if err != nil {
					return err
				}
				state.Subnets = append(state.Subnets, subnet)
				after = row.ID
			}
			if len(rows) < pageSize {
				break
			}
		}
		for after := ""; ; {
			rows, err := tx.q.ListInstancePage(ctx, sqlc.ListInstancePageParams{WorkspaceID: workspace, ID: after, Limit: pageSize})
			if err != nil {
				return fmt.Errorf("snapshot instances: %w", err)
			}
			for i := range rows {
				row := &rows[i]
				instance, err := tx.instanceFromRow(ctx, *row)
				if err != nil {
					return err
				}
				state.Instances = append(state.Instances, instance)
				state.ENIs = append(state.ENIs, instance.ENI)
				after = row.ID
			}
			if len(rows) < pageSize {
				break
			}
		}
		// Do not silently hide detached/corrupt leases behind the primary-ENI
		// projection. M1 has exactly one primary ENI per instance.
		var count int
		if err := tx.tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM enis WHERE workspace_id = ?", workspace).Scan(&count); err != nil {
			return fmt.Errorf("snapshot lease count: %w", err)
		}
		if count != len(state.ENIs) {
			return fmt.Errorf("snapshot ENI ownership incomplete: %d leases, %d primary ENIs", count, len(state.ENIs))
		}
		return nil
	})
	if err != nil {
		return DesiredState{}, err
	}
	return state, nil
}
