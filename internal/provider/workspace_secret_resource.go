package provider

// Manages a LangSmith Workspace Secret.

// TODO: Add support for write-only when we upgrade the provider 1.11+.

import (
	"context"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

// data models

// workspaceSecretModel maps workspace secret terraform configuration data.
type workspaceSecretModel struct {
	ID          types.String `tfsdk:"id"`
	Key         types.String `tfsdk:"key"`
	Value       types.String `tfsdk:"value"`
	WorkspaceID types.String `tfsdk:"workspace_id"`
}

// workspaceSecretsPath is API path for listing and upserting keys.
const workspaceSecretsPath = "api/v1/workspaces/current/secrets"

// workspaceSecretUpsertAPI is the request body for an upsert. Use a nil value
// to delete a secret.
type workspaceSecretUpsertAPI struct {
	Key   string  `json:"key"`
	Value *string `json:"value"`
}

// workspaceSecretKeyAPI is the API shape for one element in the read response body.
type workspaceSecretKeyAPI struct {
	Key string `json:"key"`
}

// Terraform resource implementation

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &workspaceSecretResource{}
	_ resource.ResourceWithConfigure   = &workspaceSecretResource{}
	_ resource.ResourceWithImportState = &workspaceSecretResource{}
)

// NewWorkspaceSecretResource is a helper function to simplify the provider implementation.
func NewWorkspaceSecretResource() resource.Resource {
	return &workspaceSecretResource{}
}

// workspaceSecretResource is the resource implementation.
type workspaceSecretResource struct {
	client *langsmith.Client
}

// Metadata returns the resource type name.
func (r *workspaceSecretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workspace_secret"
}

// Schema defines the schema for the resource.
func (r *workspaceSecretResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a workspace secret.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The ID of the workspace secret, which is its key.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"key": schema.StringAttribute{
				MarkdownDescription: "The secret's name, for example `OPENAI_API_KEY`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"value": schema.StringAttribute{
				Description: "The secret value.",
				Required:    true,
				Sensitive:   true,
			},
			"workspace_id": schema.StringAttribute{
				Description: "Workspace (tenant) ID that owns this secret. Defaults to the workspace configured on " +
					"the provider block.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

// Create creates the resource and sets the initial Terraform state.
func (r *workspaceSecretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan workspaceSecretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The API upserts, so a plain apply would silently overwrite a key that
	// already exists, and a later destroy would delete it. Refuse, and point at
	// import as the deliberate way to bring an existing secret under management.
	key := plan.Key.ValueString()
	var existing []workspaceSecretKeyAPI
	if err := r.client.Get(ctx, workspaceSecretsPath, nil, &existing, workspaceOpts(plan.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Failed to create workspace secret", err.Error())
		return
	}
	if slices.ContainsFunc(existing, func(k workspaceSecretKeyAPI) bool { return k.Key == key }) {
		resp.Diagnostics.AddError(
			"Workspace secret already exists",
			fmt.Sprintf("A secret named %q already exists in this workspace. Adopt it with an import block or "+
				"`terraform import <address> %s`. Note that the apply which follows overwrites the existing "+
				"value with this resource block's value, since the API cannot read the current value.", key, key),
		)
		return
	}

	body := []workspaceSecretUpsertAPI{{Key: key, Value: plan.Value.ValueStringPointer()}}
	if err := r.client.Post(ctx, workspaceSecretsPath, body, nil, workspaceOpts(plan.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Failed to create workspace secret", err.Error())
		return
	}
	plan.ID = plan.Key
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the Terraform state with the latest data.
// The API returns key names only, so the only drift this can detect is the
// secret having been deleted outside Terraform.
func (r *workspaceSecretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state workspaceSecretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var keys []workspaceSecretKeyAPI
	if err := r.client.Get(ctx, workspaceSecretsPath, nil, &keys, workspaceOpts(state.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Failed to read workspace secret", err.Error())
		return
	}
	// id and key always hold the same string. Deriving key from id, rather than
	// the reverse, is what lets ImportState pass through id alone.
	state.Key = state.ID
	want := state.ID.ValueString()
	// non-existing secrets should trigger a removal from state in the plan.
	if !slices.ContainsFunc(keys, func(k workspaceSecretKeyAPI) bool { return k.Key == want }) {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ImportState imports the resource state from a secret key.
// The secret `value` cannot be recovered from the API, so it is null in the imported state.
// The next plan therefore shows a single diff, and applying it re-sends the
// value from configuration.
func (r *workspaceSecretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *workspaceSecretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan workspaceSecretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := []workspaceSecretUpsertAPI{{Key: plan.Key.ValueString(), Value: plan.Value.ValueStringPointer()}}
	if err := r.client.Post(ctx, workspaceSecretsPath, body, nil, workspaceOpts(plan.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Failed to update workspace secret", err.Error())
		return
	}
	plan.ID = plan.Key
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *workspaceSecretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state workspaceSecretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A nil value is the delete: see workspaceSecretUpsertAPI.
	body := []workspaceSecretUpsertAPI{{Key: state.Key.ValueString(), Value: nil}}
	if err := r.client.Post(ctx, workspaceSecretsPath, body, nil, workspaceOpts(state.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Failed to delete workspace secret", err.Error())
	}
}

// Configure adds the provider configured client to the resource.
func (r *workspaceSecretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
