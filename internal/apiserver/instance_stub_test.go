package apiserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Amrzxk/nephos/internal/version"
)

func TestInstanceContractStubsAreExplicitAndProtected(t *testing.T) {
	h := New("secret", version.Get(), func() bool { return true }, nil, nil)
	for _, tc := range []struct{ method, suffix string }{
		{http.MethodPost, ""}, {http.MethodGet, ""},
		{http.MethodGet, "/i-test"}, {http.MethodDelete, "/i-test"},
		{http.MethodGet, "/i-test/console"},
	} {
		t.Run(tc.method+tc.suffix, func(t *testing.T) {
			path := "/v1/workspaces/default/instances" + tc.suffix
			req := httptest.NewRequest(tc.method, path, http.NoBody)
			req.Host = "localhost:7788"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != 401 || readObject(t, rec)["code"] != "AuthFailure" {
				t.Fatalf("unauthorized: %d %s", rec.Code, rec.Body.String())
			}
			rec = apiRequest(h, tc.method, path, "", "")
			if rec.Code != 501 || readObject(t, rec)["code"] != "NotImplemented" {
				t.Fatalf("stub: %d %s", rec.Code, rec.Body.String())
			}
			rec = apiRequest(h, tc.method, "/v1/workspaces/other/instances"+tc.suffix, "", "")
			if rec.Code != 404 || readObject(t, rec)["code"] != "InvalidWorkspace.NotFound" {
				t.Fatalf("workspace: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
