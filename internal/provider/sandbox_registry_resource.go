package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	frameworkvalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var (
	_ resource.Resource                = &SandboxRegistryResource{}
	_ resource.ResourceWithImportState = &SandboxRegistryResource{}
)

const sandboxRegistriesPath = "api/v2/sandboxes/registries"

func sandboxRegistryResourcePath(name string) string {
	return fmt.Sprintf("%s/%s", sandboxRegistriesPath, name)
}

func NewSandboxRegistryResource() resource.Resource {
	return &SandboxRegistryResource{}
}

type SandboxRegistryResource struct {
	client *langsmith.Client
}

type sandboxRegistryResourceModel struct {
	ID          types.String `tfsdk:"id"`
	WorkspaceID types.String `tfsdk:"workspace_id"`
	Name        types.String `tfsdk:"name"`
	URL         types.String `tfsdk:"url"`
	Username    types.String `tfsdk:"username"`
	Password    types.String `tfsdk:"password"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
	CreatedBy   types.String `tfsdk:"created_by"`
	UpdatedBy   types.String `tfsdk:"updated_by"`
}

// sandboxRegistryPayload is the create/update request body. The same shape is
// sent for both: the API's update is all-or-nothing on credentials, so we always
// send the full set (name, url, username, password).
type sandboxRegistryPayload struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// sandboxRegistryAPI models the registry response. Credentials are never returned
// by the API (stored encrypted server-side), so they are absent here.
type sandboxRegistryAPI struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	CreatedBy string `json:"created_by"`
	UpdatedBy string `json:"updated_by"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (r *SandboxRegistryResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_sandbox_registry"
}

func (r *SandboxRegistryResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a LangSmith sandbox container-image registry (workspace-scoped). Import with `<name>` or `<workspace_id>/<name>`.\n\n" +
			"The registry credentials (`username`/`password`) are write-only: the API never " +
			"returns them, so they are stored in (sensitive) Terraform state and cannot be " +
			"recovered on import. Use encrypted remote state.",
		Attributes: map[string]schema.Attribute{
			"workspace_id": schema.StringAttribute{
				Optional:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "LangSmith workspace (tenant) ID that owns this registry. When unset, the resource uses the workspace configured on the provider block.",
			},
			"id": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Registry ID.",
			},
			"name": schema.StringAttribute{
				Required:            true,
				Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
				MarkdownDescription: "Registry name, unique within the workspace. Renaming updates in place.",
			},
			"url": schema.StringAttribute{
				Required:            true,
				Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
				MarkdownDescription: "Registry URL, e.g. `https://index.docker.io/v1/`.",
			},
			"username": schema.StringAttribute{
				Required:            true,
				Sensitive:           true,
				Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
				MarkdownDescription: "Registry username. Write-only; never returned by the API.",
			},
			"password": schema.StringAttribute{
				Required:            true,
				Sensitive:           true,
				Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
				MarkdownDescription: "Registry password or token. Write-only; never returned by the API.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Creation timestamp.",
			},
			"updated_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Last update timestamp.",
			},
			"created_by": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Actor ID that created the registry.",
			},
			"updated_by": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Actor ID that last updated the registry.",
			},
		},
	}
}

func (r *SandboxRegistryResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *SandboxRegistryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan sandboxRegistryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	model, err := r.createSandboxRegistry(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Sandbox Registry", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *SandboxRegistryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state sandboxRegistryResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	model, err := r.readSandboxRegistry(ctx, state.Name.ValueString(), state, state.WorkspaceID)
	if err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Sandbox Registry", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *SandboxRegistryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan sandboxRegistryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state sandboxRegistryResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	currentName := firstNonEmpty(state.Name.ValueString(), plan.Name.ValueString())
	if currentName == "" {
		resp.Diagnostics.AddError("Unable to Update LangSmith Sandbox Registry", "Missing registry name in plan and state.")
		return
	}

	model, err := r.updateSandboxRegistry(ctx, currentName, plan, plan.WorkspaceID)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Sandbox Registry", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *SandboxRegistryResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state sandboxRegistryResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.deleteSandboxRegistry(ctx, state.Name.ValueString(), state.WorkspaceID); err != nil {
		resp.Diagnostics.AddError("Unable to Delete LangSmith Sandbox Registry", err.Error())
		return
	}
}

func (r *SandboxRegistryResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) == 2 {
		if parts[0] == "" || parts[1] == "" {
			resp.Diagnostics.AddError("Invalid Sandbox Registry Import ID", "Use <name> or <workspace_id>/<name>.")
			return
		}
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("workspace_id"), parts[0])...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), parts[1])...)
		return
	}
	if parts[0] == "" {
		resp.Diagnostics.AddError("Invalid Sandbox Registry Import ID", "Use <name> or <workspace_id>/<name>.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), parts[0])...)
}

func (r *SandboxRegistryResource) createSandboxRegistry(ctx context.Context, plan sandboxRegistryResourceModel) (sandboxRegistryResourceModel, error) {
	var result sandboxRegistryAPI
	if err := r.client.Post(ctx, sandboxRegistriesPath, sandboxRegistryPayloadFromModel(plan), &result, workspaceOpts(plan.WorkspaceID)...); err != nil {
		return sandboxRegistryResourceModel{}, err
	}
	if result.ID == "" {
		return sandboxRegistryResourceModel{}, errors.New("LangSmith did not return a sandbox registry ID")
	}
	return sandboxRegistryModelFromAPI(result, plan), nil
}

func (r *SandboxRegistryResource) readSandboxRegistry(ctx context.Context, name string, previous sandboxRegistryResourceModel, workspaceID ...types.String) (sandboxRegistryResourceModel, error) {
	var result sandboxRegistryAPI
	if err := r.client.Get(ctx, sandboxRegistryResourcePath(name), nil, &result, workspaceOpts(sandboxRegistryWorkspaceID(workspaceID))...); err != nil {
		return sandboxRegistryResourceModel{}, err
	}
	return sandboxRegistryModelFromAPI(result, previous), nil
}

func (r *SandboxRegistryResource) updateSandboxRegistry(ctx context.Context, currentName string, plan sandboxRegistryResourceModel, workspaceID ...types.String) (sandboxRegistryResourceModel, error) {
	var result sandboxRegistryAPI
	if err := r.client.Patch(ctx, sandboxRegistryResourcePath(currentName), sandboxRegistryPayloadFromModel(plan), &result, workspaceOpts(sandboxRegistryWorkspaceID(workspaceID))...); err != nil {
		return sandboxRegistryResourceModel{}, err
	}
	return sandboxRegistryModelFromAPI(result, plan), nil
}

func (r *SandboxRegistryResource) deleteSandboxRegistry(ctx context.Context, name string, workspaceID ...types.String) error {
	if err := r.client.Delete(ctx, sandboxRegistryResourcePath(name), nil, nil, workspaceOpts(sandboxRegistryWorkspaceID(workspaceID))...); err != nil {
		if isLangSmithNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

func sandboxRegistryWorkspaceID(workspaceID []types.String) types.String {
	if len(workspaceID) == 0 {
		return types.StringNull()
	}
	return workspaceID[0]
}

func sandboxRegistryPayloadFromModel(m sandboxRegistryResourceModel) sandboxRegistryPayload {
	return sandboxRegistryPayload{
		Name:     m.Name.ValueString(),
		URL:      m.URL.ValueString(),
		Username: m.Username.ValueString(),
		Password: m.Password.ValueString(),
	}
}

// sandboxRegistryModelFromAPI maps a registry response onto the resource model.
// It starts from the prior model so the write-only credentials (username,
// password), which the API never returns, are preserved.
func sandboxRegistryModelFromAPI(api sandboxRegistryAPI, previous sandboxRegistryResourceModel) sandboxRegistryResourceModel {
	next := previous
	next.ID = types.StringValue(api.ID)
	next.Name = types.StringValue(api.Name)
	next.URL = types.StringValue(api.URL)
	next.CreatedAt = nullableString(api.CreatedAt)
	next.UpdatedAt = nullableString(api.UpdatedAt)
	next.CreatedBy = nullableString(api.CreatedBy)
	next.UpdatedBy = nullableString(api.UpdatedBy)
	return next
}
