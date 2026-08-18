// Acceptance tests for the langsmith_service_key resource.
//
// These drive real Terraform (plan/apply/destroy) through the provider against a
// live LangSmith, skipped by default.
//
//	LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 \
//	  go test ./internal/provider -run '^TestAccServiceKey' -count=1 -v

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/langchain-ai/langsmith-go"
)

// serviceKeyTestConfig holds Terraform attributes for langsmith_service_key tests.
// Only set fields that should appear in config; omitempty skips the rest.
// This lets us define a terraform config in JSON format, instead of HCL.
type serviceKeyTestConfig struct {
	AccessScope    string   `json:"access_scope,omitempty"`
	CreatedAt      string   `json:"created_at,omitempty"`
	CreatedBy      string   `json:"created_by,omitempty"`
	Description    string   `json:"description,omitempty"`
	ExpiresAt      string   `json:"expires_at,omitempty"`
	ID             string   `json:"id,omitempty"`
	Key            string   `json:"key,omitempty"`
	LastUsedAt     string   `json:"last_used_at,omitempty"`
	OrgRoleID      string   `json:"org_role_id,omitempty"`
	RoleID         string   `json:"role_id,omitempty"`
	ShortKey       string   `json:"short_key,omitempty"`
	WorkspaceNames []string `json:"workspace_names,omitempty"`
	Workspaces     []string `json:"workspaces,omitempty"`
}

// serviceKeyConfig returns Terraform JSON config (accepted by TestStep.Config).
func serviceKeyConfig(cfg serviceKeyTestConfig) string {
	b, err := json.Marshal(map[string]any{
		"resource": map[string]any{
			"langsmith_service_key": map[string]any{
				"test": cfg,
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func listWorkspaceIDs(t *testing.T, ctx context.Context, client *langsmith.Client) ([]string, error) {
	t.Helper()
	type workspace struct {
		ID string `json:"id"`
	}
	type listWorkspacesAPIResponse []workspace
	var response listWorkspacesAPIResponse
	if err := client.Get(ctx, "api/v1/workspaces", nil, &response); err != nil {
		return nil, fmt.Errorf("Unable to Read LangSmith Workspaces: %w", err)
	}
	workspaceIDs := []string{}
	for _, ws := range response {
		workspaceIDs = append(workspaceIDs, ws.ID)
	}
	return workspaceIDs, nil
}

func TestAccServiceKeyRolePermutations(t *testing.T) {
	t.Parallel()
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 to run service key smoke test")
	}

	ctx := context.Background()
	client := langsmith.NewClient()
	roles, err := listRoles(ctx, client)
	if err != nil {
		t.Fatalf("list roles: %v", err)
	}
	wsViewer, err := findRoleByLookup(roles, "WORKSPACE_VIEWER", "", accessScopeWorkspace)
	if err != nil {
		t.Fatalf("lookup WORKSPACE_VIEWER: %v", err)
	}
	wsAdmin, err := findRoleByLookup(roles, "WORKSPACE_ADMIN", "", accessScopeWorkspace)
	if err != nil {
		t.Fatalf("lookup WORKSPACE_ADMIN: %v", err)
	}
	orgViewer, err := findRoleByLookup(roles, "ORGANIZATION_VIEWER", "", accessScopeOrganization)
	if err != nil {
		t.Fatalf("lookup ORGANIZATION_VIEWER: %v", err)
	}
	orgUser, err := findRoleByLookup(roles, "ORGANIZATION_USER", "", accessScopeOrganization)
	if err != nil {
		t.Fatalf("lookup ORGANIZATION_USER: %v", err)
	}
	serviceKeyTestWorkspaceIDs, err := listWorkspaceIDs(t, ctx, client)
	if err != nil {
		t.Fatalf("lookup workspaces: %v", err)
	}
	serviceKeyTestWorkspaceID := serviceKeyTestWorkspaceIDs[0]

	// Full role_id × org_role_id matrix, split by scope:
	// workspace-scoped + org_role_id is invalid (schema ConflictsWith).
	// wantRoleID / wantOrgRoleID are the IDs expected in state (including API defaults).
	cases := []struct {
		name            string
		config          serviceKeyTestConfig
		wantAccessScope string
		wantRoleID      string
		wantOrgRoleID   string
		expectError     string
	}{
		{
			name: "workspace_scoped_default_roles",
			config: serviceKeyTestConfig{
				Description: "tf-acc service key workspace_scoped_default_roles",
				Workspaces:  []string{serviceKeyTestWorkspaceID},
			},
			wantAccessScope: accessScopeWorkspace,
			wantRoleID:      wsAdmin.ID,
		},
		{
			name: "workspace_scoped_with_role_id",
			config: serviceKeyTestConfig{
				Description: "tf-acc service key workspace_scoped_with_role_id",
				Workspaces:  []string{serviceKeyTestWorkspaceID},
				RoleID:      wsViewer.ID,
			},
			wantAccessScope: accessScopeWorkspace,
			wantRoleID:      wsViewer.ID,
		},
		{
			name: "workspace_scoped_with_org_role_id_conflicts",
			config: serviceKeyTestConfig{
				Description: "tf-acc service key workspace_scoped_with_org_role_id_conflicts",
				Workspaces:  []string{serviceKeyTestWorkspaceID},
				OrgRoleID:   orgViewer.ID,
			},
			expectError: `Attribute "workspaces" cannot be specified when "org_role_id" is specified`,
		},
		{
			name: "workspace_scoped_with_role_id_and_org_role_id_conflicts",
			config: serviceKeyTestConfig{
				Description: "tf-acc service key workspace_scoped_with_role_id_and_org_role_id_conflicts",
				Workspaces:  []string{serviceKeyTestWorkspaceID},
				RoleID:      wsViewer.ID,
				OrgRoleID:   orgViewer.ID,
			},
			expectError: `Attribute "workspaces" cannot be specified when "org_role_id" is specified`,
		},
		{
			name: "org_wide_default_roles",
			config: serviceKeyTestConfig{
				Description: "tf-acc service key org_wide_default_roles",
			},
			wantAccessScope: accessScopeOrganization,
			wantRoleID:      wsAdmin.ID,
			wantOrgRoleID:   orgUser.ID,
		},
		{
			name: "org_wide_with_role_id",
			config: serviceKeyTestConfig{
				Description: "tf-acc service key org_wide_with_role_id",
				RoleID:      wsViewer.ID,
			},
			wantAccessScope: accessScopeOrganization,
			wantRoleID:      wsViewer.ID,
			wantOrgRoleID:   orgUser.ID,
		},
		{
			name: "org_wide_with_org_role_id",
			config: serviceKeyTestConfig{
				Description: "tf-acc service key org_wide_with_org_role_id",
				OrgRoleID:   orgViewer.ID,
			},
			wantAccessScope: accessScopeOrganization,
			wantRoleID:      wsAdmin.ID,
			wantOrgRoleID:   orgViewer.ID,
		},
		{
			name: "org_wide_with_role_id_and_org_role_id",
			config: serviceKeyTestConfig{
				Description: "tf-acc service key org_wide_with_role_id_and_org_role_id",
				RoleID:      wsViewer.ID,
				OrgRoleID:   orgViewer.ID,
			},
			wantAccessScope: accessScopeOrganization,
			wantRoleID:      wsViewer.ID,
			wantOrgRoleID:   orgViewer.ID,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			step := resource.TestStep{
				Config: serviceKeyConfig(tc.config),
			}
			if tc.expectError != "" {
				step.ExpectError = regexp.MustCompile(regexp.QuoteMeta(tc.expectError))
			} else {
				checks := []resource.TestCheckFunc{
					resource.TestCheckResourceAttrSet("langsmith_service_key.test", "id"),
					resource.TestCheckResourceAttr("langsmith_service_key.test", "description", tc.config.Description),
					resource.TestCheckResourceAttr("langsmith_service_key.test", "access_scope", tc.wantAccessScope),
					resource.TestCheckResourceAttrSet("langsmith_service_key.test", "short_key"),
					resource.TestCheckResourceAttrSet("langsmith_service_key.test", "key"),
					resource.TestCheckResourceAttrSet("langsmith_service_key.test", "created_at"),
				}

				if len(tc.config.Workspaces) > 0 {
					checks = append(checks,
						resource.TestCheckResourceAttr("langsmith_service_key.test", "workspaces.#", fmt.Sprintf("%d", len(tc.config.Workspaces))),
						resource.TestCheckResourceAttr("langsmith_service_key.test", "workspaces.0", tc.config.Workspaces[0]),
					)
				} else {
					checks = append(checks, resource.TestCheckNoResourceAttr("langsmith_service_key.test", "workspaces"))
				}

				checks = append(checks, resource.TestCheckResourceAttr("langsmith_service_key.test", "role_id", tc.wantRoleID))
				if tc.wantOrgRoleID != "" {
					checks = append(checks, resource.TestCheckResourceAttr("langsmith_service_key.test", "org_role_id", tc.wantOrgRoleID))
				}
				step.Check = resource.ComposeAggregateTestCheckFunc(checks...)
			}

			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
					"langsmith": providerserver.NewProtocol6WithError(New("test")()),
				},
				Steps: []resource.TestStep{
					step,
					// Delete testing automatically occurs in TestCase for successful creates.
				},
			})
		})
	}
}

// TestAccServiceKeyUpdateRoles changes both roles on an org-wide key in place.
func TestAccServiceKeyUpdateRoles(t *testing.T) {
	t.Parallel()
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 to run service key update test")
	}

	ctx := context.Background()
	client := langsmith.NewClient()
	roles, err := listRoles(ctx, client)
	if err != nil {
		t.Fatalf("list roles: %v", err)
	}
	wsViewer, err := findRoleByLookup(roles, "WORKSPACE_VIEWER", "", accessScopeWorkspace)
	if err != nil {
		t.Fatalf("lookup WORKSPACE_VIEWER: %v", err)
	}
	wsAdmin, err := findRoleByLookup(roles, "WORKSPACE_ADMIN", "", accessScopeWorkspace)
	if err != nil {
		t.Fatalf("lookup WORKSPACE_ADMIN: %v", err)
	}
	orgViewer, err := findRoleByLookup(roles, "ORGANIZATION_VIEWER", "", accessScopeOrganization)
	if err != nil {
		t.Fatalf("lookup ORGANIZATION_VIEWER: %v", err)
	}
	orgUser, err := findRoleByLookup(roles, "ORGANIZATION_USER", "", accessScopeOrganization)
	if err != nil {
		t.Fatalf("lookup ORGANIZATION_USER: %v", err)
	}

	// HCL (not JSON): ImportState uses a fresh working dir + Init; JSON configs
	// hit a terraform-plugin-testing .tf/.tf.json filename bug on that path.
	const description = "tf-acc service key update_roles"
	const expiresAt = "2099-12-31T00:00:00Z"
	createCfg := fmt.Sprintf(`
resource "langsmith_service_key" "test" {
  description = %q
}
`, description)
	updateRolesCfg := fmt.Sprintf(`
resource "langsmith_service_key" "test" {
  description = %q
  role_id     = %q
  org_role_id = %q
}
`, description, wsViewer.ID, orgViewer.ID)
	expiresAtCfg := fmt.Sprintf(`
resource "langsmith_service_key" "test" {
  description = %q
  role_id     = %q
  org_role_id = %q
  expires_at  = %q
}
`, description, wsViewer.ID, orgViewer.ID, expiresAt)
	serviceKeyTestWorkspaceIDs, err := listWorkspaceIDs(t, ctx, client)
	if err != nil {
		t.Fatalf("lookup workspaces: %v", err)
	}
	serviceKeyTestWorkspaceID := serviceKeyTestWorkspaceIDs[0]
	convertToWorkspaceScopeCfg := fmt.Sprintf(`
resource "langsmith_service_key" "test" {
  description = %q
  role_id     = %q
  workspaces  = [%q]
  expires_at  = %q
}
`, description, wsViewer.ID, serviceKeyTestWorkspaceID, expiresAt)

	var originalID string
	var newID string
	baseChecks := []resource.TestCheckFunc{
		resource.TestCheckResourceAttrSet("langsmith_service_key.test", "id"),
		resource.TestCheckResourceAttr("langsmith_service_key.test", "description", description),
		resource.TestCheckResourceAttrSet("langsmith_service_key.test", "short_key"),
		resource.TestCheckResourceAttrSet("langsmith_service_key.test", "key"),
		resource.TestCheckResourceAttrSet("langsmith_service_key.test", "created_at"),
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(New("test")()),
		},
		Steps: []resource.TestStep{
			// create
			{
				Config: createCfg,
				Check: resource.ComposeAggregateTestCheckFunc(append(baseChecks,
					resource.TestCheckResourceAttrWith("langsmith_service_key.test", "id", func(value string) error {
						if value == "" {
							return fmt.Errorf("id is empty")
						}
						originalID = value
						return nil
					}),
					resource.TestCheckResourceAttr("langsmith_service_key.test", "access_scope", accessScopeOrganization),
					resource.TestCheckResourceAttr("langsmith_service_key.test", "role_id", wsAdmin.ID),
					resource.TestCheckResourceAttr("langsmith_service_key.test", "org_role_id", orgUser.ID),
					resource.TestCheckNoResourceAttr("langsmith_service_key.test", "workspaces"),
					resource.TestCheckNoResourceAttr("langsmith_service_key.test", "expires_at"),
				)...),
			},
			// import
			{
				ResourceName:            "langsmith_service_key.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"key", "workspace_names"},
			},
			// update roles in place
			{
				Config: updateRolesCfg,
				Check: resource.ComposeAggregateTestCheckFunc(append(baseChecks,
					resource.TestCheckResourceAttrWith("langsmith_service_key.test", "id", func(value string) error {
						if value != originalID {
							return fmt.Errorf("id = %q, want %q (in-place update)", value, originalID)
						}
						return nil
					}),
					resource.TestCheckResourceAttr("langsmith_service_key.test", "access_scope", accessScopeOrganization),
					resource.TestCheckResourceAttr("langsmith_service_key.test", "role_id", wsViewer.ID),
					resource.TestCheckResourceAttr("langsmith_service_key.test", "org_role_id", orgViewer.ID),
					resource.TestCheckNoResourceAttr("langsmith_service_key.test", "workspaces"),
					resource.TestCheckNoResourceAttr("langsmith_service_key.test", "expires_at"),
				)...),
			},
			// expires_at requires replace
			{
				Config: expiresAtCfg,
				Check: resource.ComposeAggregateTestCheckFunc(append(baseChecks,
					resource.TestCheckResourceAttrWith("langsmith_service_key.test", "id", func(value string) error {
						if value == "" {
							return fmt.Errorf("id is empty")
						}
						if value == originalID {
							return fmt.Errorf("id = %q, want replace (new id)", value)
						}
						newID = value
						return nil
					}),
					resource.TestCheckResourceAttr("langsmith_service_key.test", "access_scope", accessScopeOrganization),
					resource.TestCheckResourceAttr("langsmith_service_key.test", "role_id", wsViewer.ID),
					resource.TestCheckResourceAttr("langsmith_service_key.test", "org_role_id", orgViewer.ID),
					resource.TestCheckNoResourceAttr("langsmith_service_key.test", "workspaces"),
					resource.TestCheckResourceAttr("langsmith_service_key.test", "expires_at", expiresAt),
				)...),
			},
			// change to workspace-scoped: workspaces is RequiresReplace, so this forces a replace
			{
				Config: convertToWorkspaceScopeCfg,
				Check: resource.ComposeAggregateTestCheckFunc(append(baseChecks,
					resource.TestCheckResourceAttrWith("langsmith_service_key.test", "id", func(value string) error {
						if value == newID {
							return fmt.Errorf("id = %q, want replace (new id)", value)
						}
						return nil
					}),
					resource.TestCheckResourceAttr("langsmith_service_key.test", "access_scope", accessScopeWorkspace),
					resource.TestCheckResourceAttr("langsmith_service_key.test", "role_id", wsViewer.ID),
					resource.TestCheckNoResourceAttr("langsmith_service_key.test", "org_role_id"),
					resource.TestCheckResourceAttr("langsmith_service_key.test", "workspaces.#", "1"),
					resource.TestCheckResourceAttr("langsmith_service_key.test", "workspaces.0", serviceKeyTestWorkspaceID),
					resource.TestCheckResourceAttr("langsmith_service_key.test", "expires_at", expiresAt),
				)...),
			},
		},
	})
}
