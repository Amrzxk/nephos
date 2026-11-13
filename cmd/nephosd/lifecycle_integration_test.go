//go:build linux && integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/compute/podman"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/topology"
	"github.com/Amrzxk/nephos/internal/service"
	"github.com/Amrzxk/nephos/internal/store"
	"github.com/Amrzxk/nephos/internal/tunnel/console"
	"github.com/Amrzxk/nephos/internal/version"
)

func TestConsoleRealShutdownRetainsStoppedRoots(t *testing.T) {
	if os.Getenv("NEPHOS_IN_APPLIANCE") != "1" {
		t.Skip("requires isolated privileged appliance, private Podman service and development AMI")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	resources := service.NewNetwork(s, nil, nil)
	vpc, err := resources.CreateVPC(ctx, service.CreateVPCInput{Name: "drain", CIDRBlock: "10.0.0.0/16"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var desired []model.Instance
	for i := 1; i <= 2; i++ {
		subnet, err := resources.CreateSubnet(ctx, service.CreateSubnetInput{Name: fmt.Sprint("drain", i), VPCID: vpc.ID, CIDRBlock: fmt.Sprintf("10.0.%d.0/24", i), AvailabilityZone: "local-1a"}, "")
		if err != nil {
			t.Fatal(err)
		}
		instance, err := service.NewInstances(s, nil, nil).Run(ctx, service.RunInstanceInput{Name: fmt.Sprint("drain", i), SubnetID: subnet.ID}, "")
		if err != nil {
			t.Fatal(err)
		}
		desired = append(desired, instance)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var config net.ListenConfig
	ln, err := config.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	runtimeClient := podman.New("/run/podman/podman.sock")
	engine := topology.New()
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() {
		done <- serve(runCtx, ln, filepath.Join(dir, "token"), db, "/run/nephos/hook.sock", version.Get(), engine, runtimeClient)
	}()
	url := "http://" + ln.Addr().String()
	client := newTestHTTPClient(t)
	defer client.CloseIdleConnections()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := client.Get(url + "/v1/health")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case err := <-done:
			t.Fatalf("initial recovery exited: %v", err)
		case <-ctx.Done():
			t.Fatal("real daemon never ready")
		case <-ticker.C:
		}
	}
	token, err := os.ReadFile(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	refs := make([]compute.Reference, 0, len(desired))
	var initWitnesses, cgroupWitnesses []string
	for i := range desired {
		desired[i], err = s.GetInstance(ctx, "default", desired[i].ID)
		if err != nil {
			t.Fatal(err)
		}
		if desired[i].State != model.InstanceRunning {
			t.Fatalf("instance recovery failed: %+v", desired[i])
		}
		refs = append(refs, compute.Reference{Identity: compute.Identity{WorkspaceID: desired[i].WorkspaceID, InstanceID: desired[i].ID}, ID: compute.RuntimeID(desired[i].RuntimeID)})
		status, err := runtimeClient.Inspect(ctx, refs[len(refs)-1])
		if err != nil || !status.Running || status.PID <= 0 {
			t.Fatalf("running init witness: %+v %v", status, err)
		}
		const procRoot = "/proc"
		initWitnesses = append(initWitnesses, filepath.Join(procRoot, strconv.Itoa(status.PID), "stat"))
		raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(status.PID), "cgroup"))
		if err != nil {
			t.Fatal(err)
		}
		path, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "0::/")
		if !ok || !strings.Contains(path, "libpod-"+string(refs[len(refs)-1].ID)) {
			t.Fatalf("unexpected owned init cgroup: %q", raw)
		}
		const cgroupRoot = "/sys/fs/cgroup"
		cgroupWitnesses = append(cgroupWitnesses, filepath.Join(cgroupRoot, path, "cgroup.procs"))
	}
	defer func() {
		stop()
		cleanup, finish := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer finish()
		for i := range refs {
			if err := runtimeClient.Delete(cleanup, refs[i]); err != nil {
				t.Error(err)
			}
		}
		if err := engine.DeleteVPC(cleanup, vpc); err != nil {
			t.Error(err)
		}
	}()
	// Three sessions on one instance: idle TTY, idle non-TTY, and an output
	// producer whose client deliberately stops reading after the first chunk.
	var connections []*websocket.Conn
	defer func() {
		for _, conn := range connections {
			_ = conn.CloseNow()
		}
	}()
	for _, mode := range []string{"tty", "pipe", "blocked-output"} {
		conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(url, "http")+"/v1/workspaces/default/instances/"+desired[0].ID+"/console", &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": {"Bearer " + strings.TrimSpace(string(token))}}, Subprotocols: []string{console.Subprotocol}})
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
		command := []string{"/bin/sh", "-c", "printf ready; exec sleep infinity"}
		if mode == "blocked-output" {
			command = []string{"/bin/sh", "-c", "while :; do head -c 65536 /dev/zero; done"}
		}
		request := struct {
			Type    string   `json:"type"`
			Command []string `json:"command"`
			TTY     bool     `json:"tty"`
			Rows    uint16   `json:"rows,omitempty"`
			Cols    uint16   `json:"cols,omitempty"`
		}{Type: "start", Command: command, TTY: mode == "tty"}
		if request.TTY {
			request.Rows, request.Cols = 24, 80
		}
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
			t.Fatal(err)
		}
		kind, _, err := conn.Read(ctx)
		if err != nil || kind != websocket.MessageBinary {
			t.Fatalf("%s did not attach to real running command: %v kind=%v", mode, err, kind)
		}
		if mode == "tty" {
			if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","rows":40,"cols":100}`)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for {
		raw, err := exec.CommandContext(ctx, "podman", "inspect", "--format", "{{len .ExecIDs}}", string(refs[0].ID)).Output()
		if err != nil {
			t.Fatalf("independent exec inventory: %v", err)
		}
		count, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		if count == 3 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("expected three live execs, got %d", count)
		case <-ticker.C:
		}
	}
	supervisors := ownedConmons(t, refs)
	if len(supervisors) < 5 {
		t.Fatalf("independent witness missed live instance/exec supervisors: %v", supervisors)
	}
	client.CloseIdleConnections()
	started := time.Now()
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("real daemon shutdown did not join")
	}
	if elapsed := time.Since(started); elapsed > daemonShutdownBudget {
		t.Fatalf("shutdown exceeded shared budget: %s", elapsed)
	}
	// This proof is collected BEFORE outer Docker stop/removal. Podman must
	// still answer and retain the exact roots, with every instance stopped.
	if err := runtimeClient.EnsureImage(ctx, "nephos-ubuntu:dev"); err != nil {
		t.Fatalf("runtime stopped before evidence: %v", err)
	}
	for i := range refs {
		ref, err := runtimeClient.Lookup(ctx, refs[i].Identity)
		if err != nil || ref != refs[i] {
			t.Fatalf("retained root changed: ref=%+v err=%v", ref, err)
		}
		status, err := runtimeClient.Inspect(ctx, ref)
		if err != nil || status.Running || status.PID != 0 {
			t.Fatalf("instance not fully stopped: %+v %v", status, err)
		}
		raw, err := exec.CommandContext(ctx, "podman", "inspect", "--format", "{{len .ExecIDs}}", string(ref.ID)).Output()
		if err != nil || strings.TrimSpace(string(raw)) != "0" {
			t.Fatalf("owned execs remained before outer teardown: %q %v", raw, err)
		}
		t.Logf("retained root %s (%s), stopped PID=0, exec inventory=0; Podman still alive", ref.Identity.InstanceID, ref.ID)
	}
	if supervisors := ownedConmons(t, refs); len(supervisors) != 0 {
		t.Fatalf("owned supervisors remained before outer teardown: %v", supervisors)
	}
	// Podman reports a container stopped while its conmon may still be in
	// the last moments of exit (state D, already reparented to PID 1). Give
	// reaping a bounded window; a supervisor that never goes away still fails.
	reapDeadline := time.Now().Add(10 * time.Second)
	for _, witness := range append(supervisors, initWitnesses...) {
		for {
			raw, err := os.ReadFile(witness)
			if os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) {
				break
			}
			if time.Now().After(reapDeadline) {
				t.Fatalf("supervisor not reaped within 10s (%s): %q %v", witness, raw, err)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	for _, witness := range cgroupWitnesses {
		raw, err := os.ReadFile(witness)
		if !os.IsNotExist(err) && (err != nil || strings.TrimSpace(string(raw)) != "") {
			t.Fatalf("owned cgroup remains populated (%s): %q %v", witness, raw, err)
		}
	}
	assertNoApplianceZombies(t)
}

// This deliberately does not reuse the adapter's cleanup matcher. It observes
// every conmon belonging to either full container ID, including exec helpers
// whose API entries were removed. The private test appliance's /proc is the
// witness, not Docker stop, a returned handler, or an empty runtime inventory.
func ownedConmons(t *testing.T, refs []compute.Reference) []string {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		const procRoot = "/proc"
		raw, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "cmdline"))
		if os.IsNotExist(err) {
			continue // An exited process is not residue.
		}
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if len(args) == 0 || filepath.Base(args[0]) != "conmon" {
			continue
		}
		for i := 1; i+1 < len(args); i++ {
			if args[i] != "-c" {
				continue
			}
			for j := range refs {
				if args[i+1] == string(refs[j].ID) {
					found = append(found, filepath.Join(procRoot, entry.Name(), "stat"))
				}
			}
		}
	}
	return found
}

func assertNoApplianceZombies(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		const procRoot = "/proc"
		raw, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "stat"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		end := strings.LastIndex(string(raw), ") ")
		if end < 0 {
			t.Fatalf("malformed stat for %s", entry.Name())
		}
		fields := strings.Fields(string(raw[end+2:]))
		if len(fields) == 0 || fields[0] == "Z" {
			t.Fatalf("unreaped process before outer teardown: %s", raw)
		}
	}
}
