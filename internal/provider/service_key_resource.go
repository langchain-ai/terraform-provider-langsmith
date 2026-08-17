package provider

// Manages a LangSmith Service Key.
// TODO: use the langsmith stainless client for native methods.

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	frameworkvalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

// Terraform Data model

type serviceKeyResourceModel struct {
	AccessScope    string   `tfsdk:"access_scope"`
	CreatedAt      string   `tfsdk:"created_at"`
	CreatedBy      string   `tfsdk:"created_by"`
	Description    string   `tfsdk:"description"`
	ExpiresAt      string   `tfsdk:"expires_at"`
	Id             string   `tfsdk:"id"`
	Key            string   `tfsdk:"key"`
	LastUsedAt     string   `tfsdk:"last_used_at"`
	OrgRoleId      string   `tfsdk:"org_role_id"`
	RoleId         string   `tfsdk:"role_id"`
	ShortKey       string   `tfsdk:"short_key"`
	WorkspaceNames []string `tfsdk:"workspace_names"`
	Workspaces     []string `tfsdk:"workspaces"`
}

// API model

// serviceKeyCreateAPIRequest is the request for the service key create API.
type serviceKeyCreateAPIRequest struct {
	Description string   `json:"description"`
	ExpiresAt   *string  `json:"expires_at,omitempty"`
	OrgRoleId   *string  `json:"org_role_id,omitempty"`
	RoleId      *string  `json:"role_id,omitempty"`
	Workspaces  []string `json:"workspaces,omitempty"`
}

// serviceKeyUpdateAPIRequest is the request for the service key update API.
type serviceKeyUpdateAPIRequest struct {
	OrgRoleID *string `json:"org_role_id,omitempty"`
	RoleId    *string `json:"role_id,omitempty"`
}

// serviceKeyAPIResponse is the base response for the service key API responses.
type serviceKeyAPIResponse struct {
	ID             string   `json:"id"`
	ShortKey       string   `json:"short_key"`
	Description    string   `json:"description"`
	CreatedAt      *string  `json:"created_at"`
	LastUsedAt     *string  `json:"last_used_at"`
	ExpiresAt      *string  `json:"expires_at"`
	WorkspaceNames []string `json:"workspace_names"`
	RoleID         *string  `json:"role_id"`
	OrgRoleID      *string  `json:"org_role_id"`
	AccessScope    *string  `json:"access_scope"`
	CreatedBy      *string  `json:"created_by"`
}

// serviceKeyCreateResponse is the response for the service key create API.
type serviceKeyCreateAPIResponse struct {
	serviceKeyAPIResponse
	Key string `json:"key"`
}

// serviceKeyListAPIResponse is the response for the service key list API.
type serviceKeyListAPIResponse []serviceKeyAPIResponse

// skipping definition for the delete API response.

// serviceKeyUpdateResponse is the response for the service key update API.
type serviceKeyUpdateResponse struct {
	serviceKeyAPIResponse
}

// Terraform resource methods

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource              = &serviceKeyResource{}
	_ resource.ResourceWithConfigure = &serviceKeyResource{}
)

// NewserviceKeyResource is a helper function to simplify the provider implementation.
func NewserviceKeyResource() resource.Resource {
	return &serviceKeyResource{}
}

// serviceKeyResource is the resource implementation.
type serviceKeyResource struct {
	client *langsmith.Client
}

// Metadata returns the resource type name.
func (r *serviceKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_key"
}

// Schema defines the schema for the resource.
func (r *serviceKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a LangSmith service key.",
		Attributes: map[string]schema.Attribute{
			"access_scope": schema.StringAttribute{
				Description: "The access scope of the service key.",
				Computed:    true,
				Validators: []frameworkvalidator.String{
					stringvalidator.OneOf(
						accessScopeOrganization,
						accessScopeWorkspace,
					),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the service key was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_by": schema.StringAttribute{
				Description: "The creator of the service key.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"description": schema.StringAttribute{
				Description: "The description of the service key. Not editable after creation.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"expires_at": schema.StringAttribute{
				Description: "The timestamp when the service key will expire. Not set for permanent keys. Not editable after creation.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"id": schema.StringAttribute{
				Description: "The ID of the service key.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"key": schema.StringAttribute{
				Description: "The key value of the service key. Only available on creation.",
				Computed:    true,
				Sensitive:   true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"last_used_at": schema.StringAttribute{
				Description: "The timestamp when the service key was last used.",
				Computed:    true,
			},
			"org_role_id": schema.StringAttribute{
				Description: "The ID of the organization role for the service key. Omit for workspace-specific access.",
				Optional:    true,
				Computed:    true,
				Validators: []frameworkvalidator.String{
					stringvalidator.ConflictsWith(path.MatchRoot("workspaces")),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"role_id": schema.StringAttribute{
				Description: "Workspace role ID. If omitted, the API defaults to Workspace Admin.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"short_key": schema.StringAttribute{
				Description: "The short, redacted value of the service key.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"workspace_names": schema.ListAttribute{
				Description: "The resolved names of the workspaces the service key has access to.",
				ElementType: types.StringType,
				Computed:    true,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
			},
			"workspaces": schema.ListAttribute{
				Description: "The workspace IDs the service key has access to. Omit for organization-wide access. Not editable after creation.",
				ElementType: types.StringType,
				Optional:    true,
				Validators: []frameworkvalidator.List{
					listvalidator.SizeAtLeast(1),
				},
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
					listplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

// Create creates the resource and sets the initial Terraform state.
func (r *serviceKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	// Retrieve values from plan
	var plan serviceKeyResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Create the service key
	serviceKey, err := r.client.CreateServiceKey(ctx, langsmith.CreateServiceKeyRequest{
		Description: plan.Description,
		ExpiresAt:   plan.ExpiresAt,
		OrgRoleId:   plan.OrgRoleId,
		RoleId:      plan.RoleId,
		Workspaces:  plan.Workspaces,
	})
}

// Read refreshes the Terraform state with the latest data.
func (r *serviceKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *serviceKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *serviceKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
}

// Configure adds the provider configured client to the resource.
func (r *serviceKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Add a nil check when handling ProviderData because Terraform
	// sets that data after it calls the ConfigureProvider RPC.
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*langsmith.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", fmt.Sprintf("Expected *langsmith.Client, got %T", req.ProviderData))
		return
	}
	r.client = client
}
