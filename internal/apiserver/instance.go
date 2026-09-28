package apiserver

import (
	"net/http"

	"github.com/Amrzxk/nephos/internal/apiserver/generated"
)

// Instance operations remain explicit stubs until the instance service is wired.
func instanceNotImplemented(w http.ResponseWriter, workspace generated.Workspace) {
	if !checkWorkspace(w, workspace) {
		return
	}
	writeAPIError(w, http.StatusNotImplemented, "NotImplemented", "Instance support is not yet implemented.", "")
}

func (s *server) RunInstance(w http.ResponseWriter, _ *http.Request, workspace generated.Workspace, _ generated.RunInstanceParams) {
	instanceNotImplemented(w, workspace)
}

func (s *server) ListInstances(w http.ResponseWriter, _ *http.Request, workspace generated.Workspace, _ generated.ListInstancesParams) {
	instanceNotImplemented(w, workspace)
}

func (s *server) GetInstance(w http.ResponseWriter, _ *http.Request, workspace generated.Workspace, _ generated.ResourceID) {
	instanceNotImplemented(w, workspace)
}

func (s *server) TerminateInstance(w http.ResponseWriter, _ *http.Request, workspace generated.Workspace, _ generated.ResourceID) {
	instanceNotImplemented(w, workspace)
}

func (s *server) GetInstanceConsole(w http.ResponseWriter, _ *http.Request, workspace generated.Workspace, _ generated.ResourceID) {
	instanceNotImplemented(w, workspace)
}
