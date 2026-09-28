package api

import (
	"net/http"
	"slices"
	"testing"
)

func TestInstanceContract(t *testing.T) {
	doc := contract(t)
	collection := "/v1/workspaces/{workspace}/instances"
	for _, tc := range []struct {
		method, path, id, schema string
		status                   int
	}{
		{http.MethodPost, collection, "runInstance", "Instance", 201},
		{http.MethodGet, collection, "listInstances", "InstancesPage", 200},
		{http.MethodGet, collection + "/{id}", "getInstance", "Instance", 200},
		{http.MethodDelete, collection + "/{id}", "terminateInstance", "Instance", 202},
	} {
		t.Run(tc.id, func(t *testing.T) {
			op := operation(t, doc, tc.path, tc.method)
			if op.OperationID != tc.id {
				t.Errorf("operationId=%q, want %q", op.OperationID, tc.id)
			}
			if op.Security == nil || len(*op.Security) != 1 {
				t.Fatal("instance operation must require bearer authentication")
			}
			if _, ok := (*op.Security)[0]["bearerAuth"]; !ok {
				t.Error("bearerAuth absent")
			}
			response := op.Responses.Status(tc.status)
			if response == nil || response.Value == nil {
				t.Fatalf("missing %d response", tc.status)
			}
			media := response.Value.Content["application/json"]
			if media == nil || media.Schema == nil || media.Schema.Ref != "#/components/schemas/"+tc.schema {
				t.Errorf("response must reference %s", tc.schema)
			}
			for _, status := range []int{400, 401, 404, 409} {
				ref := op.Responses.Status(status)
				if ref == nil || ref.Value == nil || ref.Value.Content["application/json"] == nil {
					t.Errorf("missing typed %d error", status)
				}
			}
			if tc.method == http.MethodPost {
				if !hasParameter(op, "header", "Idempotency-Key") {
					t.Error("Idempotency-Key absent")
				}
				if op.RequestBody == nil || op.RequestBody.Value == nil || !op.RequestBody.Value.Required {
					t.Fatal("required request body absent")
				}
				media := op.RequestBody.Value.Content["application/json"]
				if media == nil || media.Schema == nil || media.Schema.Ref != "#/components/schemas/RunInstanceRequest" {
					t.Error("RunInstanceRequest absent")
				}
			}
			if tc.id == "listInstances" {
				for _, parameter := range []string{"limit", "page_token"} {
					if !hasParameter(op, "query", parameter) {
						t.Errorf("missing %s", parameter)
					}
				}
			}
		})
	}
	for _, tc := range []struct {
		name   string
		fields []string
	}{
		{"RunInstanceRequest", []string{"name", "subnet_id"}},
		{"Instance", []string{"id", "name", "subnet_id", "private_ip", "eni_id", "instance_type", "state", "state_reason", "generation", "observed_generation"}},
		{"InstancesPage", []string{"items"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := doc.Components.Schemas[tc.name]
			if ref == nil || ref.Value == nil {
				t.Fatalf("schema %s absent", tc.name)
			}
			schema := ref.Value
			if schema.AdditionalProperties.Has == nil || *schema.AdditionalProperties.Has {
				t.Error("schema permits unknown fields")
			}
			for _, field := range tc.fields {
				if schema.Properties[field] == nil || !slices.Contains(schema.Required, field) {
					t.Errorf("%s must be present and required", field)
				}
			}
			if tc.name == "RunInstanceRequest" && len(schema.Properties) != 2 {
				t.Error("M1 request must contain only name and subnet_id")
			}
			if tc.name == "InstancesPage" && schema.Properties["next_page_token"] == nil {
				t.Error("next_page_token absent")
			}
		})
	}
}

func TestConsoleContract(t *testing.T) {
	doc := contract(t)
	op := operation(t, doc, "/v1/workspaces/{workspace}/instances/{id}/console", http.MethodGet)
	if op.OperationID != "getInstanceConsole" {
		t.Errorf("operationId=%q", op.OperationID)
	}
	if op.Security == nil || len(*op.Security) != 1 {
		t.Fatal("console must require bearer authentication")
	}
	if _, ok := (*op.Security)[0]["bearerAuth"]; !ok {
		t.Error("bearerAuth absent")
	}
	if op.Responses.Status(http.StatusSwitchingProtocols) == nil {
		t.Error("WebSocket 101 response absent")
	}
	for _, status := range []int{400, 401, 404, 409} {
		if op.Responses.Status(status) == nil {
			t.Errorf("typed %d error absent", status)
		}
	}
}
