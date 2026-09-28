package api

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func contract(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("invalid OpenAPI contract: %v", err)
	}
	return doc
}

func operation(t *testing.T, doc *openapi3.T, path, method string) *openapi3.Operation {
	t.Helper()
	item := doc.Paths.Find(path)
	if item == nil {
		t.Fatalf("%s path %s is absent", method, path)
	}
	op := item.GetOperation(method)
	if op == nil {
		t.Fatalf("%s %s is absent", method, path)
	}
	return op
}

func hasParameter(op *openapi3.Operation, in, name string) bool {
	for _, ref := range op.Parameters {
		if ref.Value != nil && ref.Value.In == in && ref.Value.Name == name {
			return true
		}
	}
	return false
}

func TestM1ResourceOperations(t *testing.T) {
	doc := contract(t)
	collection := "/v1/workspaces/{workspace}/"
	cases := []struct {
		method, path, id string
		success          int
	}{
		{http.MethodPost, collection + "vpcs", "createVpc", 201},
		{http.MethodGet, collection + "vpcs", "listVpcs", 200},
		{http.MethodGet, collection + "vpcs/{id}", "getVpc", 200},
		{http.MethodDelete, collection + "vpcs/{id}", "deleteVpc", 202},
		{http.MethodPost, collection + "subnets", "createSubnet", 201},
		{http.MethodGet, collection + "subnets", "listSubnets", 200},
		{http.MethodGet, collection + "subnets/{id}", "getSubnet", 200},
		{http.MethodDelete, collection + "subnets/{id}", "deleteSubnet", 202},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			op := operation(t, doc, tc.path, tc.method)
			if op.OperationID != tc.id {
				t.Errorf("operationId=%q, want %q", op.OperationID, tc.id)
			}
			if op.Responses == nil || op.Responses.Status(tc.success) == nil {
				t.Errorf("missing %d response", tc.success)
			}
			if op.Security == nil || len(*op.Security) != 1 {
				t.Fatal("resource route does not require one bearer scheme")
			}
			if _, ok := (*op.Security)[0]["bearerAuth"]; !ok {
				t.Error("resource route does not require bearerAuth")
			}
			for _, code := range []int{400, 401, 404, 409} {
				if op.Responses == nil || op.Responses.Status(code) == nil {
					t.Errorf("missing typed %d error response", code)
				}
			}
			item := doc.Paths.Find(tc.path)
			foundWorkspace := false
			for _, ref := range item.Parameters {
				if ref.Value != nil && ref.Value.In == "path" && ref.Value.Name == "workspace" && ref.Value.Required {
					foundWorkspace = true
				}
			}
			if !foundWorkspace {
				t.Error("required workspace path parameter is missing")
			}
		})
	}
}

func TestM1CreateRequestsRejectUnknownFields(t *testing.T) {
	doc := contract(t)
	cases := []struct {
		path, schema string
		required     []string
	}{
		{"/v1/workspaces/{workspace}/vpcs", "CreateVpcRequest", []string{"name", "cidr_block"}},
		{"/v1/workspaces/{workspace}/subnets", "CreateSubnetRequest", []string{"name", "vpc_id", "cidr_block", "availability_zone"}},
	}
	for _, tc := range cases {
		t.Run(tc.schema, func(t *testing.T) {
			op := operation(t, doc, tc.path, http.MethodPost)
			if !hasParameter(op, "header", "Idempotency-Key") {
				t.Error("Idempotency-Key header is absent")
			}
			if op.RequestBody == nil || op.RequestBody.Value == nil || !op.RequestBody.Value.Required {
				t.Fatal("required request body is absent")
			}
			media := op.RequestBody.Value.Content["application/json"]
			if media == nil || media.Schema == nil || media.Schema.Value == nil {
				t.Fatal("JSON request schema is absent")
			}
			schema := media.Schema.Value
			if schema.AdditionalProperties.Has == nil || *schema.AdditionalProperties.Has {
				t.Error("request schema permits unknown fields")
			}
			for _, field := range tc.required {
				if !slices.Contains(schema.Required, field) || schema.Properties[field] == nil {
					t.Errorf("%s must be present and required", field)
				}
			}
		})
	}
}

func TestM1ListPagination(t *testing.T) {
	doc := contract(t)
	for _, resource := range []string{"vpcs", "subnets"} {
		t.Run(resource, func(t *testing.T) {
			op := operation(t, doc, "/v1/workspaces/{workspace}/"+resource, http.MethodGet)
			for _, parameter := range []string{"limit", "page_token"} {
				if !hasParameter(op, "query", parameter) {
					t.Errorf("missing %s query parameter", parameter)
				}
			}
		})
	}
}

func TestM1ResourceResponseFields(t *testing.T) {
	doc := contract(t)
	for _, tc := range []struct {
		name   string
		fields []string
	}{
		{"Vpc", []string{"id", "name", "cidr_block", "state", "state_reason", "generation", "observed_generation"}},
		{"Subnet", []string{"id", "name", "vpc_id", "cidr_block", "availability_zone", "state", "state_reason", "generation", "observed_generation"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := doc.Components.Schemas[tc.name]
			if ref == nil || ref.Value == nil {
				t.Fatalf("%s schema absent", tc.name)
			}
			for _, field := range tc.fields {
				if ref.Value.Properties[field] == nil || !slices.Contains(ref.Value.Required, field) {
					t.Errorf("%s.%s must be present and required", tc.name, field)
				}
			}
		})
	}
	errSchema := doc.Components.Schemas["Error"]
	if errSchema == nil || errSchema.Value == nil ||
		!slices.Contains(errSchema.Value.Required, "code") ||
		!slices.Contains(errSchema.Value.Required, "message") {
		t.Fatal("typed AWS-style error envelope is missing")
	}
}

func TestM1EventsStreamContract(t *testing.T) {
	doc := contract(t)
	op := operation(t, doc, "/v1/events", http.MethodGet)
	if op.OperationID != "getEvents" {
		t.Errorf("operationId=%q", op.OperationID)
	}
	if op.Security == nil || len(*op.Security) != 1 {
		t.Fatal("events route is not authenticated")
	}
	if _, ok := (*op.Security)[0]["bearerAuth"]; !ok {
		t.Error("events route does not require bearerAuth")
	}
	if !hasParameter(op, "header", "Last-Event-ID") {
		t.Error("Last-Event-ID header absent")
	}
	response := op.Responses.Status(200)
	if response == nil || response.Value == nil || response.Value.Content["text/event-stream"] == nil {
		t.Error("200 text/event-stream response absent")
	}
}
