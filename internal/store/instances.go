package store

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/store/sqlc"
)

// InsertInstanceWithENI persists one instance and its primary lease atomically.
func (tx *Tx) InsertInstanceWithENI(ctx context.Context, instance model.Instance, eni model.ENI, now time.Time) error {
	if eni.InstanceID != instance.ID || eni.SubnetID != instance.SubnetID || eni.WorkspaceID != instance.WorkspaceID {
		return fmt.Errorf("insert instance %s: primary ENI identity mismatch", instance.ID)
	}
	if !eni.PrivateIP.Is4() {
		return fmt.Errorf("insert ENI %s: invalid IPv4 address", eni.ID)
	}
	if _, err := validENIMAC(eni.MACAddress); err != nil {
		return err
	}
	if err := tx.q.InsertInstance(ctx, sqlc.InsertInstanceParams{
		ID: instance.ID, WorkspaceID: instance.WorkspaceID, SubnetID: instance.SubnetID,
		ShortIndex: instance.ShortIndex, Name: instance.Name, CreatedAt: now.Unix(), UpdatedAt: now.Unix(),
	}); err != nil {
		return fmt.Errorf("insert instance %s: %w", instance.ID, err)
	}
	if err := tx.q.InsertENI(ctx, sqlc.InsertENIParams{
		ID: eni.ID, WorkspaceID: eni.WorkspaceID, InstanceID: eni.InstanceID, SubnetID: eni.SubnetID,
		ShortIndex: eni.ShortIndex, PrivateIp: eni.PrivateIP.String(), MacAddress: eni.MACAddress,
		CreatedAt: now.Unix(), UpdatedAt: now.Unix(),
	}); err != nil {
		return fmt.Errorf("insert ENI %s: %w", eni.ID, err)
	}
	return nil
}

// GetInstance reads the instance and ENI in the same SQLite snapshot.
func (s *Store) GetInstance(ctx context.Context, workspaceID, id string) (model.Instance, error) {
	var result model.Instance
	err := s.WithTx(ctx, func(tx *Tx) error {
		var err error
		result, err = tx.GetInstance(ctx, workspaceID, id)
		return err
	})
	return result, err
}

// GetInstance reads a consistent instance snapshot within the transaction.
func (tx *Tx) GetInstance(ctx context.Context, workspaceID, id string) (model.Instance, error) {
	row, err := tx.q.GetInstance(ctx, sqlc.GetInstanceParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		return model.Instance{}, fmt.Errorf("get instance %s: %w", id, err)
	}
	return tx.instanceFromRow(ctx, row)
}

// GetInstanceByName resolves a case-sensitive workspace name.
func (tx *Tx) GetInstanceByName(ctx context.Context, workspaceID, name string) (model.Instance, error) {
	row, err := tx.q.GetInstanceByName(ctx, sqlc.GetInstanceByNameParams{WorkspaceID: workspaceID, Name: name})
	if err != nil {
		return model.Instance{}, fmt.Errorf("get instance name %q: %w", name, err)
	}
	return tx.instanceFromRow(ctx, row)
}

// ListInstancePage reads an ID-ordered page with consistent primary ENIs.
func (s *Store) ListInstancePage(ctx context.Context, workspaceID, afterID string, limit int) ([]model.Instance, error) {
	items := make([]model.Instance, 0)
	err := s.WithTx(ctx, func(tx *Tx) error {
		rows, err := tx.q.ListInstancePage(ctx, sqlc.ListInstancePageParams{WorkspaceID: workspaceID, ID: afterID, Limit: int64(limit)})
		if err != nil {
			return fmt.Errorf("list instances: %w", err)
		}
		for _, row := range rows {
			instance, err := tx.instanceFromRow(ctx, row)
			if err != nil {
				return err
			}
			items = append(items, instance)
		}
		return nil
	})
	return items, err
}

// ENIAddresses reads the subnet's current leases while holding the write decision.
func (tx *Tx) ENIAddresses(ctx context.Context, subnetID string) (map[netip.Addr]struct{}, error) {
	rows, err := tx.q.ListENIAddresses(ctx, subnetID)
	if err != nil {
		return nil, fmt.Errorf("list ENI addresses in %s: %w", subnetID, err)
	}
	used := make(map[netip.Addr]struct{}, len(rows))
	for _, raw := range rows {
		address, err := netip.ParseAddr(raw)
		if err != nil {
			return nil, fmt.Errorf("decode ENI private address %q: %w", raw, err)
		}
		if !address.Is4() {
			return nil, fmt.Errorf("decode ENI private address %q: not IPv4", raw)
		}
		used[address] = struct{}{}
	}
	return used, nil
}

// MarkInstanceTerminating keeps the lease until reverse-order cleanup succeeds.
func (tx *Tx) MarkInstanceTerminating(ctx context.Context, workspaceID, id string, now time.Time) error {
	if err := tx.q.MarkInstanceTerminating(ctx, sqlc.MarkInstanceTerminatingParams{ID: id, WorkspaceID: workspaceID, UpdatedAt: now.Unix()}); err != nil {
		return fmt.Errorf("mark instance %s terminating: %w", id, err)
	}
	return nil
}

// RecordRuntimeID binds a create result only to the unchanged, live generation.
// A retry may repeat the same ID, but cannot overwrite another runtime identity.
func (s *Store) RecordRuntimeID(ctx context.Context, instanceID string, generation int64, runtimeID string) error {
	if runtimeID == "" {
		return fmt.Errorf("record instance %s runtime: empty ID", instanceID)
	}
	runtime := sql.NullString{String: runtimeID, Valid: true}
	count, err := sqlc.New(s.db).RecordRuntimeID(ctx, sqlc.RecordRuntimeIDParams{
		ID: instanceID, Generation: generation, RuntimeID: runtime, RuntimeID_2: runtime,
	})
	if err != nil {
		return fmt.Errorf("record instance %s runtime: %w", instanceID, err)
	}
	if count != 1 {
		return ErrStaleSnapshot
	}
	return nil
}

func (tx *Tx) instanceFromRow(ctx context.Context, row sqlc.Instance) (model.Instance, error) {
	eni, err := tx.q.GetPrimaryENI(ctx, sqlc.GetPrimaryENIParams{InstanceID: row.ID, WorkspaceID: row.WorkspaceID})
	if err != nil {
		return model.Instance{}, fmt.Errorf("get primary ENI for %s: %w", row.ID, err)
	}
	address, err := netip.ParseAddr(eni.PrivateIp)
	if err != nil {
		return model.Instance{}, fmt.Errorf("decode ENI %s private address: %w", eni.ID, err)
	}
	if !address.Is4() {
		return model.Instance{}, fmt.Errorf("decode ENI %s: not IPv4", eni.ID)
	}
	if _, err := validENIMAC(eni.MacAddress); err != nil {
		return model.Instance{}, err
	}
	return model.Instance{
		ID: row.ID, WorkspaceID: row.WorkspaceID, SubnetID: row.SubnetID, ShortIndex: row.ShortIndex,
		Name: row.Name, InstanceType: "t3.micro", State: model.InstanceState(row.State), StateReason: row.StateReason,
		Generation: row.Generation, ObservedGeneration: row.ObservedGeneration,
		DeletionRequested: row.DeletionRequested != 0, RuntimeID: row.RuntimeID.String,
		ENI: model.ENI{ID: eni.ID, WorkspaceID: eni.WorkspaceID, InstanceID: eni.InstanceID,
			SubnetID: eni.SubnetID, ShortIndex: eni.ShortIndex, PrivateIP: address, MACAddress: eni.MacAddress,
			Generation: eni.Generation, ObservedGeneration: eni.ObservedGeneration, State: model.State(eni.State), StateReason: eni.StateReason},
	}, nil
}

func validENIMAC(raw string) (net.HardwareAddr, error) {
	mac, err := net.ParseMAC(raw)
	if err != nil {
		return nil, fmt.Errorf("decode ENI MAC %q: %w", raw, err)
	}
	if len(mac) != 6 || mac[0]&3 != 2 {
		return nil, fmt.Errorf("ENI MAC %q must be locally administered unicast", raw)
	}
	return mac, nil
}
