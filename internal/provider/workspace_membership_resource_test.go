package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	frameworkresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestWorkspaceMembershipSchemaHidesInternalIdentityFields(t *testing.T) {
	resp := &frameworkresource.SchemaResponse{}
	NewWorkspaceMembershipResource().Schema(context.Background(), frameworkresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema returned diagnostics: %v", resp.Diagnostics)
	}

	for _, name := range []string{"identity_id", "pending_identity_id", "ls_user_id", "user_id", "role_name"} {
		if _, ok := resp.Schema.Attributes[name]; ok {
			t.Fatalf("schema exposes internal attribute %q", name)
		}
	}
	for _, name := range []string{"id", "workspace_id", "email", "role_id", "status"} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Fatalf("schema is missing public attribute %q", name)
		}
	}
}

func TestWorkspaceMembershipModelFromActiveReadResult(t *testing.T) {
	model := workspaceMembershipModelFromReadResult("workspace-id", "alice@langchain.dev", membershipReadResult{
		Status:     membershipStatusActive,
		IdentityID: "workspace-identity-id",
		LSUserID:   "ls-user-id",
		UserID:     "user-id",
		RoleID:     "workspace-role-id",
		RoleName:   "Workspace Admin",
	})

	if model.ID.ValueString() != "workspace/workspace-id/email/alice@langchain.dev" {
		t.Fatalf("ID = %q", model.ID.ValueString())
	}
	if model.WorkspaceID.ValueString() != "workspace-id" {
		t.Fatalf("WorkspaceID = %q", model.WorkspaceID.ValueString())
	}
	if model.Email.ValueString() != "alice@langchain.dev" {
		t.Fatalf("Email = %q", model.Email.ValueString())
	}
	if model.Status.ValueString() != membershipStatusActive {
		t.Fatalf("Status = %q", model.Status.ValueString())
	}
	if model.RoleID.ValueString() != "workspace-role-id" {
		t.Fatalf("RoleID = %q", model.RoleID.ValueString())
	}
}

func TestValidateWorkspaceMembershipRequiredFieldsRejectsNonCanonicalEmail(t *testing.T) {
	diagnostics := &attributeErrorRecorder{}
	ok := validateWorkspaceMembershipRequiredFields(workspaceMembershipResourceModel{
		WorkspaceID: types.StringValue("workspace-id"),
		Email:       types.StringValue(" Alice@LangChain.Dev "),
		RoleID:      types.StringValue("workspace-role-id"),
	}, diagnostics)

	if ok {
		t.Fatalf("validateWorkspaceMembershipRequiredFields returned true for non-canonical email")
	}
	if diagnostics.count != 1 {
		t.Fatalf("diagnostics count = %d, want 1", diagnostics.count)
	}
}

func TestParseWorkspaceMembershipImportID(t *testing.T) {
	workspaceID, email, err := parseWorkspaceMembershipImportID("workspace-id/Alice@LangChain.Dev")
	if err != nil {
		t.Fatalf("parseWorkspaceMembershipImportID returned error: %v", err)
	}
	if workspaceID != "workspace-id" {
		t.Fatalf("workspaceID = %q, want workspace-id", workspaceID)
	}
	if email != "alice@langchain.dev" {
		t.Fatalf("email = %q, want alice@langchain.dev", email)
	}

	workspaceID, email, err = parseWorkspaceMembershipImportID("workspace/workspace-id/email/Bob@LangChain.Dev")
	if err != nil {
		t.Fatalf("parseWorkspaceMembershipImportID synthetic ID returned error: %v", err)
	}
	if workspaceID != "workspace-id" {
		t.Fatalf("workspaceID = %q, want workspace-id", workspaceID)
	}
	if email != "bob@langchain.dev" {
		t.Fatalf("email = %q, want bob@langchain.dev", email)
	}

	if _, _, err := parseWorkspaceMembershipImportID("workspace-id/"); err == nil {
		t.Fatalf("parseWorkspaceMembershipImportID returned nil error for empty email")
	}
	if _, _, err := parseWorkspaceMembershipImportID("/alice@langchain.dev"); err == nil {
		t.Fatalf("parseWorkspaceMembershipImportID returned nil error for empty workspace")
	}
}

func TestWorkspaceMembershipEnsureRequiresAcceptedOrgMembership(t *testing.T) {
	resource := newWorkspaceMembershipResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/roles":
			writeJSON(t, w, []roleAPI{{
				ID:          "workspace-role-id",
				DisplayName: "Workspace Admin",
				AccessScope: accessScopeWorkspace,
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/current/members/active":
			assertWorkspaceTenantHeader(t, r)
			writeJSON(t, w, []memberIdentityAPI{})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/current/members/pending":
			assertWorkspaceTenantHeader(t, r)
			writeJSON(t, w, []pendingIdentityAPI{})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/members/active":
			writeJSON(t, w, []memberIdentityAPI{})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/members/pending":
			writeJSON(t, w, []pendingIdentityAPI{{
				ID:        "pending-org-id",
				Email:     "alice@langchain.dev",
				OrgRoleID: "org-role-id",
			}})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))

	_, err := resource.ensureWorkspaceMembership(context.Background(), workspaceMembershipResourceModel{
		WorkspaceID: types.StringValue("workspace-id"),
		Email:       types.StringValue("alice@langchain.dev"),
		RoleID:      types.StringValue("workspace-role-id"),
	})
	if err == nil {
		t.Fatalf("ensureWorkspaceMembership returned nil error for pending org membership")
	}
	if !strings.Contains(err.Error(), "organization membership for alice@langchain.dev is pending") {
		t.Fatalf("error = %v", err)
	}
}

func TestWorkspaceMembershipEnsureCreatesActiveMembershipForAcceptedOrgMember(t *testing.T) {
	var created bool
	requests := make([]string, 0, 8)
	resource := newWorkspaceMembershipResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/roles":
			writeJSON(t, w, []roleAPI{{
				ID:          "workspace-role-id",
				DisplayName: "Workspace Admin",
				AccessScope: accessScopeWorkspace,
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/current/members/active":
			assertWorkspaceTenantHeader(t, r)
			if created {
				writeJSON(t, w, []memberIdentityAPI{{
					ID:       "workspace-identity-id",
					Email:    "alice@langchain.dev",
					RoleID:   "workspace-role-id",
					RoleName: "Workspace Admin",
					LSUserID: "ls-user-id",
				}})
				return
			}
			writeJSON(t, w, []memberIdentityAPI{})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/current/members/pending":
			assertWorkspaceTenantHeader(t, r)
			writeJSON(t, w, []pendingIdentityAPI{})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/members/active":
			writeJSON(t, w, []memberIdentityAPI{{
				ID:       "org-identity-id",
				Email:    "alice@langchain.dev",
				RoleID:   "org-role-id",
				LSUserID: "ls-user-id",
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/members/pending":
			writeJSON(t, w, []pendingIdentityAPI{})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces/current/members":
			assertWorkspaceTenantHeader(t, r)
			var payload workspaceMemberPayload
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode workspace member payload: %v", err)
			}
			if payload.OrgIdentityID != "org-identity-id" || payload.RoleID != "workspace-role-id" {
				t.Fatalf("payload = %#v", payload)
			}
			created = true
			writeJSON(t, w, memberIdentityAPI{ID: "workspace-identity-id"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))

	model, err := resource.ensureWorkspaceMembership(context.Background(), workspaceMembershipResourceModel{
		WorkspaceID: types.StringValue("workspace-id"),
		Email:       types.StringValue("alice@langchain.dev"),
		RoleID:      types.StringValue("workspace-role-id"),
	})
	if err != nil {
		t.Fatalf("ensureWorkspaceMembership returned error: %v", err)
	}
	if model.Status.ValueString() != membershipStatusActive {
		t.Fatalf("Status = %q, want active", model.Status.ValueString())
	}
	if model.RoleID.ValueString() != "workspace-role-id" {
		t.Fatalf("RoleID = %q, want workspace-role-id", model.RoleID.ValueString())
	}
	if !reflect.DeepEqual(requests, []string{
		"GET /api/v1/orgs/current/roles",
		"GET /api/v1/workspaces/current/members/active",
		"GET /api/v1/workspaces/current/members/pending",
		"GET /api/v1/orgs/current/members/active",
		"GET /api/v1/orgs/current/members/pending",
		"POST /api/v1/workspaces/current/members",
		"GET /api/v1/workspaces/current/members/active",
		"GET /api/v1/workspaces/current/members/pending",
	}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestWorkspaceMembershipEnsurePatchesActiveRole(t *testing.T) {
	requests := make([]string, 0, 6)
	roleID := "old-role-id"
	resource := newWorkspaceMembershipResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/roles":
			writeJSON(t, w, []roleAPI{{
				ID:          "new-role-id",
				DisplayName: "Workspace Admin",
				AccessScope: accessScopeWorkspace,
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/current/members/active":
			assertWorkspaceTenantHeader(t, r)
			writeJSON(t, w, []memberIdentityAPI{{
				ID:       "workspace-identity-id",
				Email:    "alice@langchain.dev",
				RoleID:   roleID,
				RoleName: "Workspace Role",
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/current/members/pending":
			assertWorkspaceTenantHeader(t, r)
			writeJSON(t, w, []pendingIdentityAPI{})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/workspaces/current/members/workspace-identity-id":
			assertWorkspaceTenantHeader(t, r)
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			if payload["role_id"] != "new-role-id" {
				t.Fatalf("payload = %#v", payload)
			}
			roleID = "new-role-id"
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))

	model, err := resource.ensureWorkspaceMembership(context.Background(), workspaceMembershipResourceModel{
		WorkspaceID: types.StringValue("workspace-id"),
		Email:       types.StringValue("alice@langchain.dev"),
		RoleID:      types.StringValue("new-role-id"),
	})
	if err != nil {
		t.Fatalf("ensureWorkspaceMembership returned error: %v", err)
	}
	if model.RoleID.ValueString() != "new-role-id" {
		t.Fatalf("RoleID = %q, want new-role-id", model.RoleID.ValueString())
	}
	if !reflect.DeepEqual(requests, []string{
		"GET /api/v1/orgs/current/roles",
		"GET /api/v1/workspaces/current/members/active",
		"GET /api/v1/workspaces/current/members/pending",
		"PATCH /api/v1/workspaces/current/members/workspace-identity-id",
		"GET /api/v1/workspaces/current/members/active",
		"GET /api/v1/workspaces/current/members/pending",
	}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestWorkspaceMembershipDeleteUsesPendingEndpoint(t *testing.T) {
	requests := make([]string, 0, 3)
	resource := newWorkspaceMembershipResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		assertWorkspaceTenantHeader(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/current/members/active":
			writeJSON(t, w, []memberIdentityAPI{})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/current/members/pending":
			writeJSON(t, w, []pendingIdentityAPI{{
				ID:    "pending-id",
				Email: "alice@langchain.dev",
			}})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/workspaces/current/members/pending-id/pending":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))

	err := resource.deleteWorkspaceMembership(context.Background(), workspaceMembershipResourceModel{
		WorkspaceID: types.StringValue("workspace-id"),
		Email:       types.StringValue("alice@langchain.dev"),
	})
	if err != nil {
		t.Fatalf("deleteWorkspaceMembership returned error: %v", err)
	}
	if !reflect.DeepEqual(requests, []string{
		"GET /api/v1/workspaces/current/members/active",
		"GET /api/v1/workspaces/current/members/pending",
		"DELETE /api/v1/workspaces/current/members/pending-id/pending",
	}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func newWorkspaceMembershipResourceWithServer(t *testing.T, handler http.Handler) *WorkspaceMembershipResource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &WorkspaceMembershipResource{
		client: langsmith.NewClient(
			option.WithBaseURL(server.URL),
			option.WithAPIKey("test-key"),
		),
	}
}

func assertWorkspaceTenantHeader(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("X-Tenant-Id"); got != "workspace-id" {
		t.Fatalf("X-Tenant-Id = %q, want %q", got, "workspace-id")
	}
}
