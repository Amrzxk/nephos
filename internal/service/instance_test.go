package service

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/store"
)

func instanceFixture(t *testing.T, cidr string) (*Instances, *Network, *store.Store, string, model.Subnet) {
	t.Helper()
	network, s, path := networkFixture(t)
	ctx := context.Background()
	vpc, err := network.CreateVPC(ctx, CreateVPCInput{Name: "lab", CIDRBlock: "10.0.0.0/16"}, "")
	if err != nil {
		t.Fatal(err)
	}
	subnet, err := network.CreateSubnet(ctx, CreateSubnetInput{Name: "a", VPCID: vpc.ID, CIDRBlock: cidr, AvailabilityZone: "local-1a"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return NewInstances(s, nil, nil), network, s, path, subnet
}

func TestInstanceReservedAndExhaustedRollback(t *testing.T) {
	svc, _, _, path, subnet := instanceFixture(t, "10.0.1.0/28")
	ctx := context.Background()
	for last := 4; last <= 14; last++ {
		got, err := svc.Run(ctx, RunInstanceInput{Name: fmt.Sprintf("instance-%d", last), SubnetID: subnet.ID}, "")
		want := netip.AddrFrom4([4]byte{10, 0, 1, byte(last)})
		if err != nil || got.ENI.PrivateIP != want {
			t.Fatalf("lease=%+v err=%v want=%v", got, err, want)
		}
		if got.ENI.InstanceID != got.ID || got.ENI.SubnetID != subnet.ID || got.InstanceType != "t3.micro" || got.State != model.InstancePending || got.ObservedGeneration != 0 || got.Generation != 1 {
			t.Fatalf("snapshot=%+v", got)
		}
		mac, err := net.ParseMAC(got.ENI.MACAddress)
		if err != nil || len(mac) != 6 || mac[0]&3 != 2 {
			t.Fatalf("not a locally administered unicast MAC: %q", got.ENI.MACAddress)
		}
	}
	counts := map[string]int{}
	for _, table := range []string{"instances", "enis", "kernel_indexes", "events", "idempotency_requests"} {
		counts[table] = countTable(t, path, table)
	}
	_, err := svc.Run(ctx, RunInstanceInput{Name: "twelfth", SubnetID: subnet.ID}, "exhausted")
	assertCode(t, err, "AddressLimitExceeded", 409)
	for table, want := range counts {
		if got := countTable(t, path, table); got != want {
			t.Errorf("%s changed from %d to %d after exhaustion", table, want, got)
		}
	}
}

func TestInstanceConcurrentAllocation(t *testing.T) {
	svc, _, _, _, subnet := instanceFixture(t, "10.0.1.0/24")
	const callers = 40
	results := make([]model.Instance, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = svc.Run(context.Background(), RunInstanceInput{Name: fmt.Sprintf("n-%d", i), SubnetID: subnet.ID}, "")
		}(i)
	}
	close(start)
	wg.Wait()
	ips := map[netip.Addr]bool{}
	macs := map[string]bool{}
	indexes := map[int64]bool{}
	for i, got := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if ips[got.ENI.PrivateIP] || macs[got.ENI.MACAddress] || indexes[got.ShortIndex] || indexes[got.ENI.ShortIndex] || got.ShortIndex == got.ENI.ShortIndex {
			t.Fatalf("duplicate allocation: %+v", got)
		}
		ips[got.ENI.PrivateIP] = true
		macs[got.ENI.MACAddress] = true
		indexes[got.ShortIndex] = true
		indexes[got.ENI.ShortIndex] = true
	}
}

func TestInstanceIdempotency(t *testing.T) {
	svc, _, s, path, subnet := instanceFixture(t, "10.0.1.0/24")
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	var enqueues atomic.Int32
	svc.enqueue = func(string) { enqueues.Add(1) }
	const callers = 40
	results := make([]model.Instance, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = svc.Run(ctx, RunInstanceInput{Name: "  東京 Lab  ", SubnetID: subnet.ID}, "same-key")
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range callers {
		if errs[i] != nil || !reflect.DeepEqual(results[0], results[i]) {
			t.Fatalf("caller %d=%+v err=%v", i, results[i], errs[i])
		}
	}
	first := results[0]
	if first.Name != "東京 Lab" || countTable(t, path, "instances") != 1 || countTable(t, path, "enis") != 1 || countTable(t, path, "kernel_indexes") != 4 || countTable(t, path, "events") != 3 || enqueues.Load() != 1 {
		t.Fatal("replay wrote duplicate side effects")
	}
	if err := s.RecordRuntimeID(ctx, first.ID, first.Generation, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	replay, err := svc.Run(ctx, RunInstanceInput{Name: "東京 Lab", SubnetID: subnet.ID}, "same-key")
	if err != nil || !reflect.DeepEqual(replay, first) {
		t.Fatalf("original snapshot changed: %+v %v", replay, err)
	}
	_, err = svc.Run(ctx, RunInstanceInput{Name: "changed", SubnetID: subnet.ID}, "same-key")
	assertCode(t, err, "IdempotentParameterMismatch", 409)
	now = now.Add(24 * time.Hour)
	next, err := svc.Run(ctx, RunInstanceInput{Name: "changed", SubnetID: subnet.ID}, "same-key")
	if err != nil || next.ID == first.ID {
		t.Fatalf("expired replay=%+v err=%v", next, err)
	}
	if countTable(t, path, "idempotency_requests") != 1 {
		t.Fatal("expired reservation not replaced")
	}
}

func TestInstanceNames(t *testing.T) {
	svc, _, _, _, subnet := instanceFixture(t, "10.0.1.0/24")
	ctx := context.Background()
	for _, name := range []string{"  東京 Lab  ", "東京 lab", strings.Repeat("界", 255)} {
		got, err := svc.Run(ctx, RunInstanceInput{Name: name, SubnetID: subnet.ID}, "")
		if err != nil || got.Name != strings.TrimSpace(name) {
			t.Fatalf("name %q got=%q err=%v", name, got.Name, err)
		}
	}
	for _, name := range []string{"", "  ", "line\nbreak", strings.Repeat("界", 256), string([]byte{0xff})} {
		_, err := svc.Run(ctx, RunInstanceInput{Name: name, SubnetID: subnet.ID}, "")
		assertCode(t, err, "InvalidParameterValue", 400)
	}
	_, err := svc.Run(ctx, RunInstanceInput{Name: "東京 Lab", SubnetID: subnet.ID}, "")
	assertCode(t, err, "InvalidParameterValue", 409)
}

func TestInstancePagination(t *testing.T) {
	svc, _, _, _, subnet := instanceFixture(t, "10.0.1.0/24")
	ctx := context.Background()
	var want []string
	for i := range 7 {
		got, err := svc.Run(ctx, RunInstanceInput{Name: fmt.Sprintf("n-%d", i), SubnetID: subnet.ID}, "")
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, got.ID)
	}
	slices.Sort(want)
	var ids []string
	token := ""
	for range 5 {
		page, err := svc.List(ctx, 2, token)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 2 {
			t.Fatal("page limit exceeded")
		}
		for _, item := range page.Items {
			ids = append(ids, item.ID)
		}
		token = page.NextPageToken
		if token == "" {
			break
		}
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("IDs=%v want=%v", ids, want)
	}
	for _, tc := range []struct {
		limit int
		token string
	}{{-1, ""}, {101, ""}, {1, "garbage"}, {1, encodePageToken("vpc", "vpc-00000000000000000")}} {
		_, err := svc.List(ctx, tc.limit, tc.token)
		assertCode(t, err, "InvalidParameterValue", 400)
	}
}

func TestInstanceDependency(t *testing.T) {
	svc, network, s, path, subnet := instanceFixture(t, "10.0.1.0/24")
	ctx := context.Background()
	_, err := svc.Run(ctx, RunInstanceInput{Name: "absent", SubnetID: "subnet-missing"}, "")
	assertCode(t, err, "InvalidSubnetID.NotFound", 404)
	got, err := svc.Run(ctx, RunInstanceInput{Name: "one", SubnetID: subnet.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = network.DeleteSubnet(ctx, subnet.ID)
	assertCode(t, err, "DependencyViolation", 409)
	if err := svc.Terminate(ctx, got.ID); err != nil {
		t.Fatal(err)
	}
	current, err := svc.Get(ctx, got.ID)
	if err != nil || current.State != model.InstanceShuttingDown || !current.DeletionRequested || current.Generation != 2 || current.ENI.PrivateIP != got.ENI.PrivateIP {
		t.Fatalf("termination=%+v err=%v", current, err)
	}
	before := countTable(t, path, "events")
	if err := svc.Terminate(ctx, got.ID); err != nil {
		t.Fatal(err)
	}
	current, _ = svc.Get(ctx, got.ID)
	if current.Generation != 2 || countTable(t, path, "events") != before {
		t.Fatal("repeat termination mutated intent")
	}
	_, err = network.DeleteSubnet(ctx, subnet.ID)
	assertCode(t, err, "DependencyViolation", 409)
	if err := s.RecordRuntimeID(ctx, got.ID, 1, strings.Repeat("b", 64)); err == nil {
		t.Fatal("stale create runtime attached to deleting instance")
	}
	_, err = svc.Get(ctx, "i-missing")
	assertCode(t, err, "InvalidInstanceID.NotFound", 404)
	err = svc.Terminate(ctx, "i-missing")
	assertCode(t, err, "InvalidInstanceID.NotFound", 404)
	deleting, err := network.CreateSubnet(ctx, CreateSubnetInput{Name: "deleting", VPCID: subnet.VPCID, CIDRBlock: "10.0.2.0/24", AvailabilityZone: "local-1b"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := network.DeleteSubnet(ctx, deleting.ID); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Run(ctx, RunInstanceInput{Name: "too-late", SubnetID: deleting.ID}, "")
	assertCode(t, err, "IncorrectState", 409)
}

func TestInstanceFailedInsertRollsBackAndDoesNotEnqueue(t *testing.T) {
	svc, _, _, path, subnet := instanceFixture(t, "10.0.1.0/24")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TRIGGER reject_eni BEFORE INSERT ON enis BEGIN SELECT RAISE(ABORT, 'injected ENI insert failure'); END"); err != nil {
		t.Fatal(err)
	}
	var enqueues atomic.Int32
	svc.enqueue = func(string) { enqueues.Add(1) }
	_, err = svc.Run(context.Background(), RunInstanceInput{Name: "rollback", SubnetID: subnet.ID}, "rollback-key")
	if err == nil || !strings.Contains(err.Error(), "injected ENI insert failure") {
		t.Fatalf("error=%v", err)
	}
	for table, want := range map[string]int{"instances": 0, "enis": 0, "kernel_indexes": 2, "events": 2, "idempotency_requests": 0} {
		if got := countTable(t, path, table); got != want {
			t.Errorf("%s count=%d want=%d", table, got, want)
		}
	}
	if enqueues.Load() != 0 {
		t.Fatal("failed commit enqueued instance")
	}
	if _, err := db.Exec("DROP TRIGGER reject_eni"); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Run(context.Background(), RunInstanceInput{Name: "rollback", SubnetID: subnet.ID}, "rollback-key")
	if err != nil || got.ENI.PrivateIP != netip.MustParseAddr("10.0.1.4") || got.ShortIndex != 3 || got.ENI.ShortIndex != 4 {
		t.Fatalf("retry=%+v err=%v", got, err)
	}
}
