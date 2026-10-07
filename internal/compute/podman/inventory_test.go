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
)

// Omitting full inspection, filtering by one label, or accepting a truncated
// list would hide ambiguous owners and incorrectly authorize later cleanup.
func TestRuntimeOrphanInventory(t *testing.T) {
	otherID := strings.Repeat("b", 64)
	foreignID := strings.Repeat("c", 64)
	var mutations atomic.Int32
	client := New(unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations.Add(1)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == apiPath+"/containers/json" {
			if r.URL.Query().Get("all") != "true" || r.URL.Query().Get("filters") != "" {
				t.Errorf("inventory must include all containers: %s", r.URL)
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": testRuntimeID}, {"Id": otherID}, {"Id": foreignID}})
			return
		}
		doc := ownedInspect()
		switch r.URL.Path {
		case apiPath + "/containers/" + string(testRuntimeID) + "/json":
		case apiPath + "/containers/" + otherID + "/json":
			doc["Id"] = otherID
			doc["Name"] = "foreign-name"
			doc["Config"] = map[string]any{"Labels": map[string]string{}, "Annotations": map[string]string{instanceLabel: testID}}
		case apiPath + "/containers/" + foreignID + "/json":
			doc["Id"] = foreignID
			doc["Name"] = "unrelated"
			doc["Config"] = map[string]any{"Labels": map[string]string{}, "Annotations": map[string]string{}}
		default:
			t.Errorf("unexpected inventory request: %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(doc)
	})))
	// The behavioral RED is an absent inventory operation, not a compiler error.
	inventory, ok := any(client).(interface {
		Inventory(context.Context) ([]compute.ObservedContainer, error)
	})
	if !ok {
		t.Fatal("runtime cannot observe rowless or malformed marked containers")
	}
	items, err := inventory.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || !items[0].Managed || items[0].Reference != testReference || items[0].OwnershipError != "" ||
		items[1].Managed || items[1].Reference.ID != compute.RuntimeID(otherID) || items[1].OwnershipError == "" {
		t.Fatalf("lost strict ownership or broad candidates: %+v", items)
	}
	if mutations.Load() != 0 {
		t.Fatal("inventory mutated runtime")
	}
}

func TestRuntimeOrphanInventoryRequiresCompleteInspection(t *testing.T) {
	for _, tc := range []struct {
		name, list string
		status     int
		changedID  bool
	}{
		{name: "denied list", list: "[]", status: 403},
		{name: "null list", list: "null", status: 200},
		{name: "invalid ID", list: `[{"Id":"short"}]`, status: 200},
		{name: "duplicate ID", list: `[{"Id":"` + string(testRuntimeID) + `"},{"Id":"` + string(testRuntimeID) + `"}]`, status: 200},
		{name: "disappeared during scan", list: `[{"Id":"` + string(testRuntimeID) + `"}]`, status: 404},
		{name: "changed full ID", list: `[{"Id":"` + string(testRuntimeID) + `"}]`, status: 200, changedID: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := New(unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == apiPath+"/containers/json" {
					if tc.name == "denied list" {
						w.WriteHeader(tc.status)
					}
					_, _ = w.Write([]byte(tc.list))
					return
				}
				w.WriteHeader(tc.status)
				doc := ownedInspect()
				if tc.changedID {
					doc["Id"] = strings.Repeat("b", 64)
				}
				_ = json.NewEncoder(w).Encode(doc)
			})))
			inventory, ok := any(client).(interface {
				Inventory(context.Context) ([]compute.ObservedContainer, error)
			})
			if !ok {
				t.Fatal("runtime cannot produce complete inventory")
			}
			items, err := inventory.Inventory(context.Background())
			if !errors.Is(err, compute.ErrInspectionUnavailable) || items != nil {
				t.Fatalf("incomplete inspection became usable inventory: %+v %v", items, err)
			}
		})
	}
}

func TestAmbiguousOwnershipInventory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{name: "reserved name without markers", change: func(doc map[string]any) {
			doc["Config"] = map[string]any{}
		}},
		{name: "false managed marker", change: func(doc map[string]any) {
			doc["Config"].(map[string]any)["Labels"].(map[string]string)["io.nephos.managed"] = "false"
		}},
		{name: "contradictory annotation", change: func(doc map[string]any) {
			doc["Config"].(map[string]any)["Annotations"].(map[string]string)[instanceLabel] = "i-00000000000000002"
		}},
		{name: "list marker lost in inspect", change: func(doc map[string]any) {
			doc["Name"] = "unrelated"
			doc["Config"] = map[string]any{}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := New(unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == apiPath+"/containers/json" {
					_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": testRuntimeID, "Names": []string{testID}, "Labels": map[string]string{"io.nephos.managed": "true"}}})
					return
				}
				doc := ownedInspect()
				tc.change(doc)
				_ = json.NewEncoder(w).Encode(doc)
			})))
			items, err := client.Inventory(context.Background())
			if err != nil || len(items) != 1 || items[0].Managed || items[0].OwnershipError == "" {
				t.Fatalf("ambiguous marked object hidden or authorized: %+v %v", items, err)
			}
		})
	}
}
