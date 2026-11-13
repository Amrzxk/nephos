package podman

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Amrzxk/nephos/internal/compute"
)

func TestMutationLostReplyIsUncertain(t *testing.T) {
	c := New(unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(ownedInspect())
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("fixture cannot hijack")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		// The runtime has consumed the mutation, but its response is lost.
		_ = conn.Close()
	})))
	if err := c.Start(context.Background(), testReference); !errors.Is(err, compute.ErrUncertainEffect) {
		t.Fatalf("lost mutation reply treated as completed: %v", err)
	}
}

func TestRequiredRuntimeInspectionIsSystemic(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusNotFound, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c := New(unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = w.Write([]byte(`{malformed`))
				}
			})))
			_, err := c.Lookup(context.Background(), testReference.Identity)
			if errors.Is(err, compute.ErrInspectionUnavailable) != (status != http.StatusNotFound) {
				t.Fatalf("inspection classification: %v", err)
			}
			if status == http.StatusNotFound && !errors.Is(err, compute.ErrNotFound) {
				t.Fatal(err)
			}
		})
	}
	c := New(filepath.Join(t.TempDir(), "missing.sock"))
	if _, err := c.Lookup(context.Background(), testReference.Identity); !errors.Is(err, compute.ErrInspectionUnavailable) {
		t.Fatalf("unavailable socket allowed resource-only failure: %v", err)
	}
}

func TestMutationNotSubmittedIsNotUncertain(t *testing.T) {
	for _, canceled := range []bool{true, false} {
		t.Run(strconv.FormatBool(canceled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				cancel()
			}
			c := New(filepath.Join(t.TempDir(), "missing.sock"))
			err := c.request(ctx, http.MethodPost, "/containers/create", nil, nil)
			if err == nil || errors.Is(err, compute.ErrUncertainEffect) {
				t.Fatalf("known unsubmitted mutation retained forever: %v", err)
			}
			if canceled && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}
