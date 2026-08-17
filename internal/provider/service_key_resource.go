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
	AccessScope    types.String `tfsdk:"access_scope"`
	CreatedAt      types.String `tfsdk:"created_at"`
	CreatedBy      types.String `tfsdk:"created_by"`
	Description    types.String `tfsdk:"description"`
	ExpiresAt      types.String `tfsdk:"expires_at"`
	ID             types.String `tfsdk:"id"`
	Key            types.String `tfsdk:"key"`
	LastUsedAt     types.String `tfsdk:"last_used_at"`
	OrgRoleID      types.String `tfsdk:"org_role_id"`
	RoleID         types.String `tfsdk:"role_id"`
	ShortKey       types.String `tfsdk:"short_key"`
	WorkspaceNames types.List   `tfsdk:"workspace_names"`
	Workspaces     types.List   `tfsdk:"workspaces"`
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
	AccessScope    *string  `json:"access_scope"`
	CreatedAt      *string  `json:"created_at"`
	CreatedBy      *string  `json:"created_by"`
	Description    string   `json:"description"`
	ExpiresAt      *string  `json:"expires_at"`
	ID             string   `json:"id"`
	LastUsedAt     *string  `json:"last_used_at"`
	OrgRoleID      *string  `json:"org_role_id"`
	RoleID         *string  `json:"role_id"`
	ShortKey       string   `json:"short_key"`
	WorkspaceNames []string `json:"workspace_names"`
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

// data model conversion functions

// serviceKeyModelFromAPIResponse converts the API response to the data model.
// `workspaces` is not returned by API, but `workspace_names` is.
func serviceKeyModelFromAPIResponse(ctx context.Context, apiResponse serviceKeyCreateAPIResponse) (serviceKeyResourceModel, error) {
	workspaceNames, diags := types.ListValueFrom(ctx, types.StringType, apiResponse.WorkspaceNames)
	if diags.HasError() {
		return serviceKeyResourceModel{}, fmt.Errorf("failed to convert workspace names to list: %v", diags.Errors())
	}
	return serviceKeyResourceModel{
		AccessScope:    types.StringPointerValue(apiResponse.AccessScope),
		CreatedAt:      types.StringPointerValue(apiResponse.CreatedAt),
		CreatedBy:      types.StringPointerValue(apiResponse.CreatedBy),
		Description:    types.StringValue(apiResponse.Description),
		ExpiresAt:      types.StringPointerValue(apiResponse.ExpiresAt),
		ID:             types.StringValue(apiResponse.ID),
		Key:            types.StringValue(apiResponse.Key),
		LastUsedAt:     types.StringPointerValue(apiResponse.LastUsedAt),
		OrgRoleID:      types.StringPointerValue(apiResponse.OrgRoleID),
		RoleID:         types.StringPointerValue(apiResponse.RoleID),
		ShortKey:       types.StringValue(apiResponse.ShortKey),
		WorkspaceNames: workspaceNames,
	}, nil
}

// Terraform resource methods

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource              = &serviceKeyResource{}
	_ resource.ResourceWithConfigure = &serviceKeyResource{}
)

// NewServiceKeyResource is a helper function to simplify the provider implementation.
func NewServiceKeyResource() resource.Resource {
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
	var config serviceKeyResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var workspaces []string
	resp.Diagnostics.Append(config.Workspaces.ElementsAs(ctx, &workspaces, true)...)
	if resp.Diagnostics.HasError() {
		return
	}
	createRequest := serviceKeyCreateAPIRequest{
		Description: config.Description.ValueString(),
		ExpiresAt:   config.ExpiresAt.ValueStringPointer(),
		OrgRoleId:   config.OrgRoleID.ValueStringPointer(),
		RoleId:      config.RoleID.ValueStringPointer(),
		Workspaces:  workspaces,
	}

	var apiResponse serviceKeyCreateAPIResponse
	if err := r.client.Post(ctx, "api/v1/orgs/current/service-keys", createRequest, &apiResponse); err != nil {
		resp.Diagnostics.AddError("Failed to create service key", err.Error())
		return
	}

	plan, err := serviceKeyModelFromAPIResponse(ctx, apiResponse)
	if err != nil {
		resp.Diagnostics.AddError("Failed to convert API response to model", err.Error())
		return
	}
	plan.Workspaces = config.Workspaces // set the workspaces from the config, since API doesn't return this value.
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Read refreshes the Terraform state with the latest data.
func (r *serviceKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *serviceKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *serviceKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serviceKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiPath := fmt.Sprintf("api/v1/orgs/current/service-keys/%s", state.ID.ValueString())
	if err := r.client.Delete(ctx, apiPath, nil, nil); err != nil && !isLangSmithNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete service key", err.Error())
		return
	}
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
