// Package apiserver serves the spec-first Nephos HTTP API.
package apiserver

import (
	"encoding/json"
	"net/http"

	"github.com/Amrzxk/nephos/internal/apiserver/generated"
	"github.com/Amrzxk/nephos/internal/version"
)

type server struct {
	build version.Info
	ready func() bool
}

// New returns the generated API routes with M1's localhost and bearer-token
// boundary. The readiness callback allows later slices to include their
// startup reconcile sweep without changing this API.
func New(token string, build version.Info, ready func() bool) http.Handler {
	h := generated.Handler(&server{build: build, ready: ready})
	return localOnly(requireBearer(token, h))
}

func (s *server) GetHealth(w http.ResponseWriter, _ *http.Request) {
	status := generated.Starting
	code := http.StatusServiceUnavailable
	if s.ready() {
		status = generated.Ready
		code = http.StatusOK
	}
	writeJSON(w, code, generated.HealthResponse{Status: status})
}

func (s *server) GetVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, generated.VersionResponse{
		ApiVersion:   "v1",
		BuildVersion: s.build.Version,
		BuildCommit:  s.build.Commit,
	})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
