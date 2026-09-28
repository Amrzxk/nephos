package service

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/store"
)

func networkFixture(t *testing.T) (*Network, *store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nephos.db")
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return NewNetwork(s, nil, nil), s, path
}

func countTable(t *testing.T, path, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestCreateVPCPreservesNamesAndPermitsOverlappingCIDRs(t *testing.T) {
	n, _, _ := networkFixture(t)
	ctx := context.Background()
	first, err := n.CreateVPC(ctx, CreateVPCInput{Name: "  東京 Lab  ", CIDRBlock: "10.0.1.7/16"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != "東京 Lab" || first.CIDRBlock.String() != "10.0.0.0/16" ||
		first.State != model.StatePending || first.Generation != 1 ||
		first.ObservedGeneration != 0 || first.ShortIndex < 1 {
		t.Fatalf("first VPC=%+v", first)
	}
	second, err := n.CreateVPC(ctx, CreateVPCInput{Name: "東京 lab", CIDRBlock: "10.0.0.0/16"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || second.ShortIndex == first.ShortIndex {
		t.Fatalf("overlapping VPCs share identity: %+v %+v", first, second)
	}
	stored, err := n.GetVPC(ctx, first.ID)
	if err != nil || stored.Name != first.Name {
		t.Fatalf("GetVPC=%+v err=%v", stored, err)
	}
	_, err = n.CreateVPC(ctx, CreateVPCInput{Name: "東京 Lab", CIDRBlock: "10.1.0.0/16"}, "")
	assertCode(t, err, "InvalidParameterValue", 409)
	_, err = n.GetVPC(ctx, "vpc-fffffffffffffffff")
	assertCode(t, err, "InvalidVpcID.NotFound", 404)
}

func TestCreateSubnetValidatesRangeOverlapAndNames(t *testing.T) {
	n, _, _ := networkFixture(t)
	ctx := context.Background()
	vpc, err := n.CreateVPC(ctx, CreateVPCInput{Name: "Main", CIDRBlock: "10.0.0.0/16"}, "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := n.CreateSubnet(ctx, CreateSubnetInput{
		Name: "Main", VPCID: vpc.ID, CIDRBlock: "10.0.1.7/24", AvailabilityZone: "local-1a",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != "Main" || first.CIDRBlock.String() != "10.0.1.0/24" ||
		first.VPCID != vpc.ID || first.State != model.StatePending {
		t.Fatalf("first subnet=%+v", first)
	}
	_, err = n.CreateSubnet(ctx, CreateSubnetInput{
		Name: "Outside", VPCID: vpc.ID, CIDRBlock: "10.1.1.0/24", AvailabilityZone: "local-1a",
	}, "")
	assertCode(t, err, "InvalidSubnet.Range", 400)
	_, err = n.CreateSubnet(ctx, CreateSubnetInput{
		Name: "Overlap", VPCID: vpc.ID, CIDRBlock: "10.0.1.128/25", AvailabilityZone: "local-1a",
	}, "")
	assertCode(t, err, "InvalidSubnet.Conflict", 409)
	_, err = n.CreateSubnet(ctx, CreateSubnetInput{
		Name: "Main", VPCID: vpc.ID, CIDRBlock: "10.0.2.0/24", AvailabilityZone: "local-1b",
	}, "")
	assertCode(t, err, "InvalidParameterValue", 409)
	_, err = n.CreateSubnet(ctx, CreateSubnetInput{
		Name: "Bad AZ", VPCID: vpc.ID, CIDRBlock: "10.0.3.0/24", AvailabilityZone: "moon-1a",
	}, "")
	assertCode(t, err, "InvalidParameterValue", 400)
	second, err := n.CreateSubnet(ctx, CreateSubnetInput{
		Name: "main", VPCID: vpc.ID, CIDRBlock: "10.0.2.0/24", AvailabilityZone: "local-1b",
	}, "")
	if err != nil || second.Name != "main" {
		t.Fatalf("second subnet=%+v err=%v", second, err)
	}
	stored, err := n.GetSubnet(ctx, first.ID)
	if err != nil || stored.Name != first.Name {
		t.Fatalf("GetSubnet=%+v err=%v", stored, err)
	}
	_, err = n.CreateSubnet(ctx, CreateSubnetInput{
		Name: "Orphan", VPCID: "vpc-fffffffffffffffff", CIDRBlock: "10.0.4.0/24", AvailabilityZone: "local-1a",
	}, "")
	assertCode(t, err, "InvalidVpcID.NotFound", 404)
}

func TestCreateIdempotencyCanonicalReplayConflictAndExpiry(t *testing.T) {
	n, s, path := networkFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	n.now = func() time.Time { return now }
	first, err := n.CreateVPC(ctx, CreateVPCInput{Name: "A", CIDRBlock: "10.0.1.7/16"}, "request-1")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := n.CreateVPC(ctx, CreateVPCInput{Name: "A", CIDRBlock: "10.0.0.0/16"}, "request-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("canonical replay=%+v err=%v; want %s", replay, err, first.ID)
	}
	_, err = n.CreateVPC(ctx, CreateVPCInput{Name: "B", CIDRBlock: "10.0.0.0/16"}, "request-1")
	assertCode(t, err, "IdempotentParameterMismatch", 409)
	if countTable(t, path, "vpcs") != 1 || countTable(t, path, "kernel_indexes") != 1 ||
		countTable(t, path, "events") != 1 {
		t.Fatal("idempotent replay allocated an extra row, index or event")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var keyedResourceID string
	if err := db.QueryRow("SELECT resource_id FROM idempotency_requests WHERE key = 'request-1'").Scan(&keyedResourceID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if keyedResourceID != first.ID {
		t.Fatalf("idempotency record resource_id=%q, want %q", keyedResourceID, first.ID)
	}
	events, err := s.EventsAfter(ctx, 0, 10)
	if err != nil || len(events) != 1 || events[0].ResourceID != first.ID {
		t.Fatalf("events=%+v err=%v", events, err)
	}

	now = now.Add(24*time.Hour + time.Second)
	afterExpiry, err := n.CreateVPC(ctx, CreateVPCInput{Name: "B", CIDRBlock: "10.0.0.0/16"}, "request-1")
	if err != nil || afterExpiry.ID == first.ID {
		t.Fatalf("expired key reuse=%+v err=%v", afterExpiry, err)
	}
	if countTable(t, path, "idempotency_requests") != 1 || countTable(t, path, "vpcs") != 2 {
		t.Fatal("expired idempotency key did not replace its record")
	}
}

func TestConcurrentIdempotencyAllocatesOneResourceIndexAndEvent(t *testing.T) {
	n, _, path := networkFixture(t)
	var enqueues atomic.Int32
	n.enqueue = func(string) { enqueues.Add(1) }
	ctx := context.Background()
	const callers = 40
	start := make(chan struct{})
	var wg sync.WaitGroup
	ids := make([]string, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			vpc, err := n.CreateVPC(ctx, CreateVPCInput{Name: "Shared", CIDRBlock: "10.0.0.0/16"}, "same-key")
			ids[i], errs[i] = vpc.ID, err
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range callers {
		if errs[i] != nil || ids[i] != ids[0] {
			t.Fatalf("caller %d got id=%q err=%v; first id=%q", i, ids[i], errs[i], ids[0])
		}
	}
	if countTable(t, path, "vpcs") != 1 || countTable(t, path, "kernel_indexes") != 1 ||
		countTable(t, path, "events") != 1 || enqueues.Load() != 1 {
		t.Fatalf("concurrent replay allocated duplicates; enqueues=%d", enqueues.Load())
	}
}

func TestListPagesUseStableIDOrderAndResourceSpecificTokens(t *testing.T) {
	n, _, _ := networkFixture(t)
	ctx := context.Background()
	var ids []string
	for i := range 5 {
		vpc, err := n.CreateVPC(ctx, CreateVPCInput{
			Name: fmt.Sprintf("vpc-%d", i), CIDRBlock: "10.0.0.0/16",
		}, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, vpc.ID)
	}
	slices.Sort(ids)
	var got []string
	token := ""
	for {
		page, next, err := n.ListVPCs(ctx, 2, token)
		if err != nil {
			t.Fatal(err)
		}
		for _, vpc := range page {
			got = append(got, vpc.ID)
		}
		if next == "" {
			break
		}
		if next == page[len(page)-1].ID {
			t.Fatal("page token exposes its raw ID")
		}
		token = next
	}
	if !slices.Equal(got, ids) {
		t.Fatalf("page IDs=%v, want %v", got, ids)
	}
	_, token, err := n.ListVPCs(ctx, 2, "")
	if err != nil || token == "" {
		t.Fatalf("first page token=%q err=%v", token, err)
	}
	_, _, err = n.ListSubnets(ctx, 2, token)
	assertCode(t, err, "InvalidParameterValue", 400)
	_, _, err = n.ListVPCs(ctx, 2, "not-a-token!")
	assertCode(t, err, "InvalidParameterValue", 400)
	_, _, err = n.ListVPCs(ctx, 101, "")
	assertCode(t, err, "InvalidParameterValue", 400)
}

func TestDeleteDependencyAndCreateRaceAreTransactional(t *testing.T) {
	n, _, path := networkFixture(t)
	ctx := context.Background()
	vpc, err := n.CreateVPC(ctx, CreateVPCInput{Name: "Race", CIDRBlock: "10.0.0.0/16"}, "")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var deleteErr, createErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, deleteErr = n.DeleteVPC(ctx, vpc.ID)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, createErr = n.CreateSubnet(ctx, CreateSubnetInput{
			Name: "Child", VPCID: vpc.ID, CIDRBlock: "10.0.1.0/24", AvailabilityZone: "local-1a",
		}, "")
	}()
	close(start)
	wg.Wait()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state string
	var children int
	if err := db.QueryRow("SELECT state FROM vpcs WHERE id = ?", vpc.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM subnets WHERE vpc_id = ?", vpc.ID).Scan(&children); err != nil {
		t.Fatal(err)
	}
	if state == "deleting" && children > 0 {
		t.Fatalf("delete/create race left deleting VPC with %d subnets", children)
	}
	if deleteErr == nil && createErr == nil {
		t.Fatal("both dependent create and parent delete succeeded")
	}
	if deleteErr != nil && createErr != nil {
		t.Fatalf("both operations failed: delete=%v create=%v", deleteErr, createErr)
	}
}

func TestEventsAreOrderedAndDeletionWaitsForDependencies(t *testing.T) {
	n, s, _ := networkFixture(t)
	ctx := context.Background()
	vpc, err := n.CreateVPC(ctx, CreateVPCInput{Name: "A", CIDRBlock: "10.0.0.0/16"}, "")
	if err != nil {
		t.Fatal(err)
	}
	subnet, err := n.CreateSubnet(ctx, CreateSubnetInput{
		Name: "B", VPCID: vpc.ID, CIDRBlock: "10.0.1.0/24", AvailabilityZone: "local-1a",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = n.DeleteVPC(ctx, vpc.ID)
	assertCode(t, err, "DependencyViolation", 409)
	deleting, err := n.DeleteSubnet(ctx, subnet.ID)
	if err != nil || deleting.State != model.StateDeleting || deleting.Generation != 2 {
		t.Fatalf("delete subnet=%+v err=%v", deleting, err)
	}
	_, err = n.DeleteVPC(ctx, vpc.ID)
	assertCode(t, err, "DependencyViolation", 409)
	events, err := s.EventsAfter(ctx, 0, 10)
	if err != nil || len(events) != 3 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	for i := range events {
		if events[i].ID != int64(i+1) {
			t.Fatalf("event %d has ID %d", i, events[i].ID)
		}
	}
	resumed, err := s.EventsAfter(ctx, events[1].ID, 10)
	if err != nil || len(resumed) != 1 || resumed[0].ID != events[2].ID {
		t.Fatalf("resumed events=%+v err=%v", resumed, err)
	}
}

func TestIdempotentReplayReturnsOriginalSnapshotAfterDeletion(t *testing.T) {
	n, _, path := networkFixture(t)
	ctx := context.Background()
	original, err := n.CreateVPC(ctx, CreateVPCInput{Name: "Ephemeral", CIDRBlock: "10.0.0.0/16"}, "replay")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("UPDATE vpcs SET state = 'available', observed_generation = 1 WHERE id = ?", original.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := n.CreateVPC(ctx, CreateVPCInput{Name: "Ephemeral", CIDRBlock: "10.0.0.0/16"}, "replay")
	if err != nil || replay != original {
		t.Fatalf("replay after status update=%+v err=%v, want original %+v", replay, err, original)
	}
	if _, err := db.Exec("DELETE FROM vpcs WHERE id = ?", original.ID); err != nil {
		t.Fatal(err)
	}
	replay, err = n.CreateVPC(ctx, CreateVPCInput{Name: "Ephemeral", CIDRBlock: "10.0.0.0/16"}, "replay")
	if err != nil || replay != original {
		t.Fatalf("replay after deletion=%+v err=%v, want original %+v", replay, err, original)
	}
	if got := countTable(t, path, "vpcs"); got != 0 {
		t.Fatalf("replay recreated %d VPC rows", got)
	}
}
