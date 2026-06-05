package provider

import (
	"slices"
	"testing"
)

func TestNormalizeMembershipEmail(t *testing.T) {
	got := normalizeMembershipEmail("  Alice@LangChain.Dev  ")
	if got != "alice@langchain.dev" {
		t.Fatalf("normalizeMembershipEmail() = %q, want %q", got, "alice@langchain.dev")
	}
}

func TestMembershipIDsUseNormalizedEmail(t *testing.T) {
	if got, want := orgMembershipID(" Alice@LangChain.Dev "), "org/current/email/alice@langchain.dev"; got != want {
		t.Fatalf("orgMembershipID() = %q, want %q", got, want)
	}
	if got, want := workspaceMembershipID("workspace-1", " Alice@LangChain.Dev "), "workspace/workspace-1/email/alice@langchain.dev"; got != want {
		t.Fatalf("workspaceMembershipID() = %q, want %q", got, want)
	}
}

func TestMemberListQueryURLQuery(t *testing.T) {
	values := memberListQuery{
		Limit:      100,
		Offset:     25,
		Emails:     []string{" Alice@LangChain.Dev ", "", "bob@langchain.dev"},
		SortBy:     "email",
		SortByDesc: true,
	}.URLQuery()

	if got, want := values.Get("limit"), "100"; got != want {
		t.Fatalf("limit = %q, want %q", got, want)
	}
	if got, want := values.Get("offset"), "25"; got != want {
		t.Fatalf("offset = %q, want %q", got, want)
	}
	if got, want := values["emails"], []string{"alice@langchain.dev", "bob@langchain.dev"}; !slices.Equal(got, want) {
		t.Fatalf("emails = %q, want %q", got, want)
	}
	if got, want := values.Get("sort_by"), "email"; got != want {
		t.Fatalf("sort_by = %q, want %q", got, want)
	}
	if got, want := values.Get("sort_by_desc"), "true"; got != want {
		t.Fatalf("sort_by_desc = %q, want %q", got, want)
	}

	if values := (memberListQuery{}).URLQuery(); len(values) != 0 {
		t.Fatalf("zero-value query encoded unexpected values: %v", values)
	}
}

func TestFindRoleByIDValidatesScope(t *testing.T) {
	roles := []roleAPI{{
		ID:          "role-1",
		Name:        "ORGANIZATION_USER",
		DisplayName: "Organization User",
		AccessScope: "organization",
	}}

	role, err := findRoleByID(roles, "role-1", "organization")
	if err != nil {
		t.Fatalf("findRoleByID returned error: %v", err)
	}
	if role.Name != "ORGANIZATION_USER" {
		t.Fatalf("role.Name = %q, want ORGANIZATION_USER", role.Name)
	}

	_, err = findRoleByID(roles, "role-1", "workspace")
	if err == nil {
		t.Fatalf("findRoleByID returned nil error for wrong access scope")
	}
}

func TestFindRoleByLookupRequiresSingleMatch(t *testing.T) {
	roles := []roleAPI{
		{ID: "role-1", Name: "WORKSPACE_USER", DisplayName: "Workspace User", AccessScope: "workspace"},
		{ID: "role-2", Name: "WORKSPACE_USER", DisplayName: "Organization User", AccessScope: "workspace"},
		{ID: "role-3", Name: "WORKSPACE_USER", DisplayName: "Organization User", AccessScope: "organization"},
	}

	_, err := findRoleByLookup(roles, "WORKSPACE_USER", "", "workspace")
	if err == nil {
		t.Fatalf("findRoleByLookup returned nil error for duplicate role name")
	}

	role, err := findRoleByLookup(roles, "", "Organization User", "organization")
	if err != nil {
		t.Fatalf("findRoleByLookup returned error: %v", err)
	}
	if role.ID != "role-3" {
		t.Fatalf("role.ID = %q, want role-3", role.ID)
	}

	role, err = findRoleByLookup(roles, "", "Organization User", "workspace")
	if err != nil {
		t.Fatalf("findRoleByLookup returned error for scoped workspace match: %v", err)
	}
	if role.ID != "role-2" {
		t.Fatalf("role.ID = %q, want role-2", role.ID)
	}
}

func TestFindRoleByLookupRequiresConsistentNameAndDisplayName(t *testing.T) {
	roles := []roleAPI{
		{ID: "role-1", Name: "WORKSPACE_USER", DisplayName: "Workspace User", AccessScope: "workspace"},
		{ID: "role-2", Name: "WORKSPACE_ADMIN", DisplayName: "Workspace Admin", AccessScope: "workspace"},
	}

	role, err := findRoleByLookup(roles, "WORKSPACE_USER", "Workspace User", "workspace")
	if err != nil {
		t.Fatalf("findRoleByLookup returned error: %v", err)
	}
	if role.ID != "role-1" {
		t.Fatalf("role.ID = %q, want role-1", role.ID)
	}

	_, err = findRoleByLookup(roles, "WORKSPACE_USER", "Workspace Admin", "workspace")
	if err == nil {
		t.Fatalf("findRoleByLookup returned nil error for mismatched name and display_name")
	}
}

func TestMembershipReadResultPrefersActive(t *testing.T) {
	active := []memberIdentityAPI{{
		ID:         "active-id",
		Email:      "alice@langchain.dev",
		RoleID:     "role-1",
		RoleName:   "Role One",
		LSUserID:   "ls-user-1",
		UserID:     "user-1",
		IsDisabled: false,
	}}
	pending := []pendingIdentityAPI{{
		ID:       "pending-id",
		Email:    "alice@langchain.dev",
		RoleID:   "role-1",
		RoleName: "Role One",
	}}

	result, ok := membershipByEmail(active, pending, "ALICE@LANGCHAIN.DEV")
	if !ok {
		t.Fatalf("membershipByEmail did not find active membership")
	}
	if result.Status != membershipStatusActive {
		t.Fatalf("Status = %q, want %q", result.Status, membershipStatusActive)
	}
	if result.IdentityID != "active-id" {
		t.Fatalf("IdentityID = %q, want active-id", result.IdentityID)
	}
}

func TestMembershipReadResultSkipsDisabledActive(t *testing.T) {
	active := []memberIdentityAPI{{
		ID:         "disabled-id",
		Email:      "alice@langchain.dev",
		RoleID:     "role-disabled",
		RoleName:   "Disabled Role",
		IsDisabled: true,
	}}
	pending := []pendingIdentityAPI{{
		ID:          "pending-id",
		Email:       "alice@langchain.dev",
		OrgRoleID:   "role-pending",
		OrgRoleName: "Pending Role",
	}}

	result, ok := membershipByEmail(active, pending, " alice@langchain.dev ")
	if !ok {
		t.Fatalf("membershipByEmail did not find pending membership")
	}
	if result.Status != membershipStatusPending {
		t.Fatalf("Status = %q, want %q", result.Status, membershipStatusPending)
	}
	if result.PendingIdentityID != "pending-id" {
		t.Fatalf("PendingIdentityID = %q, want pending-id", result.PendingIdentityID)
	}
	if result.RoleID != "role-pending" {
		t.Fatalf("RoleID = %q, want role-pending", result.RoleID)
	}
	if result.RoleName != "Pending Role" {
		t.Fatalf("RoleName = %q, want Pending Role", result.RoleName)
	}
}

func TestMembershipReadResultPendingOnly(t *testing.T) {
	pending := []pendingIdentityAPI{{
		ID:       "pending-id",
		Email:    "bob@langchain.dev",
		RoleID:   "role-1",
		RoleName: "Role One",
	}}

	result, ok := membershipByEmail(nil, pending, "BOB@LANGCHAIN.DEV")
	if !ok {
		t.Fatalf("membershipByEmail did not find pending membership")
	}
	if result.Status != membershipStatusPending {
		t.Fatalf("Status = %q, want %q", result.Status, membershipStatusPending)
	}
	if result.PendingIdentityID != "pending-id" {
		t.Fatalf("PendingIdentityID = %q, want pending-id", result.PendingIdentityID)
	}
	if result.RoleID != "role-1" {
		t.Fatalf("RoleID = %q, want role-1", result.RoleID)
	}
	if result.RoleName != "Role One" {
		t.Fatalf("RoleName = %q, want Role One", result.RoleName)
	}
}
