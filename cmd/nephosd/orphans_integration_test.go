//go:build linux && integration

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/compute/podman"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/topology"
	"github.com/Amrzxk/nephos/internal/version"
)

func TestServeCollectsRowlessRootBeforeReadyReal(t *testing.T) {
	if os.Getenv("NEPHOS_IN_APPLIANCE") != "1" {
		t.Skip("requires isolated privileged appliance and private Podman service")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runtime := podman.New("/run/podman/podman.sock")
	instance := model.Instance{ID: fmt.Sprintf("i-%017x", time.Now().UnixNano()), WorkspaceID: "default", InstanceType: "t3.micro"}
	created, err := runtime.Create(ctx, instance)
	if err != nil || !created.Created {
		t.Fatalf("seed exact rowless fixture: %+v %v", created, err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		if err := runtime.Delete(cleanup, created.Reference); err != nil {
			t.Error(err)
		}
	})
	var config net.ListenConfig
	ln, err := config.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	dir := t.TempDir()
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() {
		done <- serve(runCtx, ln, filepath.Join(dir, "token"), filepath.Join(dir, "db"), filepath.Join(dir, "hook.sock"), version.Get(), topology.New(), runtime)
	}()
	client := newTestHTTPClient(t)
	defer client.CloseIdleConnections()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := client.Get("http://" + ln.Addr().String() + "/v1/health")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case err := <-done:
			t.Fatalf("daemon exited before ready: %v", err)
		case <-ctx.Done():
			t.Fatal("daemon did not complete initial GC")
		case <-ticker.C:
		}
	}
	if _, err := runtime.Lookup(ctx, created.Reference.Identity); !errors.Is(err, compute.ErrNotFound) {
		t.Fatalf("ready while rowless root retained: %v", err)
	}
	client.CloseIdleConnections()
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("daemon cleanup did not join")
	}
}
