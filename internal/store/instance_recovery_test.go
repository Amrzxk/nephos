package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/model"
)

func seedRecoveryRows(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, query := range []string{
		"INSERT INTO kernel_indexes(resource_kind,resource_id) VALUES ('vpc','vpc-a'),('subnet','subnet-a'),('instance','i-a'),('eni','eni-a')",
		"INSERT INTO vpcs(id,workspace_id,short_index,name,cidr_block) VALUES ('vpc-a','default',1,'a','10.0.0.0/16')",
		"INSERT INTO subnets(id,workspace_id,vpc_id,short_index,name,cidr_block,availability_zone) VALUES ('subnet-a','default','vpc-a',2,'a','10.0.1.0/24','local-1a')",
		"INSERT INTO instances(id,workspace_id,subnet_id,short_index,name) VALUES ('i-a','default','subnet-a',3,'a')",
		"INSERT INTO enis(id,workspace_id,instance_id,subnet_id,short_index,private_ip,mac_address) VALUES ('eni-a','default','i-a','subnet-a',4,'10.0.1.4','02:00:00:00:00:01')",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrationProvisioningHistory(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		observed    int
		cached      bool
		want        int
	}{
		{name: "running", state: "running", want: 1},
		{name: "formerly observed", state: "failed", observed: 1, cached: true, want: 1},
		{name: "observed empty cache", state: "failed", observed: 1, want: 1},
		{name: "pre-running cache", state: "failed", cached: true},
		{name: "never attempted", state: "pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "legacy.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			for _, migration := range []string{initialMigration, idempotencyResultMigration, deletionIntentMigration, instanceDeletionMigration} {
				if _, err := db.ExecContext(ctx, migration); err != nil {
					t.Fatal(err)
				}
			}
			seedRecoveryRows(t, db)
			var cache any
			if tc.cached {
				cache = "old-runtime-cache"
			}
			if _, err := db.ExecContext(ctx, "UPDATE instances SET state=?,observed_generation=?,runtime_id=?", tc.state, tc.observed, cache); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, "PRAGMA user_version=4"); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			s, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var got int
			if err := s.db.QueryRowContext(ctx, "SELECT provisioned FROM instances WHERE id='i-a'").Scan(&got); err != nil {
				t.Fatalf("missing durable provisioning fact: %v", err)
			}
			if got != tc.want {
				t.Fatalf("provisioned=%d want=%d", got, tc.want)
			}
		})
	}
}

func TestRuntimeRebindCAS(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedRecoveryRows(t, s.db)
	if err := s.RecordRuntimeID(ctx, "i-a", 1, "old"); err != nil {
		t.Fatal(err)
	}
	rebind := func(oldID, newID string, generation int64) error {
		return s.RebindRuntimeID(ctx, "i-a", generation, oldID, newID)
	}
	if err := rebind("old", "new", 1); err != nil {
		t.Fatalf("expected guarded rebind rejected: %v", err)
	}
	for _, tc := range []struct {
		old, next  string
		generation int64
	}{
		{old: "old", next: "replacement", generation: 1},
		{old: "new", next: "replacement", generation: 2},
	} {
		if err := rebind(tc.old, tc.next, tc.generation); !errors.Is(err, ErrStaleSnapshot) {
			t.Fatalf("stale rebind accepted: %v", err)
		}
	}
	if err := s.WithTx(ctx, func(tx *Tx) error { return tx.MarkInstanceTerminating(ctx, "default", "i-a", time.Now()) }); err != nil {
		t.Fatal(err)
	}
	if err := rebind("new", "replacement", 2); !errors.Is(err, ErrStaleSnapshot) {
		t.Fatalf("terminating rebind accepted: %v", err)
	}
}

func TestProvisionedFactAndEventAtomic(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedRecoveryRows(t, s.db)
	if err := s.RecordRuntimeID(ctx, "i-a", 1, "retained"); err != nil {
		t.Fatal(err)
	}
	i, err := s.GetInstance(ctx, "default", "i-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "CREATE TRIGGER reject_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'test event failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInstance(ctx, i, model.InstanceRunning, "", true); err == nil {
		t.Fatal("event rejection did not roll back")
	}
	var provisioned int
	if err := s.db.QueryRowContext(ctx, "SELECT provisioned FROM instances WHERE id='i-a'").Scan(&provisioned); err != nil {
		t.Fatalf("missing preservation state: %v", err)
	}
	if provisioned != 0 {
		t.Fatal("provisioned committed without running event")
	}
	if _, err := s.db.ExecContext(ctx, "DROP TRIGGER reject_event"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInstance(ctx, i, model.InstanceRunning, "", true); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT provisioned FROM instances WHERE id='i-a'").Scan(&provisioned); err != nil || provisioned != 1 {
		t.Fatalf("provisioning not recorded: %d %v", provisioned, err)
	}
}
