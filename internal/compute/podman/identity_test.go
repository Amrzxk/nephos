package podman

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/model"
)

func TestRuntimeExpectedIdentity(t *testing.T) {
	cases := []struct {
		name     string
		change   func(map[string]any)
		identity compute.Identity
		reject   bool
	}{
		{name: "exact identity"},
		{name: "wrong expected workspace", reject: true, identity: compute.Identity{WorkspaceID: "other", InstanceID: testID}},
		{name: "wrong expected instance", reject: true, identity: compute.Identity{WorkspaceID: "default", InstanceID: "i-00000000000000002"}},
		{name: "another managed instance", reject: true, change: func(doc map[string]any) {
			other := "i-00000000000000002"
			doc["Name"] = other
			cfg := doc["Config"].(map[string]any)
			cfg["Labels"].(map[string]string)[instanceLabel] = other
			cfg["Annotations"].(map[string]string)[instanceLabel] = other
		}},
		{name: "wrong workspace", reject: true, change: func(doc map[string]any) {
			doc["Config"].(map[string]any)["Labels"].(map[string]string)["io.nephos.workspace-id"] = "other"
		}},
		{name: "missing managed marker", reject: true, change: func(doc map[string]any) {
			delete(doc["Config"].(map[string]any)["Labels"].(map[string]string), "io.nephos.managed")
		}},
		{name: "contradictory annotation", reject: true, change: func(doc map[string]any) {
			doc["Config"].(map[string]any)["Annotations"].(map[string]string)[instanceLabel] = "i-00000000000000002"
		}},
		{name: "name collision", reject: true, change: func(doc map[string]any) { doc["Name"] = "foreign" }},
		{name: "changed full ID", reject: true, change: func(doc map[string]any) { doc["Id"] = strings.Repeat("b", 64) }},
		{name: "short ID", reject: true, change: func(doc map[string]any) { doc["Id"] = "abc123" }},
		{name: "missing workspace", reject: true, change: func(doc map[string]any) {
			delete(doc["Config"].(map[string]any)["Labels"].(map[string]string), "io.nephos.workspace-id")
		}},
		{name: "missing instance label", reject: true, change: func(doc map[string]any) {
			delete(doc["Config"].(map[string]any)["Labels"].(map[string]string), instanceLabel)
		}},
		{name: "missing annotation", reject: true, change: func(doc map[string]any) {
			delete(doc["Config"].(map[string]any)["Annotations"].(map[string]string), instanceLabel)
		}},
	}
	for _, tc := range cases {
		for _, method := range []string{"inspect", "start", "delete", "exec"} {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				doc := ownedInspect()
				doc["State"] = map[string]any{"Running": true, "Pid": 714}
				if tc.change != nil {
					tc.change(doc)
				}
				var mutations atomic.Int32
				c := New(unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet {
						_ = json.NewEncoder(w).Encode(doc)
						return
					}
					mutations.Add(1)
					if method == "exec" {
						http.Error(w, "validated-exec", http.StatusServiceUnavailable)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				})))
				ctx := context.Background()
				ref := testReference
				if tc.identity != (compute.Identity{}) {
					ref.Identity = tc.identity
				}
				var err error
				var status compute.Status
				switch method {
				case "inspect":
					status, err = c.Inspect(ctx, ref)
				case "start":
					err = c.Start(ctx, ref)
				case "delete":
					err = c.Delete(ctx, ref)
				case "exec":
					_, err = c.Exec(ctx, ref, compute.ExecRequest{Command: []string{"/bin/true"}})
				}
				if tc.reject {
					if err == nil || mutations.Load() != 0 {
						t.Fatalf("operation for expected instance %s accepted mismatched identity: err=%v mutations=%d", testID, err, mutations.Load())
					}
					if status != (compute.Status{}) {
						t.Fatalf("unverified state escaped: %+v", status)
					}
				} else if method == "exec" {
					if err == nil || !strings.Contains(err.Error(), "validated-exec") || mutations.Load() != 1 {
						t.Fatalf("exact exec: %v mutations=%d", err, mutations.Load())
					}
				} else if err != nil {
					t.Fatal(err)
				} else if method == "inspect" && status != (compute.Status{Running: true, PID: 714}) {
					t.Fatalf("lost verified runtime status: %+v", status)
				}
			})
		}
	}
}

func TestLookupAuthoritativeAbsence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		collision bool
		missing   bool
	}{
		{name: "absent", status: http.StatusNotFound, missing: true},
		{name: "runtime unavailable", status: http.StatusServiceUnavailable},
		{name: "name collision", status: http.StatusOK, collision: true},
		{name: "retained", status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mutations atomic.Int32
			c := New(unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					mutations.Add(1)
				}
				if r.URL.Path != apiPath+"/containers/"+testID+"/json" {
					t.Errorf("lookup by unexpected name: %s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				if tc.status == http.StatusOK {
					doc := ownedInspect()
					if tc.collision {
						doc["Name"] = "i-00000000000000002"
					}
					_ = json.NewEncoder(w).Encode(doc)
				}
			})))
			ref, err := c.Lookup(context.Background(), testReference.Identity)
			if errors.Is(err, compute.ErrNotFound) != tc.missing {
				t.Fatalf("absence misclassified: ref=%+v err=%v", ref, err)
			}
			if tc.status == http.StatusOK && !tc.collision {
				if err != nil || ref != testReference {
					t.Fatalf("lookup: %+v %v", ref, err)
				}
			} else if err == nil {
				t.Fatal("lookup accepted failed inspection")
			}
			if mutations.Load() != 0 {
				t.Fatal("lookup mutated runtime")
			}
		})
	}
}

func TestCreateProvenance(t *testing.T) {
	for _, exists := range []bool{true, false} {
		t.Run(map[bool]string{true: "retained", false: "new"}[exists], func(t *testing.T) {
			var creates atomic.Int32
			c := New(unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					if !exists && creates.Load() == 0 {
						http.Error(w, "not found", http.StatusNotFound)
						return
					}
					_ = json.NewEncoder(w).Encode(ownedInspect())
					return
				}
				creates.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": testRuntimeID})
			})))
			result, err := c.Create(context.Background(), model.Instance{ID: testID, WorkspaceID: "default", InstanceType: "t3.micro"})
			if err != nil {
				t.Fatal(err)
			}
			if result.Reference != testReference || result.Created == exists {
				t.Fatalf("incorrect creation provenance: %#v", result)
			}
			if creates.Load() != map[bool]int32{true: 0, false: 1}[exists] {
				t.Fatalf("creates=%d", creates.Load())
			}
		})
	}
}
