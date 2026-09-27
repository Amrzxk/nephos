package apiserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"

	"github.com/Amrzxk/nephos/internal/apiserver/generated"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/service"
)

func writeAPIError(w http.ResponseWriter, status int, code, message, resourceID string) {
	body := generated.Error{Code: code, Message: message}
	if resourceID != "" {
		body.ResourceId = &resourceID
	}
	writeJSON(w, status, body)
}

func writeServiceError(w http.ResponseWriter, err error) {
	var domain *service.Error
	if errors.As(err, &domain) {
		writeAPIError(w, domain.Status, domain.Code, domain.Message, domain.ResourceID)
		return
	}
	slog.Error("API resource operation failed", "error", err)
	writeAPIError(w, http.StatusInternalServerError, "InternalError", "resource operation failed", "")
}

func checkWorkspace(w http.ResponseWriter, workspace generated.Workspace) bool {
	if workspace == "default" {
		return true
	}
	writeAPIError(w, http.StatusNotFound, "InvalidWorkspace.NotFound", "workspace does not exist", workspace)
	return false
}

func (s *server) checkResources(w http.ResponseWriter) bool {
	if s.resources != nil {
		return true
	}
	writeAPIError(w, http.StatusServiceUnavailable, "ServiceUnavailable", "resource service is not ready", "")
	return false
}

func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAPIError(w, http.StatusBadRequest, "InvalidParameterValue", "Content-Type must be application/json", "")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeAPIError(w, http.StatusBadRequest, "InvalidParameterValue", fmt.Sprintf("invalid JSON body: %v", err), "")
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, "InvalidParameterValue", "request body must contain exactly one JSON object", "")
		return false
	}
	return true
}

func required(w http.ResponseWriter, fields ...string) bool {
	for _, field := range fields {
		if field == "" {
			writeAPIError(w, http.StatusBadRequest, "InvalidParameterValue", "required request field is empty", "")
			return false
		}
	}
	return true
}

func vpcResponse(vpc model.VPC) generated.Vpc {
	return generated.Vpc{Id: vpc.ID, Name: vpc.Name, CidrBlock: vpc.CIDRBlock.String(),
		State: generated.VpcState(vpc.State), StateReason: vpc.StateReason,
		Generation: vpc.Generation, ObservedGeneration: vpc.ObservedGeneration}
}

func subnetResponse(subnet model.Subnet) generated.Subnet {
	return generated.Subnet{Id: subnet.ID, VpcId: subnet.VPCID, Name: subnet.Name,
		CidrBlock: subnet.CIDRBlock.String(), AvailabilityZone: generated.SubnetAvailabilityZone(subnet.AvailabilityZone),
		State: generated.SubnetState(subnet.State), StateReason: subnet.StateReason,
		Generation: subnet.Generation, ObservedGeneration: subnet.ObservedGeneration}
}

func (s *server) CreateVpc(w http.ResponseWriter, r *http.Request, workspace generated.Workspace, params generated.CreateVpcParams) {
	if !checkWorkspace(w, workspace) || !s.checkResources(w) {
		return
	}
	var body generated.CreateVpcRequest
	if !decodeBody(w, r, &body) || !required(w, body.Name, body.CidrBlock) {
		return
	}
	key := ""
	if params.IdempotencyKey != nil {
		key = *params.IdempotencyKey
	}
	vpc, err := s.resources.CreateVPC(r.Context(), service.CreateVPCInput{Name: body.Name, CIDRBlock: body.CidrBlock}, key)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, vpcResponse(vpc))
}

func (s *server) ListVpcs(w http.ResponseWriter, r *http.Request, workspace generated.Workspace, params generated.ListVpcsParams) {
	if !checkWorkspace(w, workspace) || !s.checkResources(w) {
		return
	}
	limit, token := 0, ""
	if params.Limit != nil {
		limit = *params.Limit
	}
	if params.PageToken != nil {
		token = *params.PageToken
	}
	items, next, err := s.resources.ListVPCs(r.Context(), limit, token)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	page := generated.VpcsPage{Items: make([]generated.Vpc, 0, len(items))}
	for i := range items {
		page.Items = append(page.Items, vpcResponse(items[i]))
	}
	if next != "" {
		page.NextPageToken = &next
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *server) GetVpc(w http.ResponseWriter, r *http.Request, workspace generated.Workspace, id generated.ResourceID) {
	if !checkWorkspace(w, workspace) || !s.checkResources(w) {
		return
	}
	vpc, err := s.resources.GetVPC(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, vpcResponse(vpc))
}

func (s *server) DeleteVpc(w http.ResponseWriter, r *http.Request, workspace generated.Workspace, id generated.ResourceID) {
	if !checkWorkspace(w, workspace) || !s.checkResources(w) {
		return
	}
	vpc, err := s.resources.DeleteVPC(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, vpcResponse(vpc))
}

func (s *server) CreateSubnet(w http.ResponseWriter, r *http.Request, workspace generated.Workspace, params generated.CreateSubnetParams) {
	if !checkWorkspace(w, workspace) || !s.checkResources(w) {
		return
	}
	var body generated.CreateSubnetRequest
	if !decodeBody(w, r, &body) || !required(w, body.Name, body.VpcId, body.CidrBlock, string(body.AvailabilityZone)) {
		return
	}
	key := ""
	if params.IdempotencyKey != nil {
		key = *params.IdempotencyKey
	}
	subnet, err := s.resources.CreateSubnet(r.Context(), service.CreateSubnetInput{
		Name: body.Name, VPCID: body.VpcId, CIDRBlock: body.CidrBlock, AvailabilityZone: string(body.AvailabilityZone),
	}, key)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, subnetResponse(subnet))
}

func (s *server) ListSubnets(w http.ResponseWriter, r *http.Request, workspace generated.Workspace, params generated.ListSubnetsParams) {
	if !checkWorkspace(w, workspace) || !s.checkResources(w) {
		return
	}
	limit, token := 0, ""
	if params.Limit != nil {
		limit = *params.Limit
	}
	if params.PageToken != nil {
		token = *params.PageToken
	}
	items, next, err := s.resources.ListSubnets(r.Context(), limit, token)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	page := generated.SubnetsPage{Items: make([]generated.Subnet, 0, len(items))}
	for i := range items {
		page.Items = append(page.Items, subnetResponse(items[i]))
	}
	if next != "" {
		page.NextPageToken = &next
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *server) GetSubnet(w http.ResponseWriter, r *http.Request, workspace generated.Workspace, id generated.ResourceID) {
	if !checkWorkspace(w, workspace) || !s.checkResources(w) {
		return
	}
	subnet, err := s.resources.GetSubnet(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, subnetResponse(subnet))
}

func (s *server) DeleteSubnet(w http.ResponseWriter, r *http.Request, workspace generated.Workspace, id generated.ResourceID) {
	if !checkWorkspace(w, workspace) || !s.checkResources(w) {
		return
	}
	subnet, err := s.resources.DeleteSubnet(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, subnetResponse(subnet))
}
