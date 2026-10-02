package apiserver

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/service"
	"github.com/Amrzxk/nephos/internal/store"
	"github.com/Amrzxk/nephos/internal/version"
)

func instanceAPI(t *testing.T) (http.Handler, *store.Store, string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	network := service.NewNetwork(s, nil, nil)
	vpc, err := network.CreateVPC(ctx, service.CreateVPCInput{Name: "vpc", CIDRBlock: "10.0.0.0/16"}, "")
	if err != nil {
		t.Fatal(err)
	}
	subnet, err := network.CreateSubnet(ctx, service.CreateSubnetInput{Name: "subnet", VPCID: vpc.ID, CIDRBlock: "10.0.1.0/24", AvailabilityZone: "local-1a"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return New("secret", version.Get(), func() bool { return true }, network, service.NewInstances(s, nil, nil), s, nil), s, subnet.ID
}

func TestInstanceAPI(t *testing.T) {
	h, _, subnetID := instanceAPI(t)
	base := "/v1/workspaces/default/instances"
	unauthorized := httptest.NewRequest(http.MethodGet, base, http.NoBody)
	unauthorized.Host = "localhost:7788"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, unauthorized)
	if rec.Code != http.StatusUnauthorized || readObject(t, rec)["code"] != "AuthFailure" {
		t.Fatalf("unauthorized instance route: %d %s", rec.Code, rec.Body.String())
	}
	rec = apiRequest(h, http.MethodGet, "/v1/workspaces/other/instances", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown workspace: %d %s", rec.Code, rec.Body.String())
	}
	rec = apiRequest(h, http.MethodGet, base+"/i-00000000000000000", "", "")
	if rec.Code != http.StatusNotFound || readObject(t, rec)["code"] != "InvalidInstanceID.NotFound" {
		t.Fatalf("unknown instance: %d %s", rec.Code, rec.Body.String())
	}
	bad := `{"name":"first","subnet_id":"` + subnetID + `","extra":true}`
	rec = apiRequest(h, http.MethodPost, base, bad, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("extra JSON accepted: %d %s", rec.Code, rec.Body.String())
	}
	body := `{"name":"first","subnet_id":"` + subnetID + `"}`
	rec = apiRequest(h, http.MethodPost, base, body, "same-key")
	if rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	first := readObject(t, rec)
	id, _ := first["id"].(string)
	if !strings.HasPrefix(id, "i-") || first["state"] != "pending" || first["private_ip"] != "10.0.1.4" || first["eni_id"] == "" {
		t.Fatalf("transitional response: %v", first)
	}
	rec = apiRequest(h, http.MethodPost, base, body, "same-key")
	if rec.Code != http.StatusCreated || readObject(t, rec)["id"] != id {
		t.Fatalf("keyed replay: %d %s", rec.Code, rec.Body.String())
	}
	rec = apiRequest(h, http.MethodPost, base, `{"name":"different","subnet_id":"`+subnetID+`"}`, "same-key")
	if rec.Code != http.StatusConflict {
		t.Fatalf("key conflict: %d %s", rec.Code, rec.Body.String())
	}
	rec = apiRequest(h, http.MethodGet, base+"/"+id, "", "")
	if rec.Code != http.StatusOK || readObject(t, rec)["id"] != id {
		t.Fatalf("get instance: %d %s", rec.Code, rec.Body.String())
	}
	rec = apiRequest(h, http.MethodPost, base, `{"name":"second","subnet_id":"`+subnetID+`"}`, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("second run: %d %s", rec.Code, rec.Body.String())
	}
	rec = apiRequest(h, http.MethodGet, base+"?limit=1", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	page := readObject(t, rec)
	items, _ := page["items"].([]any)
	token, _ := page["next_page_token"].(string)
	if len(items) != 1 || token == "" {
		t.Fatalf("first page: %v", page)
	}
	rec = apiRequest(h, http.MethodGet, base+"?limit=1&page_token="+token, "", "")
	if rec.Code != http.StatusOK || len(readObject(t, rec)["items"].([]any)) != 1 {
		t.Fatalf("second page: %d %s", rec.Code, rec.Body.String())
	}
	rec = apiRequest(h, http.MethodDelete, base+"/"+id, "", "")
	if rec.Code != http.StatusAccepted || readObject(t, rec)["state"] != "shutting-down" {
		t.Fatalf("terminate: %d %s", rec.Code, rec.Body.String())
	}
}

func TestInstanceEvents(t *testing.T) {
	h, _, subnetID := instanceAPI(t)
	body := `{"name":"first","subnet_id":"` + subnetID + `"}`
	rec := apiRequest(h, http.MethodPost, "/v1/workspaces/default/instances", body, "")
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	id := readObject(t, rec)["id"].(string)
	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/events", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	response, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	found := false
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var event struct {
			ResourceID   string `json:"resource_id"`
			ResourceType string `json:"resource_type"`
			State        string `json:"state"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.ResourceID == id && event.ResourceType == "instance" && event.State == "pending" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("instance create absent from authenticated SSE: %v", scanner.Err())
	}
}
