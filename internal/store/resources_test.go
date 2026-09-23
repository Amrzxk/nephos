package store

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"testing"

	"github.com/Amrzxk/nephos/internal/model"
)

func TestWithTxRollsBackResourceIndexAndEventTogether(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "nephos.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sentinel := errors.New("injected failure after event")

	err = s.WithTx(ctx, func(tx *Tx) error {
		id := "vpc-0123456789abcdef0"
		index, err := tx.AllocateIndex(ctx, "vpc", id)
		if err != nil {
			return err
		}
		if err := tx.InsertVPC(ctx, model.VPC{
			ID: id, WorkspaceID: "default", Name: "rollback",
			CIDRBlock:  netip.MustParsePrefix("10.0.0.0/16"),
			ShortIndex: index, State: model.StatePending, Generation: 1,
		}, 100); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, Event{
			WorkspaceID: "default", ResourceType: "vpc", ResourceID: id,
			Action: "created", State: "pending", Generation: 1, CreatedAt: 100,
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("transaction error=%v, want injected failure", err)
	}
	for _, table := range []string{"vpcs", "kernel_indexes", "events"} {
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d rows after rollback", table, count)
		}
	}
}
