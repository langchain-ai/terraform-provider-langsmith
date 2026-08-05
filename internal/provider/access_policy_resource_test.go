package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestAccessPolicyResourceCreateAndRead(t *testing.T) {
	requests := []string{}
	resource := newAccessPolicyResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.Method+" "+req.URL.Path)
		switch req.Method {
		case http.MethodPost:
			var payload accessPolicyPayload
			decodeJSON(t, req, &payload)
			if payload.Name != "Production readers" || payload.Effect != "allow" {
				t.Fatalf("create payload = %#v", payload)
			}
			writeJSON(t, w, accessPolicyCreateResponse{ID: "policy-id"})
		case http.MethodGet:
			writeJSON(t, w, accessPolicyAPI{ID: "policy-id", Name: "Production readers", Effect: "allow", ConditionGroups: sampleAccessPolicyGroups(), CreatedAt: "created", UpdatedAt: "updated"})
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
	}))

	model, err := resource.createAccessPolicy(context.Background(), sampleAccessPolicyModel())
	if err != nil {
		t.Fatalf("createAccessPolicy() error = %v", err)
	}
	if model.ID.ValueString() != "policy-id" {
		t.Fatalf("model = %#v", model)
	}
	if !reflect.DeepEqual(requests, []string{"POST /api/v1/platform/orgs/current/access-policies", "GET /api/v1/platform/orgs/current/access-policies/policy-id"}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestAccessPolicyResourceUpdate(t *testing.T) {
	requests := []string{}
	resource := newAccessPolicyResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.Method+" "+req.URL.RequestURI())
		switch req.Method {
		case http.MethodPatch:
			var payload accessPolicyUpdatePayload
			decodeJSON(t, req, &payload)
			if payload.Name != "Production readers" || payload.Description == nil || *payload.Description != "Read production projects" {
				t.Fatalf("update payload = %#v", payload)
			}
			writeJSON(t, w, accessPolicyAPI{ID: "policy-id"})
		case http.MethodGet:
			writeJSON(t, w, accessPolicyAPI{ID: "policy-id", Name: "Production readers", Effect: "allow", ConditionGroups: sampleAccessPolicyGroups()})
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
	}))

	model, err := resource.updateAccessPolicy(context.Background(), "policy-id", sampleAccessPolicyModel())
	if err != nil {
		t.Fatalf("updateAccessPolicy() error = %v", err)
	}
	if model.ID.ValueString() != "policy-id" {
		t.Fatalf("model = %#v", model)
	}
	if !reflect.DeepEqual(requests, []string{
		"PATCH /api/v1/platform/orgs/current/access-policies/policy-id",
		"GET /api/v1/platform/orgs/current/access-policies/policy-id",
	}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestPreserveAccessPolicyOptionalShape(t *testing.T) {
	model := accessPolicyModelFromAPI(accessPolicyAPI{})
	configured := accessPolicyResourceModel{Description: types.StringValue("")}
	got := preserveAccessPolicyOptionalShape(model, configured)
	if got.Description.IsNull() || got.Description.ValueString() != "" {
		t.Fatalf("Description = %#v, want explicit empty string", got.Description)
	}
	got = preserveAccessPolicyOptionalShape(model, accessPolicyResourceModel{Description: types.StringNull()})
	if !got.Description.IsNull() {
		t.Fatalf("optional null shape was not preserved: %#v", got)
	}
}

func TestAccessPolicyUpdateClearsDescriptionWithJSONNull(t *testing.T) {
	payload := accessPolicyUpdatePayloadFromModel(accessPolicyResourceModel{
		Name: types.StringValue("Readers"), Description: types.StringNull(), Effect: types.StringValue("allow"),
	})
	if payload.Description != nil {
		t.Fatalf("Description = %#v, want nil so JSON encodes null", payload.Description)
	}
}

func TestAccessPolicyCreateDoesNotRetry(t *testing.T) {
	requests := 0
	resource := newAccessPolicyResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		http.Error(w, "temporary failure", http.StatusInternalServerError)
	}))

	if _, err := resource.createAccessPolicy(context.Background(), sampleAccessPolicyModel()); err == nil {
		t.Fatal("createAccessPolicy() error = nil")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func sampleAccessPolicyModel() accessPolicyResourceModel {
	return accessPolicyResourceModel{
		Name: types.StringValue("Production readers"), Description: types.StringValue("Read production projects"), Effect: types.StringValue("allow"),
		ConditionGroups: []accessPolicyConditionGroupModel{{
			Permission: types.StringValue("projects:read"), ResourceType: types.StringValue("project"),
			Conditions: []accessPolicyConditionModel{{AttributeName: types.StringValue("resource_tag_key"), AttributeKey: types.StringValue("Environment"), Operator: types.StringValue("equals"), AttributeValue: types.StringValue("production")}},
		}},
	}
}

func sampleAccessPolicyGroups() []accessPolicyConditionGroup {
	return accessPolicyPayloadFromModel(sampleAccessPolicyModel()).ConditionGroups
}

func newAccessPolicyResourceWithServer(t *testing.T, handler http.Handler) *AccessPolicyResource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &AccessPolicyResource{client: langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("test-key"))}
}
