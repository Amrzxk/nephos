package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/model"
)

func TestInstanceRecordStatusAndDelete(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
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
	if err := s.RecordRuntimeID(ctx, "i-a", 1, id); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.GetInstance(ctx, "default", "i-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInstance(ctx, snapshot, model.InstanceRunning, "", true); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInstance(ctx, snapshot, model.InstanceRunning, "", true); err != nil {
		t.Fatalf("idempotent status: %v", err)
	}
	current, err := s.GetInstance(ctx, "default", "i-a")
	if err != nil || current.State != model.InstanceRunning || current.ObservedGeneration != 1 || current.ENI.State != model.StateAvailable || current.ENI.ObservedGeneration != 1 {
		t.Fatalf("observed instance %+v %v", current, err)
	}
	events, err := s.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	statuses := 0
	for _, event := range events {
		if event.ResourceID == "i-a" && event.State == string(model.InstanceRunning) {
			statuses++
		}
	}
	if statuses != 1 {
		t.Fatalf("duplicate or absent resumable status event: %d", statuses)
	}
	if err := s.WithTx(ctx, func(tx *Tx) error { return tx.MarkInstanceTerminating(ctx, "default", "i-a", time.Now()) }); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInstance(ctx, snapshot, model.InstanceRunning, "", true); !errors.Is(err, ErrStaleSnapshot) {
		t.Fatalf("stale status accepted: %v", err)
	}
	if err := s.FinishInstanceTermination(ctx, snapshot); !errors.Is(err, ErrStaleSnapshot) {
		t.Fatalf("stale teardown accepted: %v", err)
	}
	terminating, err := s.GetInstance(ctx, "default", "i-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishInstanceTermination(ctx, terminating); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetInstance(ctx, "default", "i-a"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("instance row retained: %v", err)
	}
	events, err = s.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1].Action != "deleted" || events[len(events)-1].ResourceID != "i-a" {
		t.Fatalf("durable delete event missing: %+v", events)
	}
}
