package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Amrzxk/nephos/internal/store/sqlc"
)

func TestGeneratedVPCAndEventQueriesRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "nephos.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	q := sqlc.New(s.db)
	id := "vpc-0123456789abcdef0"
	if _, err := q.GetVPC(ctx, sqlc.GetVPCParams{ID: id, WorkspaceID: "default"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing VPC error=%v, want sql.ErrNoRows", err)
	}

	result, err := s.db.ExecContext(ctx, "INSERT INTO kernel_indexes(resource_kind, resource_id) VALUES ('vpc', ?)", id)
	if err != nil {
		t.Fatal(err)
	}
	shortIndex, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	err = q.InsertVPC(ctx, sqlc.InsertVPCParams{
		ID: id, WorkspaceID: "default", ShortIndex: shortIndex,
		Name: "Lab East", CidrBlock: "10.0.0.0/16", CreatedAt: 100, UpdatedAt: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := q.GetVPC(ctx, sqlc.GetVPCParams{ID: id, WorkspaceID: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Lab East" || got.CidrBlock != "10.0.0.0/16" || got.Generation != 1 || got.ObservedGeneration != 0 || got.State != "pending" {
		t.Fatalf("stored VPC=%+v", got)
	}
	page, err := q.ListVPCPage(ctx, sqlc.ListVPCPageParams{WorkspaceID: "default", ID: "", Limit: 2})
	if err != nil || len(page) != 1 || page[0].ID != id {
		t.Fatalf("VPC page=%+v err=%v", page, err)
	}

	if err := q.InsertEvent(ctx, sqlc.InsertEventParams{
		WorkspaceID: "default", ResourceType: "vpc", ResourceID: id,
		Action: "created", State: "pending", Generation: 1, CreatedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}
	events, err := q.EventsAfter(ctx, sqlc.EventsAfterParams{ID: 0, Limit: 10})
	if err != nil || len(events) != 1 || events[0].ResourceID != id || events[0].ID != 1 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestGeneratedSubnetQueriesPreserveRelationships(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "nephos.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	q := sqlc.New(s.db)

	vpcID := "vpc-11111111111111111"
	result, err := s.db.ExecContext(ctx, "INSERT INTO kernel_indexes(resource_kind, resource_id) VALUES ('vpc', ?)", vpcID)
	if err != nil {
		t.Fatal(err)
	}
	vpcIndex, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if err := q.InsertVPC(ctx, sqlc.InsertVPCParams{
		ID: vpcID, WorkspaceID: "default", ShortIndex: vpcIndex,
		Name: "VPC", CidrBlock: "10.0.0.0/16", CreatedAt: 100, UpdatedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateVPCStatus(ctx, sqlc.UpdateVPCStatusParams{
		ID: vpcID, WorkspaceID: "default", State: "available",
		StateReason: "", ObservedGeneration: 1, UpdatedAt: 200,
	}); err != nil {
		t.Fatal(err)
	}
	vpc, err := q.GetVPCByName(ctx, sqlc.GetVPCByNameParams{WorkspaceID: "default", Name: "VPC"})
	if err != nil || vpc.State != "available" || vpc.ObservedGeneration != 1 {
		t.Fatalf("VPC by name=%+v err=%v", vpc, err)
	}

	subnetID := "subnet-22222222222222222"
	result, err = s.db.ExecContext(ctx, "INSERT INTO kernel_indexes(resource_kind, resource_id) VALUES ('subnet', ?)", subnetID)
	if err != nil {
		t.Fatal(err)
	}
	subnetIndex, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if err := q.InsertSubnet(ctx, sqlc.InsertSubnetParams{
		ID: subnetID, WorkspaceID: "default", VpcID: vpcID, ShortIndex: subnetIndex,
		Name: "子網 A", CidrBlock: "10.0.1.0/24", AvailabilityZone: "local-1a",
		CreatedAt: 100, UpdatedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}
	subnet, err := q.GetSubnet(ctx, sqlc.GetSubnetParams{ID: subnetID, WorkspaceID: "default"})
	if err != nil || subnet.Name != "子網 A" || subnet.VpcID != vpcID {
		t.Fatalf("subnet=%+v err=%v", subnet, err)
	}
	page, err := q.ListSubnetPage(ctx, sqlc.ListSubnetPageParams{WorkspaceID: "default", ID: "", Limit: 2})
	if err != nil || len(page) != 1 || page[0].ID != subnetID {
		t.Fatalf("subnet page=%+v err=%v", page, err)
	}
	siblings, err := q.ListSubnetsByVPC(ctx, vpcID)
	if err != nil || len(siblings) != 1 || siblings[0].ID != subnetID {
		t.Fatalf("subnets by VPC=%+v err=%v", siblings, err)
	}
	if err := q.UpdateSubnetStatus(ctx, sqlc.UpdateSubnetStatusParams{
		ID: subnetID, WorkspaceID: "default", State: "available",
		StateReason: "", ObservedGeneration: 1, UpdatedAt: 200,
	}); err != nil {
		t.Fatal(err)
	}
	subnet, err = q.GetSubnetByName(ctx, sqlc.GetSubnetByNameParams{WorkspaceID: "default", Name: "子網 A"})
	if err != nil || subnet.State != "available" {
		t.Fatalf("subnet by name=%+v err=%v", subnet, err)
	}
	if err := q.DeleteVPC(ctx, sqlc.DeleteVPCParams{ID: vpcID, WorkspaceID: "default"}); err == nil {
		t.Fatal("VPC with subnet deleted despite foreign key")
	}
	if err := q.DeleteSubnet(ctx, sqlc.DeleteSubnetParams{ID: subnetID, WorkspaceID: "default"}); err != nil {
		t.Fatal(err)
	}
	if err := q.DeleteVPC(ctx, sqlc.DeleteVPCParams{ID: vpcID, WorkspaceID: "default"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetVPC(ctx, sqlc.GetVPCParams{ID: vpcID, WorkspaceID: "default"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted VPC error=%v", err)
	}
}
