package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestWorkspaceRoleResourceMetadata(t *testing.T) {
	resourceUnderTest := NewWorkspaceRoleResource()
	var resp resource.MetadataResponse
	resourceUnderTest.Metadata(context.Background(), resource.MetadataRequest{
		ProviderTypeName: "langsmith",
	}, &resp)

	if resp.TypeName != "langsmith_workspace_role" {
		t.Fatalf("TypeName = %q, want langsmith_workspace_role", resp.TypeName)
	}
}

func TestWorkspaceRoleResourceSchemaDoesNotExposeAccessScope(t *testing.T) {
	resourceUnderTest := NewWorkspaceRoleResource()
	var resp resource.SchemaResponse
	resourceUnderTest.Schema(context.Background(), resource.SchemaRequest{}, &resp)

	if _, ok := resp.Schema.Attributes["access_scope"]; ok {
		t.Fatalf("access_scope should be implied by langsmith_workspace_role, not exposed in schema")
	}
}

func TestRolePayloadFromModel(t *testing.T) {
	payload := rolePayloadFromModel(roleResourceModel{
		DisplayName: types.StringValue("Dev Contributor"),
		Description: types.StringValue("Can use dev workspaces"),
		Permissions: []string{"projects:read", "projects:create"},
	})

	if payload.DisplayName != "Dev Contributor" {
		t.Fatalf("DisplayName = %q", payload.DisplayName)
	}
	if payload.Description != "Can use dev workspaces" {
		t.Fatalf("Description = %q", payload.Description)
	}
	if !reflect.DeepEqual(payload.Permissions, []string{"projects:read", "projects:create"}) {
		t.Fatalf("Permissions = %#v", payload.Permissions)
	}
}

func TestRoleModelFromAPI(t *testing.T) {
	model := roleModelFromAPI(roleAPI{
		ID:             "role-id",
		Name:           "CUSTOM_ROLE",
		DisplayName:    "Custom Role",
		Description:    "desc",
		OrganizationID: "org-id",
		Permissions:    []string{"projects:read"},
		AccessScope:    "workspace",
	})

	if model.ID.ValueString() != "role-id" {
		t.Fatalf("ID = %q", model.ID.ValueString())
	}
	if model.Name.ValueString() != "CUSTOM_ROLE" {
		t.Fatalf("Name = %q", model.Name.ValueString())
	}
	if !reflect.DeepEqual(model.Permissions, []string{"projects:read"}) {
		t.Fatalf("Permissions = %#v", model.Permissions)
	}
}

func TestRoleModelFromAPIUsesEmptyPermissionsWhenResponseOmitsPermissions(t *testing.T) {
	model := roleModelFromAPI(roleAPI{
		ID:          "role-id",
		Name:        "CUSTOM",
		DisplayName: "Custom Role",
		Description: "desc",
		AccessScope: accessScopeWorkspace,
	})

	if model.Permissions == nil {
		t.Fatalf("Permissions = nil, want empty list")
	}
	if len(model.Permissions) != 0 {
		t.Fatalf("Permissions = %#v, want empty list", model.Permissions)
	}
}

func TestFindRoleByIDReturnsNotFoundSentinel(t *testing.T) {
	_, err := findRoleByID(nil, "missing-role", "workspace")
	if !errors.Is(err, errRoleNotFound) {
		t.Fatalf("findRoleByID error = %v, want errRoleNotFound", err)
	}
}

func TestValidateRoleResourceRequiredFields(t *testing.T) {
	diagnostics := &attributeErrorRecorder{}
	ok := validateRoleResourceRequiredFields(roleResourceModel{
		DisplayName: types.StringValue(" "),
		Description: types.StringValue(" "),
		Permissions: nil,
	}, diagnostics)
	if ok {
		t.Fatalf("validateRoleResourceRequiredFields returned true for invalid role")
	}
	if diagnostics.count != 3 {
		t.Fatalf("diagnostics count = %d, want 3", diagnostics.count)
	}
}

func TestWorkspaceRoleResourceCreatePostsPayloadAndReadsRole(t *testing.T) {
	requests := make([]string, 0, 2)
	resource := newRoleResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/current/roles":
			var payload rolePayload
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode create payload: %v", err)
			}
			if payload.DisplayName != "Dev Contributor" || payload.Description != "Can use dev workspaces" {
				t.Fatalf("payload = %#v", payload)
			}
			if !reflect.DeepEqual(payload.Permissions, []string{"projects:read"}) {
				t.Fatalf("payload.Permissions = %#v", payload.Permissions)
			}
			writeJSON(t, w, roleAPI{ID: "role-id"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/roles":
			writeJSON(t, w, []roleAPI{{
				ID:             "role-id",
				Name:           "CUSTOM",
				DisplayName:    "Dev Contributor",
				Description:    "Can use dev workspaces",
				OrganizationID: "org-id",
				Permissions:    []string{"projects:read"},
				AccessScope:    accessScopeWorkspace,
			}})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))

	role, err := resource.createRole(context.Background(), roleResourceModel{
		DisplayName: types.StringValue("Dev Contributor"),
		Description: types.StringValue("Can use dev workspaces"),
		Permissions: []string{"projects:read"},
	})
	if err != nil {
		t.Fatalf("createRole returned error: %v", err)
	}
	if role.ID != "role-id" || role.AccessScope != accessScopeWorkspace {
		t.Fatalf("role = %#v", role)
	}
	if !reflect.DeepEqual(requests, []string{
		"POST /api/v1/orgs/current/roles",
		"GET /api/v1/orgs/current/roles",
	}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestWorkspaceRoleResourceUpdatePatchesPayloadAndReadsRole(t *testing.T) {
	requests := make([]string, 0, 2)
	resource := newRoleResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/orgs/current/roles/role-id":
			var payload rolePayload
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode update payload: %v", err)
			}
			if payload.DisplayName != "Dev Admin" {
				t.Fatalf("payload.DisplayName = %q, want Dev Admin", payload.DisplayName)
			}
			writeJSON(t, w, roleAPI{ID: "role-id"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/roles":
			writeJSON(t, w, []roleAPI{{
				ID:          "role-id",
				Name:        "CUSTOM",
				DisplayName: "Dev Admin",
				Description: "Can administer dev workspaces",
				Permissions: []string{"projects:read", "projects:create"},
				AccessScope: accessScopeWorkspace,
			}})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))

	role, err := resource.updateRole(context.Background(), "role-id", roleResourceModel{
		DisplayName: types.StringValue("Dev Admin"),
		Description: types.StringValue("Can administer dev workspaces"),
		Permissions: []string{"projects:read", "projects:create"},
	})
	if err != nil {
		t.Fatalf("updateRole returned error: %v", err)
	}
	if role.DisplayName != "Dev Admin" {
		t.Fatalf("role.DisplayName = %q, want Dev Admin", role.DisplayName)
	}
	if !reflect.DeepEqual(requests, []string{
		"PATCH /api/v1/orgs/current/roles/role-id",
		"GET /api/v1/orgs/current/roles",
	}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestWorkspaceRoleResourceDeleteReturnsNotFound(t *testing.T) {
	resource := newRoleResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/orgs/current/roles/missing-role" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		http.NotFound(w, r)
	}))

	err := resource.deleteRole(context.Background(), "missing-role")
	if !isLangSmithNotFound(err) {
		t.Fatalf("deleteRole error = %v, want LangSmith 404", err)
	}
}

func newRoleResourceWithServer(t *testing.T, handler http.Handler) *WorkspaceRoleResource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &WorkspaceRoleResource{
		client: langsmith.NewClient(
			option.WithBaseURL(server.URL),
			option.WithAPIKey("test-key"),
		),
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

type attributeErrorRecorder struct {
	count int
}

func (r *attributeErrorRecorder) AddAttributeError(attributePath path.Path, summary string, detail string) {
	r.count++
}
