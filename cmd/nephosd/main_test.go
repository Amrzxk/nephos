package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/compute/podman"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
	"github.com/Amrzxk/nephos/internal/service"
	"github.com/Amrzxk/nephos/internal/store"
	"github.com/Amrzxk/nephos/internal/version"
)

type blockingNetwork struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingNetwork) EnsureVPC(ctx context.Context, _ model.VPC, _ []model.Subnet) error {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (b *blockingNetwork) DeleteVPC(context.Context, model.VPC) error { return nil }
func (b *blockingNetwork) EnsureENI(context.Context, model.VPC, model.Subnet, model.ENI, *netns.InstanceTarget) error {
	return nil
}
func (b *blockingNetwork) DeleteENI(context.Context, model.VPC, model.ENI) error { return nil }
func (b *blockingNetwork) ListVPCNames(context.Context) ([]string, error)        { return nil, nil }

type blockingInstanceRuntime struct {
	entered chan struct{}
	release chan struct{}
}

func (r *blockingInstanceRuntime) EnsureImage(context.Context, string) error { return nil }
func (r *blockingInstanceRuntime) Create(ctx context.Context, _ model.Instance) (compute.RuntimeID, error) {
	select {
	case r.entered <- struct{}{}:
	default:
	}
	select {
	case <-r.release:
		return compute.RuntimeID(strings.Repeat("a", 64)), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (r *blockingInstanceRuntime) Start(context.Context, compute.RuntimeID) error  { return nil }
func (r *blockingInstanceRuntime) Delete(context.Context, compute.RuntimeID) error { return nil }
func (r *blockingInstanceRuntime) Inspect(context.Context, compute.RuntimeID) (compute.Status, error) {
	return compute.Status{Running: true}, nil
}
func (r *blockingInstanceRuntime) Exec(context.Context, compute.RuntimeID, compute.ExecRequest) (compute.ExecSession, error) {
	return nil, nil
}

func TestServeWaitsForInitialInstanceSweep(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	network := service.NewNetwork(s, nil, nil)
	vpc, err := network.CreateVPC(ctx, service.CreateVPCInput{Name: "vpc", CIDRBlock: "10.0.0.0/16"}, "")
	if err != nil {
		t.Fatal(err)
	}
	subnet, err := network.CreateSubnet(ctx, service.CreateSubnetInput{Name: "subnet", VPCID: vpc.ID, CIDRBlock: "10.0.1.0/24", AvailabilityZone: "local-1a"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewInstances(s, nil, nil).Run(ctx, service.RunInstanceInput{Name: "first", SubnetID: subnet.ID}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var config net.ListenConfig
	ln, err := config.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	engine := &blockingNetwork{entered: make(chan struct{}, 1), release: make(chan struct{})}
	close(engine.release)
	runtime := &blockingInstanceRuntime{entered: make(chan struct{}, 1), release: make(chan struct{})}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serve(runCtx, ln, filepath.Join(t.TempDir(), "token"), dbPath, filepath.Join(t.TempDir(), "hook.sock"), version.Get(), engine, runtime)
	}()
	select {
	case <-runtime.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("initial instance sweep did not start")
	}
	url := "http://" + ln.Addr().String() + "/v1/health"
	client := newTestHTTPClient(t)
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("health before instance sweep=%d", response.StatusCode)
	}
	close(runtime.release)
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, err = client.Get(url)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not become ready after instance sweep")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeReportsStartingUntilInitialSweepCompletes(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state", "nephos.db")
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewNetwork(s, nil, nil).CreateVPC(ctx, service.CreateVPCInput{
		Name: "Lab", CIDRBlock: "10.0.0.0/16",
	}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var config net.ListenConfig
	ln, err := config.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	engine := &blockingNetwork{entered: make(chan struct{}, 1), release: make(chan struct{})}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serve(runCtx, ln, filepath.Join(t.TempDir(), "api-token"), dbPath, filepath.Join(t.TempDir(), "hook.sock"), version.Get(), engine, podman.New(filepath.Join(t.TempDir(), "podman.sock")))
	}()
	select {
	case <-engine.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("initial sweep did not start")
	}
	url := "http://" + ln.Addr().String() + "/v1/health"
	client := newTestHTTPClient(t)
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("health before sweep=%d", response.StatusCode)
	}
	close(engine.release)
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, err = client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("health never became ready: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	// Transport may have made an unused speculative dial. Close the test's
	// connections before cancellation rather than wait for the server's
	// five-second grace period for a connection with no first request.
	client.CloseIdleConnections()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
}

func TestServeCreatesTokenAndShutsDown(t *testing.T) {
	var listenConfig net.ListenConfig
	ln, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "secrets", "api-token")
	dbPath := filepath.Join(t.TempDir(), "state.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	engine := &blockingNetwork{entered: make(chan struct{}, 1), release: make(chan struct{})}
	go func() {
		done <- serve(ctx, ln, path, dbPath, filepath.Join(t.TempDir(), "hook.sock"), version.Get(), engine, podman.New(filepath.Join(t.TempDir(), "podman.sock")))
	}()

	client := newTestHTTPClient(t)
	url := "http://" + ln.Addr().String() + "/v1/health"
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("health never became ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if data, err := os.ReadFile(path); err != nil || len(data) < 43 {
		t.Fatalf("token not created: length=%d err=%v", len(data), err)
	}
	client.CloseIdleConnections()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not stop on cancellation")
	}
}

func newTestHTTPClient(t *testing.T) *http.Client {
	t.Helper()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: time.Second}
}
