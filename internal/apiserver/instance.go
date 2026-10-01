package apiserver

import (
	"net/http"

	"github.com/Amrzxk/nephos/internal/apiserver/generated"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/service"
)

func (s *server) checkInstances(w http.ResponseWriter) bool {
	if s.instances != nil {
		return true
	}
	writeAPIError(w, http.StatusServiceUnavailable, "ServiceUnavailable", "instance service is not ready", "")
	return false
}

func instanceResponse(instance model.Instance) generated.Instance {
	return generated.Instance{Id: instance.ID, Name: instance.Name, SubnetId: instance.SubnetID,
		PrivateIp: instance.ENI.PrivateIP.String(), EniId: instance.ENI.ID,
		InstanceType: generated.InstanceInstanceType(instance.InstanceType), State: generated.InstanceState(instance.State),
		StateReason: instance.StateReason, Generation: instance.Generation, ObservedGeneration: instance.ObservedGeneration}
}

func (s *server) RunInstance(w http.ResponseWriter, r *http.Request, workspace generated.Workspace, params generated.RunInstanceParams) {
	if !checkWorkspace(w, workspace) || !s.checkInstances(w) {
		return
	}
	var body generated.RunInstanceRequest
	if !decodeBody(w, r, &body) || !required(w, body.Name, body.SubnetId) {
		return
	}
	key := ""
	if params.IdempotencyKey != nil {
		key = *params.IdempotencyKey
	}
	instance, err := s.instances.Run(r.Context(), service.RunInstanceInput{Name: body.Name, SubnetID: body.SubnetId}, key)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, instanceResponse(instance))
}

func (s *server) ListInstances(w http.ResponseWriter, r *http.Request, workspace generated.Workspace, params generated.ListInstancesParams) {
	if !checkWorkspace(w, workspace) || !s.checkInstances(w) {
		return
	}
	limit, token := 0, ""
	if params.Limit != nil {
		limit = *params.Limit
	}
	if params.PageToken != nil {
		token = *params.PageToken
	}
	page, err := s.instances.List(r.Context(), limit, token)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response := generated.InstancesPage{Items: make([]generated.Instance, 0, len(page.Items))}
	for i := range page.Items {
		response.Items = append(response.Items, instanceResponse(page.Items[i]))
	}
	if page.NextPageToken != "" {
		response.NextPageToken = &page.NextPageToken
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *server) GetInstance(w http.ResponseWriter, r *http.Request, workspace generated.Workspace, id generated.ResourceID) {
	if !checkWorkspace(w, workspace) || !s.checkInstances(w) {
		return
	}
	instance, err := s.instances.Get(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, instanceResponse(instance))
}

func (s *server) TerminateInstance(w http.ResponseWriter, r *http.Request, workspace generated.Workspace, id generated.ResourceID) {
	if !checkWorkspace(w, workspace) || !s.checkInstances(w) {
		return
	}
	instance, err := s.instances.TerminateSnapshot(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, instanceResponse(instance))
}
