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
			if payload.Name != "Production readers" || payload.Effect != "allow" || !reflect.DeepEqual(payload.RoleIDs, []string{"role-a", "role-b"}) {
				t.Fatalf("create payload = %#v", payload)
			}
			writeJSON(t, w, accessPolicyCreateResponse{ID: "policy-id"})
		case http.MethodGet:
			writeJSON(t, w, accessPolicyAPI{ID: "policy-id", Name: "Production readers", Effect: "allow", ConditionGroups: sampleAccessPolicyGroups(), RoleIDs: []string{"role-b", "role-a"}, CreatedAt: "created", UpdatedAt: "updated"})
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
	}))

	model, err := resource.createAccessPolicy(context.Background(), sampleAccessPolicyModel([]string{"role-b", "role-a"}))
	if err != nil {
		t.Fatalf("createAccessPolicy() error = %v", err)
	}
	if model.ID.ValueString() != "policy-id" || !reflect.DeepEqual(model.RoleIDs, []string{"role-a", "role-b"}) {
		t.Fatalf("model = %#v", model)
	}
	if !reflect.DeepEqual(requests, []string{"POST /api/v1/platform/orgs/current/access-policies", "GET /api/v1/platform/orgs/current/access-policies/policy-id"}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestAccessPolicyResourceUpdateReconcilesRoleAttachments(t *testing.T) {
	requests := []string{}
	resource := newAccessPolicyResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.Method+" "+req.URL.Path)
		switch {
		case req.Method == http.MethodPatch:
			var payload accessPolicyPayload
			decodeJSON(t, req, &payload)
			if payload.Name != "Production readers" || payload.RoleIDs != nil {
				t.Fatalf("update payload = %#v", payload)
			}
			writeJSON(t, w, accessPolicyAPI{ID: "policy-id"})
		case req.Method == http.MethodPost && req.URL.Path == "/api/v1/platform/orgs/current/access-policies/roles/role-c/access-policies":
			assertAccessPolicyAttachmentPayload(t, req, "policy-id")
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodDelete && req.URL.Path == "/api/v1/platform/orgs/current/access-policies/roles/role-a/access-policies":
			assertAccessPolicyAttachmentPayload(t, req, "policy-id")
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodGet:
			writeJSON(t, w, accessPolicyAPI{ID: "policy-id", Name: "Production readers", Effect: "allow", ConditionGroups: sampleAccessPolicyGroups(), RoleIDs: []string{"role-b", "role-c"}})
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
	}))

	model, err := resource.updateAccessPolicy(context.Background(), "policy-id", []string{"role-a", "role-b"}, sampleAccessPolicyModel([]string{"role-b", "role-c"}))
	if err != nil {
		t.Fatalf("updateAccessPolicy() error = %v", err)
	}
	if !reflect.DeepEqual(model.RoleIDs, []string{"role-b", "role-c"}) {
		t.Fatalf("RoleIDs = %#v", model.RoleIDs)
	}
	if !reflect.DeepEqual(requests, []string{
		"PATCH /api/v1/platform/orgs/current/access-policies/policy-id",
		"POST /api/v1/platform/orgs/current/access-policies/roles/role-c/access-policies",
		"DELETE /api/v1/platform/orgs/current/access-policies/roles/role-a/access-policies",
		"GET /api/v1/platform/orgs/current/access-policies/policy-id",
	}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestStringSetDifferenceIsSorted(t *testing.T) {
	got := stringSetDifference([]string{"c", "a", "b"}, []string{"b"})
	if !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Fatalf("stringSetDifference() = %#v", got)
	}
}

func sampleAccessPolicyModel(roleIDs []string) accessPolicyResourceModel {
	return accessPolicyResourceModel{
		Name: types.StringValue("Production readers"), Description: types.StringValue("Read production projects"), Effect: types.StringValue("allow"), RoleIDs: roleIDs,
		ConditionGroups: []accessPolicyConditionGroupModel{{
			Permission: types.StringValue("projects:read"), ResourceType: types.StringValue("project"),
			Conditions: []accessPolicyConditionModel{{AttributeName: types.StringValue("resource_tag_key"), AttributeKey: types.StringValue("Environment"), Operator: types.StringValue("equals"), AttributeValue: types.StringValue("production")}},
		}},
	}
}

func sampleAccessPolicyGroups() []accessPolicyConditionGroup {
	return accessPolicyPayloadFromModel(sampleAccessPolicyModel(nil), false).ConditionGroups
}

func assertAccessPolicyAttachmentPayload(t *testing.T, req *http.Request, policyID string) {
	t.Helper()
	var payload accessPolicyAttachmentPayload
	decodeJSON(t, req, &payload)
	if !reflect.DeepEqual(payload.AccessPolicyIDs, []string{policyID}) {
		t.Fatalf("attachment payload = %#v", payload)
	}
}

func newAccessPolicyResourceWithServer(t *testing.T, handler http.Handler) *AccessPolicyResource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &AccessPolicyResource{client: langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("test-key"))}
}
