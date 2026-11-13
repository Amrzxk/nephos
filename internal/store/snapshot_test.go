package store

import (
	"context"
	"path/filepath"
	"testing"
)

// Dropping pending/deleting rows or their ENIs from maintenance observation
// would let GC remove a root which still has a durable owner.
func TestDesiredSnapshotRetainsPendingAndDeletingOwners(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, query := range []string{
		"INSERT INTO kernel_indexes(resource_kind,resource_id) VALUES ('vpc','vpc-a'),('subnet','subnet-a'),('instance','i-a'),('eni','eni-a')",
		"INSERT INTO vpcs(id,workspace_id,short_index,name,cidr_block) VALUES ('vpc-a','default',1,'a','10.0.0.0/16')",
		"INSERT INTO subnets(id,workspace_id,vpc_id,short_index,name,cidr_block,availability_zone) VALUES ('subnet-a','default','vpc-a',2,'a','10.0.1.0/24','local-1a')",
		"INSERT INTO instances(id,workspace_id,subnet_id,short_index,name,deletion_requested) VALUES ('i-a','default','subnet-a',3,'a',1)",
		"INSERT INTO enis(id,workspace_id,instance_id,subnet_id,short_index,private_ip,mac_address) VALUES ('eni-a','default','i-a','subnet-a',4,'10.0.1.4','02:00:00:00:00:01')",
	} {
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.DesiredSnapshot(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.VPCs) != 1 || len(got.Subnets) != 1 || len(got.Instances) != 1 || len(got.ENIs) != 1 {
		t.Fatalf("incomplete desired snapshot: %+v", got)
	}
	if got.Instances[0].RuntimeID != "" || !got.Instances[0].DeletionRequested || got.ENIs[0].SubnetID != got.Subnets[0].ID || got.Subnets[0].VPCID != got.VPCs[0].ID {
		t.Fatalf("lost retained ownership: %+v", got)
	}
	if _, err := s.DesiredSnapshot(ctx, "foreign"); err == nil {
		t.Fatal("unsupported workspace was accepted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DesiredSnapshot(ctx, "default"); err == nil {
		t.Fatal("closed store became empty desired world")
	}
}
