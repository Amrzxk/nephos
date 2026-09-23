package apiserver

import (
	"net/http"

	"github.com/Amrzxk/nephos/internal/apiserver/generated"
)

// These handlers keep the spec-first slice honest until the store and service
// path is wired. Slice 2 replaces them with real implementations.
func unimplemented(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotImplemented, generated.Error{
		Code:    "NotImplemented",
		Message: "resource API is not yet implemented",
	})
}

func (s *server) GetEvents(w http.ResponseWriter, _ *http.Request, _ generated.GetEventsParams) {
	unimplemented(w)
}

func (s *server) ListVpcs(w http.ResponseWriter, _ *http.Request, _ generated.Workspace, _ generated.ListVpcsParams) {
	unimplemented(w)
}

func (s *server) CreateVpc(w http.ResponseWriter, _ *http.Request, _ generated.Workspace, _ generated.CreateVpcParams) {
	unimplemented(w)
}

func (s *server) GetVpc(w http.ResponseWriter, _ *http.Request, _ generated.Workspace, _ generated.ResourceID) {
	unimplemented(w)
}

func (s *server) DeleteVpc(w http.ResponseWriter, _ *http.Request, _ generated.Workspace, _ generated.ResourceID) {
	unimplemented(w)
}

func (s *server) ListSubnets(w http.ResponseWriter, _ *http.Request, _ generated.Workspace, _ generated.ListSubnetsParams) {
	unimplemented(w)
}

func (s *server) CreateSubnet(w http.ResponseWriter, _ *http.Request, _ generated.Workspace, _ generated.CreateSubnetParams) {
	unimplemented(w)
}

func (s *server) GetSubnet(w http.ResponseWriter, _ *http.Request, _ generated.Workspace, _ generated.ResourceID) {
	unimplemented(w)
}

func (s *server) DeleteSubnet(w http.ResponseWriter, _ *http.Request, _ generated.Workspace, _ generated.ResourceID) {
	unimplemented(w)
}
