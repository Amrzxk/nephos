package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/store/sqlc"
)

// Tx is a store-owned transaction. Services validate and make dependency
// decisions through it; only Store commits or rolls it back.
type Tx struct {
	tx *sql.Tx
	q  *sqlc.Queries
}

// WithTx serializes one resource decision and all of its durable side effects.
func (s *Store) WithTx(ctx context.Context, fn func(*Tx) error) error {
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin resource transaction: %w", err)
	}
	defer func() { _ = sqlTx.Rollback() }()
	tx := &Tx{tx: sqlTx, q: sqlc.New(sqlTx)}
	if err := fn(tx); err != nil {
		return fmt.Errorf("resource transaction: %w", err)
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("commit resource transaction: %w", err)
	}
	return nil
}

// AllocateIndex reserves a monotonically increasing short kernel name in the
// same transaction as the resource row. Allocation rows outlive deletions.
func (tx *Tx) AllocateIndex(ctx context.Context, kind, resourceID string) (int64, error) {
	result, err := tx.tx.ExecContext(ctx,
		"INSERT INTO kernel_indexes(resource_kind, resource_id) VALUES (?, ?)", kind, resourceID)
	if err != nil {
		return 0, fmt.Errorf("allocate %s kernel index: %w", kind, err)
	}
	index, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read %s kernel index: %w", kind, err)
	}
	return index, nil
}

// InsertVPC creates a pending VPC row in the current transaction.
func (tx *Tx) InsertVPC(ctx context.Context, vpc model.VPC, now int64) error {
	if err := tx.q.InsertVPC(ctx, sqlc.InsertVPCParams{
		ID: vpc.ID, WorkspaceID: vpc.WorkspaceID, ShortIndex: vpc.ShortIndex,
		Name: vpc.Name, CidrBlock: vpc.CIDRBlock.String(), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return fmt.Errorf("insert VPC %s: %w", vpc.ID, err)
	}
	return nil
}

// InsertSubnet creates a pending subnet row in the current transaction.
func (tx *Tx) InsertSubnet(ctx context.Context, subnet model.Subnet, now int64) error {
	if err := tx.q.InsertSubnet(ctx, sqlc.InsertSubnetParams{
		ID: subnet.ID, WorkspaceID: subnet.WorkspaceID, VpcID: subnet.VPCID,
		ShortIndex: subnet.ShortIndex, Name: subnet.Name,
		CidrBlock: subnet.CIDRBlock.String(), AvailabilityZone: subnet.AvailabilityZone,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return fmt.Errorf("insert subnet %s: %w", subnet.ID, err)
	}
	return nil
}

// GetVPC loads a VPC snapshot from committed state.
func (s *Store) GetVPC(ctx context.Context, workspaceID, id string) (model.VPC, error) {
	row, err := sqlc.New(s.db).GetVPC(ctx, sqlc.GetVPCParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		return model.VPC{}, fmt.Errorf("get VPC %s: %w", id, err)
	}
	return vpcFromRow(row)
}

// GetVPC loads a VPC snapshot inside the current transaction.
func (tx *Tx) GetVPC(ctx context.Context, workspaceID, id string) (model.VPC, error) {
	row, err := tx.q.GetVPC(ctx, sqlc.GetVPCParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		return model.VPC{}, fmt.Errorf("get VPC %s: %w", id, err)
	}
	return vpcFromRow(row)
}

// GetVPCByName resolves a case-sensitive name inside the transaction.
func (tx *Tx) GetVPCByName(ctx context.Context, workspaceID, name string) (model.VPC, error) {
	row, err := tx.q.GetVPCByName(ctx, sqlc.GetVPCByNameParams{WorkspaceID: workspaceID, Name: name})
	if err != nil {
		return model.VPC{}, fmt.Errorf("get VPC name %q: %w", name, err)
	}
	return vpcFromRow(row)
}

// ListVPCPage reads one ID-ordered page after the given ID.
func (s *Store) ListVPCPage(ctx context.Context, workspaceID, afterID string, limit int) ([]model.VPC, error) {
	rows, err := sqlc.New(s.db).ListVPCPage(ctx, sqlc.ListVPCPageParams{
		WorkspaceID: workspaceID, ID: afterID, Limit: int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list VPCs: %w", err)
	}
	items := make([]model.VPC, 0, len(rows))
	for i := range rows {
		item, err := vpcFromRow(rows[i])
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// GetSubnet loads a subnet snapshot from committed state.
func (s *Store) GetSubnet(ctx context.Context, workspaceID, id string) (model.Subnet, error) {
	row, err := sqlc.New(s.db).GetSubnet(ctx, sqlc.GetSubnetParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		return model.Subnet{}, fmt.Errorf("get subnet %s: %w", id, err)
	}
	return subnetFromRow(row)
}

// GetSubnet loads a subnet snapshot inside the current transaction.
func (tx *Tx) GetSubnet(ctx context.Context, workspaceID, id string) (model.Subnet, error) {
	row, err := tx.q.GetSubnet(ctx, sqlc.GetSubnetParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		return model.Subnet{}, fmt.Errorf("get subnet %s: %w", id, err)
	}
	return subnetFromRow(row)
}

// GetSubnetByName resolves a case-sensitive name inside the transaction.
func (tx *Tx) GetSubnetByName(ctx context.Context, workspaceID, name string) (model.Subnet, error) {
	row, err := tx.q.GetSubnetByName(ctx, sqlc.GetSubnetByNameParams{WorkspaceID: workspaceID, Name: name})
	if err != nil {
		return model.Subnet{}, fmt.Errorf("get subnet name %q: %w", name, err)
	}
	return subnetFromRow(row)
}

// ListSubnetPage reads one ID-ordered page after the given ID.
func (s *Store) ListSubnetPage(ctx context.Context, workspaceID, afterID string, limit int) ([]model.Subnet, error) {
	rows, err := sqlc.New(s.db).ListSubnetPage(ctx, sqlc.ListSubnetPageParams{
		WorkspaceID: workspaceID, ID: afterID, Limit: int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list subnets: %w", err)
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

// ListSubnetsByVPC reads all dependents while holding the transaction.
func (tx *Tx) ListSubnetsByVPC(ctx context.Context, vpcID string) ([]model.Subnet, error) {
	rows, err := tx.q.ListSubnetsByVPC(ctx, vpcID)
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

// CountInstancesInSubnet checks whether a subnet can be deleted.
func (tx *Tx) CountInstancesInSubnet(ctx context.Context, subnetID string) (int64, error) {
	count, err := tx.q.CountInstancesInSubnet(ctx, subnetID)
	if err != nil {
		return 0, fmt.Errorf("count instances in subnet %s: %w", subnetID, err)
	}
	return count, nil
}

// MarkVPCDeleting increments desired generation and returns the snapshot.
func (tx *Tx) MarkVPCDeleting(ctx context.Context, workspaceID, id string, now int64) (model.VPC, error) {
	if err := tx.q.MarkVPCDeleting(ctx, sqlc.MarkVPCDeletingParams{
		UpdatedAt: now, ID: id, WorkspaceID: workspaceID,
	}); err != nil {
		return model.VPC{}, fmt.Errorf("mark VPC %s deleting: %w", id, err)
	}
	return tx.GetVPC(ctx, workspaceID, id)
}

// MarkSubnetDeleting increments desired generation and returns the snapshot.
func (tx *Tx) MarkSubnetDeleting(ctx context.Context, workspaceID, id string, now int64) (model.Subnet, error) {
	if err := tx.q.MarkSubnetDeleting(ctx, sqlc.MarkSubnetDeletingParams{
		UpdatedAt: now, ID: id, WorkspaceID: workspaceID,
	}); err != nil {
		return model.Subnet{}, fmt.Errorf("mark subnet %s deleting: %w", id, err)
	}
	return tx.GetSubnet(ctx, workspaceID, id)
}

// Idempotency is a durable keyed-create reservation.
type Idempotency struct {
	WorkspaceID  string
	Operation    string
	Key          string
	PayloadHash  string
	ResourceID   string
	ResponseJSON string
	CreatedAt    int64
	ExpiresAt    int64
}

// GetIdempotency reads a keyed-create record inside the transaction.
func (tx *Tx) GetIdempotency(ctx context.Context, workspaceID, operation, key string) (Idempotency, error) {
	row, err := tx.q.GetIdempotency(ctx, sqlc.GetIdempotencyParams{
		WorkspaceID: workspaceID, Operation: operation, Key: key,
	})
	if err != nil {
		return Idempotency{}, fmt.Errorf("get idempotency key: %w", err)
	}
	return Idempotency{
		WorkspaceID: row.WorkspaceID, Operation: row.Operation, Key: row.Key,
		PayloadHash: row.PayloadHash, ResourceID: row.ResourceID,
		ResponseJSON: row.ResponseJson, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
	}, nil
}

// PutIdempotency writes or replaces a keyed-create record atomically.
func (tx *Tx) PutIdempotency(ctx context.Context, record Idempotency) error {
	if err := tx.q.PutIdempotency(ctx, sqlc.PutIdempotencyParams{
		WorkspaceID: record.WorkspaceID, Operation: record.Operation, Key: record.Key,
		PayloadHash: record.PayloadHash, ResourceID: record.ResourceID,
		ResponseJson: record.ResponseJSON, CreatedAt: record.CreatedAt, ExpiresAt: record.ExpiresAt,
	}); err != nil {
		return fmt.Errorf("put idempotency key: %w", err)
	}
	return nil
}

func vpcFromRow(row sqlc.Vpc) (model.VPC, error) {
	prefix, err := netip.ParsePrefix(row.CidrBlock)
	if err != nil || !prefix.Addr().Is4() {
		return model.VPC{}, fmt.Errorf("VPC %s has corrupt CIDR block %q", row.ID, row.CidrBlock)
	}
	return model.VPC{
		ID: row.ID, WorkspaceID: row.WorkspaceID, Name: row.Name, CIDRBlock: prefix,
		ShortIndex: row.ShortIndex, State: model.State(row.State), StateReason: row.StateReason,
		Generation: row.Generation, ObservedGeneration: row.ObservedGeneration,
	}, nil
}

func subnetFromRow(row sqlc.Subnet) (model.Subnet, error) {
	prefix, err := netip.ParsePrefix(row.CidrBlock)
	if err != nil || !prefix.Addr().Is4() {
		return model.Subnet{}, fmt.Errorf("subnet %s has corrupt CIDR block %q", row.ID, row.CidrBlock)
	}
	return model.Subnet{
		ID: row.ID, WorkspaceID: row.WorkspaceID, VPCID: row.VpcID, Name: row.Name,
		CIDRBlock: prefix, AvailabilityZone: row.AvailabilityZone, ShortIndex: row.ShortIndex,
		State: model.State(row.State), StateReason: row.StateReason,
		Generation: row.Generation, ObservedGeneration: row.ObservedGeneration,
	}, nil
}

// IsUniqueViolation recognizes SQLite's extended unique-constraint result.
func IsUniqueViolation(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}

// IsForeignKeyViolation recognizes SQLite's extended foreign-key result.
func IsForeignKeyViolation(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY
}
