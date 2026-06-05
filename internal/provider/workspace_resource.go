package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	_ resource.Resource                = &WorkspaceResource{}
	_ resource.ResourceWithImportState = &WorkspaceResource{}
)

var errWorkspaceNotFound = errors.New("workspace not found")

func NewWorkspaceResource() resource.Resource {
	return &WorkspaceResource{}
}

type WorkspaceResource struct {
	client *langsmith.Client
}

type workspaceResourceModel struct {
	ID             types.String `tfsdk:"id"`
	DisplayName    types.String `tfsdk:"display_name"`
	TenantHandle   types.String `tfsdk:"tenant_handle"`
	OrganizationID types.String `tfsdk:"organization_id"`
	DataPlaneURL   types.String `tfsdk:"data_plane_url"`
	IsPersonal     types.Bool   `tfsdk:"is_personal"`
	IsDeleted      types.Bool   `tfsdk:"is_deleted"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

func (r *WorkspaceResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workspace"
}

func (r *WorkspaceResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a LangSmith workspace.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Workspace ID. Leave unset to let LangSmith generate one.",
			},
			"display_name": schema.StringAttribute{
				Required:            true,
				Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
				MarkdownDescription: "Workspace display name.",
			},
			"tenant_handle": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Workspace handle. LangSmith only accepts this on create.",
			},
			"organization_id": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Organization ID.",
			},
			"data_plane_url": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Workspace data plane URL when returned by the API.",
			},
			"is_personal": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether this is a personal workspace.",
			},
			"is_deleted": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether this workspace is deleted.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Creation timestamp.",
			},
		},
	}
}

func (r *WorkspaceResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *WorkspaceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan workspaceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !validateWorkspaceResourceRequiredFields(plan, &resp.Diagnostics) {
		return
	}

	created, err := r.client.Workspaces.New(ctx, workspaceNewParamsFromModel(plan))
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Workspace", err.Error())
		return
	}
	next, err := r.readWorkspace(ctx, created.ID, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Created LangSmith Workspace", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *WorkspaceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state workspaceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	next, err := r.readWorkspace(ctx, state.ID.ValueString(), state)
	if err != nil {
		if errors.Is(err, errWorkspaceNotFound) || isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Workspace", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *WorkspaceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan workspaceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state workspaceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !validateWorkspaceResourceRequiredFields(plan, &resp.Diagnostics) {
		return
	}

	workspaceID := firstNonEmpty(state.ID.ValueString(), plan.ID.ValueString())
	if workspaceID == "" {
		resp.Diagnostics.AddError("Unable to Update LangSmith Workspace", "Missing workspace ID in plan and state.")
		return
	}
	next, err := r.updateWorkspace(ctx, workspaceID, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Workspace", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *WorkspaceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state workspaceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.client.Workspaces.Delete(ctx, state.ID.ValueString()); err != nil {
		if isLangSmithNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to Delete LangSmith Workspace", err.Error())
		return
	}
}

func (r *WorkspaceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *WorkspaceResource) readWorkspace(ctx context.Context, workspaceID string, previous workspaceResourceModel) (workspaceResourceModel, error) {
	workspaces, err := r.client.Workspaces.List(ctx, langsmith.WorkspaceListParams{IncludeDeleted: langsmith.Bool(true)})
	if err != nil {
		return workspaceResourceModel{}, err
	}
	for _, workspace := range *workspaces {
		if workspace.ID == workspaceID {
			if workspace.IsDeleted {
				return workspaceResourceModel{}, fmt.Errorf("%w: %s", errWorkspaceNotFound, workspaceID)
			}
			return workspaceModelFromListResponse(workspace, previous), nil
		}
	}
	return workspaceResourceModel{}, fmt.Errorf("%w: %s", errWorkspaceNotFound, workspaceID)
}

func (r *WorkspaceResource) updateWorkspace(ctx context.Context, workspaceID string, plan workspaceResourceModel) (workspaceResourceModel, error) {
	if _, err := r.client.Workspaces.Update(ctx, workspaceID, langsmith.WorkspaceUpdateParams{
		DisplayName: langsmith.F(plan.DisplayName.ValueString()),
	}); err != nil {
		return workspaceResourceModel{}, err
	}
	return r.readWorkspace(ctx, workspaceID, plan)
}

func workspaceNewParamsFromModel(data workspaceResourceModel) langsmith.WorkspaceNewParams {
	params := langsmith.WorkspaceNewParams{
		DisplayName: langsmith.F(data.DisplayName.ValueString()),
	}
	if value := stringValue(data.ID); value != "" {
		params.ID = langsmith.F(value)
	}
	if value := stringValue(data.TenantHandle); value != "" {
		params.TenantHandle = langsmith.F(value)
	}
	return params
}

func workspaceModelFromListResponse(workspace langsmith.WorkspaceListResponse, previous workspaceResourceModel) workspaceResourceModel {
	next := previous
	next.ID = types.StringValue(workspace.ID)
	next.DisplayName = types.StringValue(workspace.DisplayName)
	next.TenantHandle = nullableString(workspace.TenantHandle)
	next.OrganizationID = nullableString(workspace.OrganizationID)
	next.DataPlaneURL = nullableString(workspace.DataPlaneURL)
	next.IsPersonal = types.BoolValue(workspace.IsPersonal)
	next.IsDeleted = types.BoolValue(workspace.IsDeleted)
	next.CreatedAt = timeValue(workspace.CreatedAt)
	return next
}

func validateWorkspaceResourceRequiredFields(data workspaceResourceModel, diagnostics interface {
	AddAttributeError(attributePath path.Path, summary string, detail string)
}) bool {
	if stringConfig(data.DisplayName) == "" {
		diagnostics.AddAttributeError(path.Root("display_name"), "Invalid Workspace Display Name", "display_name must not be empty.")
		return false
	}
	return true
}

func timeValue(value time.Time) types.String {
	if value.IsZero() {
		return types.StringNull()
	}
	return types.StringValue(value.UTC().Format(time.RFC3339))
}
