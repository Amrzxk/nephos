package apiserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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

func resourceAPI(t *testing.T) http.Handler {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return New("secret", version.Get(), func() bool { return true }, service.NewNetwork(s, nil, nil), nil, s, nil)
}

func apiRequest(h http.Handler, method, path, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "localhost:7788"
	req.Header.Set("Authorization", "Bearer secret")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func readObject(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response %d: %v (%q)", rec.Code, err, rec.Body.String())
	}
	return body
}

func TestResourceRoutesRequireBearerAndDefaultWorkspace(t *testing.T) {
	h := resourceAPI(t)
	for _, path := range []string{"/v1/workspaces/default/vpcs", "/v1/workspaces/default/subnets", "/v1/events"} {
		req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		req.Host = "localhost:7788"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 || readObject(t, rec)["code"] != "AuthFailure" {
			t.Fatalf("unauthorized %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	rec := apiRequest(h, http.MethodGet, "/v1/workspaces/other/vpcs", "", "")
	if rec.Code != 404 || readObject(t, rec)["code"] != "InvalidWorkspace.NotFound" {
		t.Fatalf("unknown workspace: %d %s", rec.Code, rec.Body.String())
	}
}

func TestVpcAndSubnetCRUDValidationPaginationAndIdempotency(t *testing.T) {
	h := resourceAPI(t)
	base := "/v1/workspaces/default"
	for _, body := range []string{
		`{"name":"East","cidr_block":"10.0.0.0/16","extra":true}`,
		`{"name":"East","cidr_block":"10.0.0.0/16"} {}`,
	} {
		rec := apiRequest(h, http.MethodPost, base+"/vpcs", body, "")
		if rec.Code != 400 || readObject(t, rec)["code"] != "InvalidParameterValue" {
			t.Fatalf("unknown/trailing JSON: %d %s", rec.Code, rec.Body.String())
		}
	}
	first := apiRequest(h, http.MethodPost, base+"/vpcs", `{"name":"Lab East","cidr_block":"10.0.0.0/16"}`, "same-key")
	if first.Code != 201 {
		t.Fatalf("VPC create: %d %s", first.Code, first.Body.String())
	}
	firstBody := readObject(t, first)
	id, _ := firstBody["id"].(string)
	if !strings.HasPrefix(id, "vpc-") || firstBody["state"] != "pending" || firstBody["generation"] != float64(1) {
		t.Fatalf("created VPC=%v", firstBody)
	}
	replay := apiRequest(h, http.MethodPost, base+"/vpcs", `{"name":"Lab East","cidr_block":"10.0.1.1/16"}`, "same-key")
	if replay.Code != 201 || readObject(t, replay)["id"] != id {
		t.Fatalf("idempotent replay: %d %s", replay.Code, replay.Body.String())
	}
	mismatch := apiRequest(h, http.MethodPost, base+"/vpcs", `{"name":"Changed","cidr_block":"10.0.0.0/16"}`, "same-key")
	if mismatch.Code != 409 || readObject(t, mismatch)["code"] != "IdempotentParameterMismatch" {
		t.Fatalf("mismatched replay: %d %s", mismatch.Code, mismatch.Body.String())
	}
	second := apiRequest(h, http.MethodPost, base+"/vpcs", `{"name":"Lab West","cidr_block":"10.0.0.0/16"}`, "")
	if second.Code != 201 {
		t.Fatalf("second VPC: %d %s", second.Code, second.Body.String())
	}
	list := apiRequest(h, http.MethodGet, base+"/vpcs?limit=1", "", "")
	if list.Code != 200 {
		t.Fatalf("list VPC: %d %s", list.Code, list.Body.String())
	}
	page := readObject(t, list)
	items, ok := page["items"].([]any)
	if !ok || len(items) != 1 || page["next_page_token"] == "" {
		t.Fatalf("first page=%v", page)
	}
	token, _ := page["next_page_token"].(string)
	last := apiRequest(h, http.MethodGet, base+"/vpcs?limit=1&page_token="+token, "", "")
	if last.Code != 200 {
		t.Fatalf("last page: %d %s", last.Code, last.Body.String())
	}
	lastPage := readObject(t, last)
	lastItems, ok := lastPage["items"].([]any)
	if !ok || len(lastItems) != 1 || lastItems[0].(map[string]any)["id"] == items[0].(map[string]any)["id"] {
		t.Fatalf("last page=%v", lastPage)
	}
	if rec := apiRequest(h, http.MethodGet, base+"/vpcs/vpc-missing", "", ""); rec.Code != 404 {
		t.Fatalf("missing VPC: %d", rec.Code)
	}
	subnetReq := fmt.Sprintf(`{"name":"App α","vpc_id":%q,"cidr_block":"10.0.1.0/24","availability_zone":"local-1a"}`, id)
	subnet := apiRequest(h, http.MethodPost, base+"/subnets", subnetReq, "")
	if subnet.Code != 201 {
		t.Fatalf("subnet create: %d %s", subnet.Code, subnet.Body.String())
	}
	subnetID, _ := readObject(t, subnet)["id"].(string)
	if rec := apiRequest(h, http.MethodGet, base+"/subnets/"+subnetID, "", ""); rec.Code != 200 {
		t.Fatalf("subnet GET: %d", rec.Code)
	}
	if rec := apiRequest(h, http.MethodGet, base+"/subnets/subnet-missing", "", ""); rec.Code != 404 {
		t.Fatalf("missing subnet: %d", rec.Code)
	}
	blocked := apiRequest(h, http.MethodDelete, base+"/vpcs/"+id, "", "")
	if blocked.Code != 409 || readObject(t, blocked)["code"] != "DependencyViolation" {
		t.Fatalf("dependent VPC delete: %d %s", blocked.Code, blocked.Body.String())
	}
	deleting := apiRequest(h, http.MethodDelete, base+"/subnets/"+subnetID, "", "")
	if deleting.Code != 202 || readObject(t, deleting)["state"] != "deleting" {
		t.Fatalf("subnet delete: %d %s", deleting.Code, deleting.Body.String())
	}
}

func TestEventsResumeAfterLastID(t *testing.T) {
	h := resourceAPI(t)
	badReq := httptest.NewRequest(http.MethodGet, "/v1/events", http.NoBody)
	badReq.Host = "localhost:7788"
	badReq.Header.Set("Authorization", "Bearer secret")
	badReq.Header.Set("Last-Event-ID", "-1")
	badRec := httptest.NewRecorder()
	h.ServeHTTP(badRec, badReq)
	if badRec.Code != 400 || readObject(t, badRec)["code"] != "InvalidParameterValue" {
		t.Fatalf("bad event cursor: %d %s", badRec.Code, badRec.Body.String())
	}
	for _, name := range []string{"East", "West"} {
		body := fmt.Sprintf(`{"name":%q,"cidr_block":"10.0.0.0/16"}`, name)
		if rec := apiRequest(h, http.MethodPost, "/v1/workspaces/default/vpcs", body, ""); rec.Code != 201 {
			t.Fatalf("create event: %d %s", rec.Code, rec.Body.String())
		}
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	readFirst := func(lastID string) (string, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/events", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer secret")
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("event response=%d", resp.StatusCode)
		}
		scanner := bufio.NewScanner(resp.Body)
		if !scanner.Scan() {
			t.Fatalf("event ID missing: %v", scanner.Err())
		}
		id := strings.TrimPrefix(scanner.Text(), "id: ")
		if !scanner.Scan() {
			t.Fatalf("event data missing: %v", scanner.Err())
		}
		data := strings.TrimPrefix(scanner.Text(), "data: ")
		return id, data
	}
	firstID, firstData := readFirst("")
	secondID, secondData := readFirst(firstID)
	if firstID != "1" || secondID != "2" || !strings.Contains(firstData, `"resource_id"`) || firstData == secondData {
		t.Fatalf("resume first=%q %q second=%q %q", firstID, firstData, secondID, secondData)
	}
}
