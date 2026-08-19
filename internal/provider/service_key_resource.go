package provider

// Manages a LangSmith Service Key.
// TODO: use the langsmith stainless client for native methods.

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	AccessScope types.String `tfsdk:"access_scope"`
	CreatedAt   types.String `tfsdk:"created_at"`
	CreatedBy   types.String `tfsdk:"created_by"`
	Description types.String `tfsdk:"description"`
	ExpiresAt   types.String `tfsdk:"expires_at"`
	ID          types.String `tfsdk:"id"`
	Key         types.String `tfsdk:"key"`
	LastUsedAt  types.String `tfsdk:"last_used_at"`
	OrgRoleID   types.String `tfsdk:"org_role_id"`
	RoleID      types.String `tfsdk:"role_id"`
	ShortKey    types.String `tfsdk:"short_key"`
	Workspaces  types.List   `tfsdk:"workspaces"`
}

// API model

// serviceKeyCreateAPIRequest is the request for the service key create API.
type serviceKeyCreateAPIRequest struct {
	Description string   `json:"description"`
	ExpiresAt   *string  `json:"expires_at,omitempty"`
	OrgRoleID   *string  `json:"org_role_id,omitempty"`
	RoleID      *string  `json:"role_id,omitempty"`
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

// serviceKeyCreateAPIResponse is the response for the service key create API.
type serviceKeyCreateAPIResponse struct {
	serviceKeyAPIResponse
	Key string `json:"key"`
}

// serviceKeyListAPIResponse is the response for the service key list API.
type serviceKeyListAPIResponse []serviceKeyAPIResponse

// skipping definition for the delete API response.

// data model conversion functions

// serviceKeyModelFromCreateAPIResponse converts the create API response to the data model.
// `workspaces` is not returned by API.
func serviceKeyModelFromCreateAPIResponse(apiResponse serviceKeyCreateAPIResponse) serviceKeyResourceModel {
	model := serviceKeyModelFromAPIResponse(apiResponse.serviceKeyAPIResponse)
	model.Key = types.StringValue(apiResponse.Key)
	return model
}

// serviceKeyModelFromAPIResponse converts the API response to the data model.
func serviceKeyModelFromAPIResponse(apiResponse serviceKeyAPIResponse) serviceKeyResourceModel {
	return serviceKeyResourceModel{
		AccessScope: types.StringPointerValue(apiResponse.AccessScope),
		CreatedAt:   types.StringPointerValue(apiResponse.CreatedAt),
		CreatedBy:   types.StringPointerValue(apiResponse.CreatedBy),
		Description: types.StringValue(apiResponse.Description),
		ExpiresAt:   rfc3339StringValue(apiResponse.ExpiresAt),
		ID:          types.StringValue(apiResponse.ID),
		Key:         types.StringNull(),
		LastUsedAt:  types.StringPointerValue(apiResponse.LastUsedAt),
		OrgRoleID:   types.StringPointerValue(apiResponse.OrgRoleID),
		RoleID:      types.StringPointerValue(apiResponse.RoleID),
		ShortKey:    types.StringValue(apiResponse.ShortKey),
		Workspaces:  types.ListNull(types.StringType),
	}
}

// rfc3339StringValue normalizes API timestamps to RFC3339 UTC (Z), so values like
// "...+00:00" match config written with "...Z".
func rfc3339StringValue(value *string) types.String {
	if value == nil || *value == "" {
		return types.StringNull()
	}
	t, err := time.Parse(time.RFC3339, *value)
	if err != nil {
		// could not parse the timestamp from the API, but return the value anyways.
		return types.StringValue(*value)
	}
	return types.StringValue(t.UTC().Format(time.RFC3339))
}

// Terraform resource methods

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &serviceKeyResource{}
	_ resource.ResourceWithConfigure   = &serviceKeyResource{}
	_ resource.ResourceWithImportState = &serviceKeyResource{}
	_ resource.ResourceWithModifyPlan  = &serviceKeyResource{}
)

// privateKeyFreshImport is a maerker that lets ModifyPlan() know that the plan is from an Import() or not.
const privateKeyFreshImport = "fresh_import"

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
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
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
			// `workspace_names` is not included. It introduces inconsitencies when workspaces are deleted.
			// And we dont not want that a deleted workspace should trigger a replace, or an error because of a mismatch with `workspaces`.
			"workspaces": schema.ListAttribute{
				Description: "The workspace IDs the service key has access to. Omit for organization-wide access. Editing requires replace, and is validated during import.",
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
		OrgRoleID:   config.OrgRoleID.ValueStringPointer(),
		RoleID:      config.RoleID.ValueStringPointer(),
		Workspaces:  workspaces,
	}

	var apiResponse serviceKeyCreateAPIResponse
	if err := r.client.Post(ctx, "api/v1/orgs/current/service-keys", createRequest, &apiResponse); err != nil {
		resp.Diagnostics.AddError("Failed to create service key", err.Error())
		return
	}

	plan := serviceKeyModelFromCreateAPIResponse(apiResponse)
	plan.Workspaces = config.Workspaces // set the workspaces from the config, since API doesn't return this value.

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Read refreshes the Terraform state with the latest data.
func (r *serviceKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	// Get current state
	var state serviceKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiKey, err := r.fetchServiceKeyByID(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read service key", err.Error())
		return
	}
	newState := serviceKeyModelFromAPIResponse(*apiKey)
	newState.Key = state.Key               // set the key to the state value, since API doesn't return this value.
	newState.Workspaces = state.Workspaces // carry over existing value for workspaces, since API doesn't return this value.

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// ImportState imports the resource and sets the Terraform state on success.
func (r *serviceKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Retrieve import ID and save to id attribute
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	if resp.Diagnostics.HasError() {
		return
	}

	// Mark this state as import-produced so ModifyPlan knows to validate
	// workspaces against the config on the first plan following this import.
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, privateKeyFreshImport, []byte("true"))...)
}

// ModifyPlan validates the Import(), so that the config `workspaces` match what the API has.
func (r *serviceKeyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	freshImport, diags := req.Private.GetKey(ctx, privateKeyFreshImport)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() || freshImport == nil {
		return
	}

	// Consume the marker so it never leaks into a later, unrelated plan.
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, privateKeyFreshImport, nil)...)

	var state serviceKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.AccessScope.ValueString() != accessScopeWorkspace {
		return
	}

	apiKey, err := r.fetchServiceKeyByID(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read service key", err.Error())
		return
	}
	workspaceIDs, diags := r.resolveWorkspaceIDsFromNames(ctx, apiKey.WorkspaceNames)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var config serviceKeyResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !config.Workspaces.Equal(workspaceIDs) {
		resp.Diagnostics.AddAttributeError(
			path.Root("workspaces"),
			"Workspace mismatch on import",
			"The configured workspace IDs do not match the workspaces actually granted to this service key.",
		)
		return
	}

	// Confirmed that workspaces config matches the API's,
	// no need to trigger replace.
	resp.RequiresReplace = slices.DeleteFunc(resp.RequiresReplace, func(p path.Path) bool {
		return p.Equal(path.Root("workspaces"))
	})
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *serviceKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan serviceKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	updateRequest := serviceKeyUpdateAPIRequest{
		OrgRoleID: plan.OrgRoleID.ValueStringPointer(),
		RoleId:    plan.RoleID.ValueStringPointer(),
	}
	apiPath := fmt.Sprintf("api/v1/orgs/current/service-keys/%s", plan.ID.ValueString())
	if err := r.client.Patch(ctx, apiPath, updateRequest, nil); err != nil {
		resp.Diagnostics.AddError("Failed to update service key", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
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

// helpers

// fetchServiceKeyByID looks up a service key via the list endpoint (there is no get-by-id endpoint).
func (r *serviceKeyResource) fetchServiceKeyByID(ctx context.Context, id string) (*serviceKeyAPIResponse, error) {
	var apiResponse serviceKeyListAPIResponse
	if err := r.client.Get(ctx, "api/v1/orgs/current/service-keys", nil, &apiResponse); err != nil {
		return nil, err
	}
	for _, key := range apiResponse {
		if key.ID == id {
			return &key, nil
		}
	}
	return nil, fmt.Errorf("service key %q not found", id)
}

// resolveWorkspaceIDsFromNames maps granted workspace names back to IDs, since the API never returns IDs directly.
func (r *serviceKeyResource) resolveWorkspaceIDsFromNames(ctx context.Context, names []string) (types.List, diag.Diagnostics) {
	var diags diag.Diagnostics

	allWorkspaces, err := r.client.Workspaces.List(ctx, langsmith.WorkspaceListParams{IncludeDeleted: langsmith.Bool(true)})
	if err != nil {
		diags.AddError("Failed to look up workspaces", err.Error())
		return types.ListNull(types.StringType), diags
	}
	idByName := make(map[string]string, len(*allWorkspaces))
	for _, ws := range *allWorkspaces {
		idByName[ws.DisplayName] = ws.ID
	}

	ids := make([]string, 0, len(names))
	for _, name := range names {
		id, ok := idByName[name]
		if !ok {
			diags.AddError(
				"Unknown workspace in service key grants",
				fmt.Sprintf("Workspace %q is granted to this service key but does not exist in this organization.", name),
			)
			continue
		}
		ids = append(ids, id)
	}
	if diags.HasError() {
		return types.ListNull(types.StringType), diags
	}

	workspaceIDs, listDiags := types.ListValueFrom(ctx, types.StringType, ids)
	diags.Append(listDiags...)
	return workspaceIDs, diags
}
