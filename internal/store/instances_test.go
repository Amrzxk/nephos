package store

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/model"
)

func TestInstanceMigrationUpgrade(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schema3.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range []string{initialMigration, idempotencyResultMigration, deletionIntentMigration} {
		if _, err := db.ExecContext(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		"PRAGMA user_version=3",
		"INSERT INTO kernel_indexes(resource_kind,resource_id) VALUES ('vpc','vpc-old'),('subnet','subnet-old')",
		"INSERT INTO vpcs(id,workspace_id,short_index,name,cidr_block) VALUES ('vpc-old','default',1,'old vpc','10.0.0.0/16')",
		"INSERT INTO subnets(id,workspace_id,vpc_id,short_index,name,cidr_block,availability_zone) VALUES ('subnet-old','default','vpc-old',2,'old subnet','10.0.1.0/24','local-1a')",
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version, count int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || count != schemaVersion {
		t.Fatalf("schema=%d migrations=%d, want %d", version, count, schemaVersion)
	}
	subnet, err := s.GetSubnet(ctx, "default", "subnet-old")
	if err != nil || subnet.Name != "old subnet" || subnet.VPCID != "vpc-old" {
		t.Fatalf("old data=%+v err=%v", subnet, err)
	}
	instance := model.Instance{ID: "i-old", WorkspaceID: "default", SubnetID: subnet.ID, Name: "new", Generation: 1, State: model.InstancePending}
	eni := model.ENI{ID: "eni-old", WorkspaceID: "default", InstanceID: instance.ID, SubnetID: subnet.ID, PrivateIP: netip.MustParseAddr("10.0.1.4"), MACAddress: "02:00:00:00:00:01", Generation: 1}
	err = s.WithTx(ctx, func(tx *Tx) error {
		var err error
		instance.ShortIndex, err = tx.AllocateIndex(ctx, "instance", instance.ID)
		if err != nil {
			return err
		}
		eni.ShortIndex, err = tx.AllocateIndex(ctx, "eni", eni.ID)
		if err != nil {
			return err
		}
		return tx.InsertInstanceWithENI(ctx, instance, eni, time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInstance(ctx, "default", instance.ID)
	if err != nil || got.RuntimeID != "" || got.DeletionRequested || got.ENI.PrivateIP != eni.PrivateIP {
		t.Fatalf("snapshot=%+v err=%v", got, err)
	}
}

func TestInstanceRuntimeIDGenerationGuard(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, query := range []string{
		"INSERT INTO kernel_indexes(resource_kind,resource_id) VALUES ('vpc','vpc-a'),('subnet','subnet-a'),('instance','i-a'),('eni','eni-a')",
		"INSERT INTO vpcs(id,workspace_id,short_index,name,cidr_block) VALUES ('vpc-a','default',1,'a','10.0.0.0/16')",
		"INSERT INTO subnets(id,workspace_id,vpc_id,short_index,name,cidr_block,availability_zone) VALUES ('subnet-a','default','vpc-a',2,'a','10.0.1.0/24','local-1a')",
		"INSERT INTO instances(id,workspace_id,subnet_id,short_index,name) VALUES ('i-a','default','subnet-a',3,'a')",
		"INSERT INTO enis(id,workspace_id,instance_id,subnet_id,short_index,private_ip,mac_address) VALUES ('eni-a','default','i-a','subnet-a',4,'10.0.1.4','02:00:00:00:00:01')",
	} {
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	id := strings.Repeat("a", 64)
	if err := s.RecordRuntimeID(ctx, "i-a", 2, id); !errors.Is(err, ErrStaleSnapshot) {
		t.Fatalf("stale generation: %v", err)
	}
	if err := s.RecordRuntimeID(ctx, "i-a", 1, id); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRuntimeID(ctx, "i-a", 1, id); err != nil {
		t.Fatalf("idempotent ID: %v", err)
	}
	if err := s.RecordRuntimeID(ctx, "i-a", 1, strings.Repeat("b", 64)); !errors.Is(err, ErrStaleSnapshot) {
		t.Fatalf("different runtime adopted: %v", err)
	}
	if err := s.RecordRuntimeID(ctx, "i-a", 1, ""); err == nil {
		t.Fatal("empty ID accepted")
	}
	err = s.WithTx(ctx, func(tx *Tx) error { return tx.MarkInstanceTerminating(ctx, "default", "i-a", time.Now()) })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRuntimeID(ctx, "i-a", 2, id); !errors.Is(err, ErrStaleSnapshot) {
		t.Fatalf("deleting instance: %v", err)
	}
}
