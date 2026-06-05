package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	frameworkresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestOrgMembershipSchemaHidesInternalIdentityFields(t *testing.T) {
	resp := &frameworkresource.SchemaResponse{}
	NewOrgMembershipResource().Schema(context.Background(), frameworkresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema returned diagnostics: %v", resp.Diagnostics)
	}

	for _, name := range []string{"identity_id", "pending_identity_id", "ls_user_id", "user_id", "role_name"} {
		if _, ok := resp.Schema.Attributes[name]; ok {
			t.Fatalf("schema exposes internal attribute %q", name)
		}
	}
	for _, name := range []string{"id", "email", "role_id", "status"} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Fatalf("schema is missing public attribute %q", name)
		}
	}
}

func TestOrgMembershipModelFromActiveReadResult(t *testing.T) {
	model := orgMembershipModelFromReadResult(" Alice@LangChain.Dev ", membershipReadResult{
		Status:     membershipStatusActive,
		IdentityID: "identity-id",
		LSUserID:   "ls-user-id",
		UserID:     "user-id",
		RoleID:     "org-role-id",
		RoleName:   "Org Admin",
	})

	if model.ID.ValueString() != "org/current/email/alice@langchain.dev" {
		t.Fatalf("ID = %q", model.ID.ValueString())
	}
	if model.Email.ValueString() != "alice@langchain.dev" {
		t.Fatalf("Email = %q", model.Email.ValueString())
	}
	if model.Status.ValueString() != membershipStatusActive {
		t.Fatalf("Status = %q", model.Status.ValueString())
	}
	if model.RoleID.ValueString() != "org-role-id" {
		t.Fatalf("RoleID = %q", model.RoleID.ValueString())
	}
}

func TestValidateOrgMembershipRequiredFieldsRejectsNonCanonicalEmail(t *testing.T) {
	diagnostics := &attributeErrorRecorder{}
	ok := validateOrgMembershipRequiredFields(orgMembershipResourceModel{
		Email:  types.StringValue(" Alice@LangChain.Dev "),
		RoleID: types.StringValue("org-role-id"),
	}, diagnostics)

	if ok {
		t.Fatalf("validateOrgMembershipRequiredFields returned true for non-canonical email")
	}
	if diagnostics.count != 1 {
		t.Fatalf("diagnostics count = %d, want 1", diagnostics.count)
	}
}

func TestParseOrgMembershipImportID(t *testing.T) {
	email, err := parseOrgMembershipImportID(" Alice@LangChain.Dev ")
	if err != nil {
		t.Fatalf("parseOrgMembershipImportID returned error: %v", err)
	}
	if email != "alice@langchain.dev" {
		t.Fatalf("email = %q, want alice@langchain.dev", email)
	}

	email, err = parseOrgMembershipImportID("org/current/email/Bob@LangChain.Dev")
	if err != nil {
		t.Fatalf("parseOrgMembershipImportID synthetic ID returned error: %v", err)
	}
	if email != "bob@langchain.dev" {
		t.Fatalf("email = %q, want bob@langchain.dev", email)
	}

	if _, err := parseOrgMembershipImportID("org/current/email/"); err == nil {
		t.Fatalf("parseOrgMembershipImportID returned nil error for empty synthetic email")
	}
}

func TestOrgMembershipEnsureCreatesPendingInvite(t *testing.T) {
	var created bool
	requests := make([]string, 0, 6)
	resource := newOrgMembershipResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/roles":
			writeJSON(t, w, []roleAPI{{
				ID:          "org-role-id",
				DisplayName: "Org Admin",
				AccessScope: accessScopeOrganization,
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/members/active":
			assertMemberListQuery(t, r, "alice@langchain.dev")
			writeJSON(t, w, []memberIdentityAPI{})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/members/pending":
			assertMemberListQuery(t, r, "alice@langchain.dev")
			if created {
				writeJSON(t, w, []pendingIdentityAPI{{
					ID:          "pending-id",
					Email:       "alice@langchain.dev",
					OrgRoleID:   "org-role-id",
					OrgRoleName: "Org Admin",
				}})
				return
			}
			writeJSON(t, w, []pendingIdentityAPI{})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/current/members":
			var payload orgInvitePayload
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode invite payload: %v", err)
			}
			if payload.Email != "alice@langchain.dev" || payload.RoleID != "org-role-id" {
				t.Fatalf("payload = %#v", payload)
			}
			created = true
			writeJSON(t, w, pendingIdentityAPI{
				ID:        "pending-id",
				Email:     "alice@langchain.dev",
				OrgRoleID: "org-role-id",
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))

	model, err := resource.ensureOrgMembership(context.Background(), orgMembershipResourceModel{
		Email:  types.StringValue(" Alice@LangChain.Dev "),
		RoleID: types.StringValue("org-role-id"),
	})
	if err != nil {
		t.Fatalf("ensureOrgMembership returned error: %v", err)
	}
	if model.Status.ValueString() != membershipStatusPending {
		t.Fatalf("Status = %q, want pending", model.Status.ValueString())
	}
	if model.RoleID.ValueString() != "org-role-id" {
		t.Fatalf("RoleID = %q, want org-role-id", model.RoleID.ValueString())
	}
	if !reflect.DeepEqual(requests, []string{
		"GET /api/v1/orgs/current/roles",
		"GET /api/v1/orgs/current/members/active",
		"GET /api/v1/orgs/current/members/pending",
		"POST /api/v1/orgs/current/members",
		"GET /api/v1/orgs/current/members/active",
		"GET /api/v1/orgs/current/members/pending",
	}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestOrgMembershipEnsurePatchesActiveRole(t *testing.T) {
	requests := make([]string, 0, 5)
	roleID := "old-role-id"
	resource := newOrgMembershipResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/roles":
			writeJSON(t, w, []roleAPI{{
				ID:          "new-role-id",
				DisplayName: "Org Admin",
				AccessScope: accessScopeOrganization,
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/members/active":
			writeJSON(t, w, []memberIdentityAPI{{
				ID:       "identity-id",
				Email:    "alice@langchain.dev",
				RoleID:   roleID,
				RoleName: "Org Role",
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/members/pending":
			writeJSON(t, w, []pendingIdentityAPI{})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/orgs/current/members/identity-id":
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			if payload["role_id"] != "new-role-id" {
				t.Fatalf("payload = %#v", payload)
			}
			roleID = "new-role-id"
			writeJSON(t, w, memberIdentityAPI{ID: "identity-id"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))

	model, err := resource.ensureOrgMembership(context.Background(), orgMembershipResourceModel{
		Email:  types.StringValue("alice@langchain.dev"),
		RoleID: types.StringValue("new-role-id"),
	})
	if err != nil {
		t.Fatalf("ensureOrgMembership returned error: %v", err)
	}
	if model.Status.ValueString() != membershipStatusActive {
		t.Fatalf("Status = %q, want active", model.Status.ValueString())
	}
	if model.RoleID.ValueString() != "new-role-id" {
		t.Fatalf("RoleID = %q, want new-role-id", model.RoleID.ValueString())
	}
	if !reflect.DeepEqual(requests, []string{
		"GET /api/v1/orgs/current/roles",
		"GET /api/v1/orgs/current/members/active",
		"GET /api/v1/orgs/current/members/pending",
		"PATCH /api/v1/orgs/current/members/identity-id",
		"GET /api/v1/orgs/current/members/active",
		"GET /api/v1/orgs/current/members/pending",
	}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestOrgMembershipEnsureReplacesPendingInviteWhenRoleChanges(t *testing.T) {
	requests := make([]string, 0, 7)
	roleID := "old-role-id"
	resource := newOrgMembershipResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/roles":
			writeJSON(t, w, []roleAPI{{
				ID:          "new-role-id",
				DisplayName: "Org Admin",
				AccessScope: accessScopeOrganization,
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/members/active":
			writeJSON(t, w, []memberIdentityAPI{})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/members/pending":
			writeJSON(t, w, []pendingIdentityAPI{{
				ID:        "pending-id",
				Email:     "alice@langchain.dev",
				OrgRoleID: roleID,
			}})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/orgs/current/members/pending-id/pending":
			roleID = ""
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/current/members":
			var payload orgInvitePayload
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode invite payload: %v", err)
			}
			if payload.Email != "alice@langchain.dev" || payload.RoleID != "new-role-id" {
				t.Fatalf("payload = %#v", payload)
			}
			roleID = "new-role-id"
			writeJSON(t, w, pendingIdentityAPI{ID: "pending-id"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))

	model, err := resource.ensureOrgMembership(context.Background(), orgMembershipResourceModel{
		Email:  types.StringValue("alice@langchain.dev"),
		RoleID: types.StringValue("new-role-id"),
	})
	if err != nil {
		t.Fatalf("ensureOrgMembership returned error: %v", err)
	}
	if model.RoleID.ValueString() != "new-role-id" {
		t.Fatalf("RoleID = %q, want new-role-id", model.RoleID.ValueString())
	}
	if !reflect.DeepEqual(requests, []string{
		"GET /api/v1/orgs/current/roles",
		"GET /api/v1/orgs/current/members/active",
		"GET /api/v1/orgs/current/members/pending",
		"DELETE /api/v1/orgs/current/members/pending-id/pending",
		"POST /api/v1/orgs/current/members",
		"GET /api/v1/orgs/current/members/active",
		"GET /api/v1/orgs/current/members/pending",
	}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestOrgMembershipDeleteUsesPendingEndpoint(t *testing.T) {
	requests := make([]string, 0, 3)
	resource := newOrgMembershipResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/members/active":
			writeJSON(t, w, []memberIdentityAPI{})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/members/pending":
			assertMemberListQuery(t, r, "alice@langchain.dev")
			writeJSON(t, w, []pendingIdentityAPI{{
				ID:    "pending-id",
				Email: "alice@langchain.dev",
			}})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/orgs/current/members/pending-id/pending":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))

	err := resource.deleteOrgMembership(context.Background(), orgMembershipResourceModel{
		Email: types.StringValue("alice@langchain.dev"),
	})
	if err != nil {
		t.Fatalf("deleteOrgMembership returned error: %v", err)
	}
	if !reflect.DeepEqual(requests, []string{
		"GET /api/v1/orgs/current/members/active",
		"GET /api/v1/orgs/current/members/pending",
		"DELETE /api/v1/orgs/current/members/pending-id/pending",
	}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func newOrgMembershipResourceWithServer(t *testing.T, handler http.Handler) *OrgMembershipResource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &OrgMembershipResource{
		client: langsmith.NewClient(
			option.WithBaseURL(server.URL),
			option.WithAPIKey("test-key"),
		),
	}
}

func assertMemberListQuery(t *testing.T, r *http.Request, email string) {
	t.Helper()
	query := r.URL.Query()
	if got := query["emails"]; !slices.Equal(got, []string{email}) {
		t.Fatalf("emails = %#v, want [%q]", got, email)
	}
	if got, want := query.Get("limit"), "100"; got != want {
		t.Fatalf("limit = %q, want %q", got, want)
	}
}
