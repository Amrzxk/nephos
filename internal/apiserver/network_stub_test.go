package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Amrzxk/nephos/internal/version"
)

// This guard is replaced by behavior tests when slice 2 wires the resource
// services. An unwired generated route must never claim success.
func TestUnwiredM1RoutesFailExplicitly(t *testing.T) {
	h := New("secret", version.Get(), func() bool { return true })
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/events"},
		{http.MethodGet, "/v1/workspaces/default/vpcs"},
		{http.MethodPost, "/v1/workspaces/default/vpcs"},
		{http.MethodGet, "/v1/workspaces/default/vpcs/vpc-0123456789abcdef0"},
		{http.MethodDelete, "/v1/workspaces/default/vpcs/vpc-0123456789abcdef0"},
		{http.MethodGet, "/v1/workspaces/default/subnets"},
		{http.MethodPost, "/v1/workspaces/default/subnets"},
		{http.MethodGet, "/v1/workspaces/default/subnets/subnet-0123456789abcdef0"},
		{http.MethodDelete, "/v1/workspaces/default/subnets/subnet-0123456789abcdef0"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, http.NoBody)
			req.Host = "localhost:7788"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated status=%d", rec.Code)
			}

			req.Header.Set("Authorization", "Bearer secret")
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotImplemented {
				t.Fatalf("unwired route status=%d body=%s", rec.Code, rec.Body.String())
			}
			var body struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != "NotImplemented" || body.Message == "" {
				t.Fatalf("unwired route body=%+v", body)
			}
		})
	}
}
