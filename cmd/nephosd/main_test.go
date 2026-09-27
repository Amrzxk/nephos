package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/model"
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
func (b *blockingNetwork) DeleteVPC(context.Context, model.VPC) error     { return nil }
func (b *blockingNetwork) ListVPCNames(context.Context) ([]string, error) { return nil, nil }

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
		done <- serve(runCtx, ln, filepath.Join(t.TempDir(), "api-token"), dbPath, version.Get(), engine)
	}()
	select {
	case <-engine.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("initial sweep did not start")
	}
	url := "http://" + ln.Addr().String() + "/v1/health"
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("health before sweep=%d", response.StatusCode)
	}
	close(engine.release)
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
			t.Fatalf("health never became ready: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
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
	go func() { done <- serve(ctx, ln, path, dbPath, version.Get(), engine) }()

	client := &http.Client{Timeout: time.Second}
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
