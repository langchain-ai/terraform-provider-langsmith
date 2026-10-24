package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	frameworkvalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
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
	DataPlaneID    types.String `tfsdk:"data_plane_id"`
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
			"data_plane_id": schema.StringAttribute{
				Optional: true,
				Validators: []frameworkvalidator.String{stringvalidator.RegexMatches(
					regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`), "must be a UUID",
				)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "Data plane ID for workspace creation. The provider resolves its API URL through the configured endpoint and creates the workspace through that data plane. Omit to use the provider endpoint. Changing this value requires replacement.",
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

	created, err := r.createWorkspace(ctx, plan)
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
	// The computed URL may be unknown in the plan. Route using refreshed state.
	plan.DataPlaneURL = state.DataPlaneURL
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
	if err := r.deleteWorkspace(ctx, state.ID.ValueString(), state.DataPlaneURL); err != nil {
		if isLangSmithNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to Delete LangSmith Workspace", err.Error())
		return
	}
}

func (r *WorkspaceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") {
		resp.Diagnostics.AddError("Invalid Workspace Import ID", "Use workspace_id or workspace_id/data_plane_id.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0])...)
	if len(parts) == 2 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("data_plane_id"), parts[1])...)
	}
}

func (r *WorkspaceResource) createWorkspace(ctx context.Context, plan workspaceResourceModel) (*langsmith.WorkspaceNewResponse, error) {
	var opts []option.RequestOption
	if dataPlaneID := stringValue(plan.DataPlaneID); dataPlaneID != "" {
		var dataPlane struct {
			APIURL string `json:"api_url"`
		}
		if err := r.client.Get(ctx, "api/v1/orgs/current/data-planes/"+url.PathEscape(dataPlaneID), nil, &dataPlane); err != nil {
			return nil, fmt.Errorf("resolve workspace data plane: %w", err)
		}
		var err error
		opts, err = workspaceDataPlaneOptions(dataPlane.APIURL)
		if err != nil {
			return nil, err
		}
	}
	return r.client.Workspaces.New(ctx, workspaceNewParamsFromModel(plan), opts...)
}

// URLs come from the authenticated organization API. Private BYOC endpoints are
// supported, but credentials must only be sent to an absolute HTTPS URL.
func workspaceDataPlaneOptions(rawURL string) ([]option.RequestOption, error) {
	endpoint := normalizeAPIURL(rawURL)
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("workspace data plane must have an absolute HTTPS API URL without credentials, query, or fragment")
	}
	return []option.RequestOption{option.WithBaseURL(endpoint)}, nil
}

func workspaceMutationOptions(workspaceID string, dataPlaneURL types.String) ([]option.RequestOption, error) {
	opts := []option.RequestOption{workspaceTenantOption(workspaceID)}
	if endpoint := stringValue(dataPlaneURL); endpoint != "" {
		routing, err := workspaceDataPlaneOptions(endpoint)
		if err != nil {
			return nil, err
		}
		opts = append(opts, routing...)
	}
	return opts, nil
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
	opts, err := workspaceMutationOptions(workspaceID, plan.DataPlaneURL)
	if err != nil {
		return workspaceResourceModel{}, err
	}
	if _, err := r.client.Workspaces.Update(ctx, workspaceID, langsmith.WorkspaceUpdateParams{
		DisplayName: langsmith.F(plan.DisplayName.ValueString()),
	}, opts...); err != nil {
		return workspaceResourceModel{}, err
	}
	return r.readWorkspace(ctx, workspaceID, plan)
}

func (r *WorkspaceResource) deleteWorkspace(ctx context.Context, workspaceID string, dataPlaneURL types.String) error {
	opts, err := workspaceMutationOptions(workspaceID, dataPlaneURL)
	if err != nil {
		return err
	}
	_, err = r.client.Workspaces.Delete(ctx, workspaceID, opts...)
	return err
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
