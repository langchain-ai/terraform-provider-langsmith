package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

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

const unresolvedDataPlaneMessage = "This BYOC workspace's data_plane_id could not be resolved. Grant organization:read or organization:infrastructure:read access and refresh before changing or deleting the workspace."

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

func (m workspaceResourceModel) hasUnresolvedDataPlane() bool {
	return stringValue(m.DataPlaneURL) != "" && stringValue(m.DataPlaneID) == ""
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
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIf(workspaceDataPlaneRequiresReplace, "Changing a resolved data plane requires replacement.", "Changing a resolved data plane requires replacement."),
				},
				MarkdownDescription: "BYOC data plane ID. The provider sends workspace create, update, and delete requests to this data plane's API URL. Leave unset for a workspace on the control plane. Data plane discovery requires organization:read or organization:infrastructure:read. Import and refresh with only workspace metadata access retain the workspace with a warning, but changes and deletion are blocked until its data plane ID can be resolved.",
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

	opts, err := r.dataPlaneOptions(ctx, stringValue(plan.DataPlaneID))
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("data_plane_id"), "Unable to Resolve LangSmith Data Plane", err.Error())
		return
	}
	created, err := r.client.Workspaces.New(ctx, workspaceNewParamsFromModel(plan), opts...)
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
		if errors.Is(err, errWorkspaceNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		var apiErr *langsmith.Error
		if next.hasUnresolvedDataPlane() && errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden {
			resp.Diagnostics.AddWarning("Unable to Resolve LangSmith Data Plane", unresolvedDataPlaneMessage)
		} else {
			resp.Diagnostics.AddError("Unable to Read LangSmith Workspace", err.Error())
			return
		}
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
	if state.hasUnresolvedDataPlane() {
		resp.Diagnostics.AddError("Unable to Resolve LangSmith Data Plane", unresolvedDataPlaneMessage)
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
	if state.hasUnresolvedDataPlane() {
		resp.Diagnostics.AddError("Unable to Resolve LangSmith Data Plane", unresolvedDataPlaneMessage)
		return
	}
	if err := r.deleteWorkspace(ctx, state.ID.ValueString(), stringValue(state.DataPlaneID)); err != nil {
		if errors.Is(err, errWorkspaceNotFound) {
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
			next := workspaceModelFromListResponse(workspace, previous)
			if next.DataPlaneURL.IsNull() {
				next.DataPlaneID = types.StringNull()
			} else if stringValue(next.DataPlaneID) == "" {
				dataPlaneID, err := r.dataPlaneIDForURL(ctx, workspace.DataPlaneURL)
				if err != nil {
					return next, fmt.Errorf("resolve data plane for workspace %s: %w", workspaceID, err)
				}
				next.DataPlaneID = types.StringValue(dataPlaneID)
			}
			return next, nil
		}
	}
	return workspaceResourceModel{}, fmt.Errorf("%w: %s", errWorkspaceNotFound, workspaceID)
}

func workspaceDataPlaneRequiresReplace(ctx context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	var state workspaceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.hasUnresolvedDataPlane() {
		resp.Diagnostics.AddAttributeError(req.Path, "Unable to Resolve LangSmith Data Plane", unresolvedDataPlaneMessage)
		return
	}
	resp.RequiresReplace = true
}

func (r *WorkspaceResource) updateWorkspace(ctx context.Context, workspaceID string, plan workspaceResourceModel) (workspaceResourceModel, error) {
	opts, err := r.dataPlaneOptions(ctx, stringValue(plan.DataPlaneID))
	if err != nil {
		return workspaceResourceModel{}, err
	}
	if _, err := r.client.Workspaces.Update(ctx, workspaceID, langsmith.WorkspaceUpdateParams{
		DisplayName: langsmith.F(plan.DisplayName.ValueString()),
	}, append(opts, workspaceTenantOption(workspaceID))...); err != nil {
		return workspaceResourceModel{}, err
	}
	return r.readWorkspace(ctx, workspaceID, plan)
}

func (r *WorkspaceResource) deleteWorkspace(ctx context.Context, workspaceID, dataPlaneID string) error {
	opts, err := r.dataPlaneOptions(ctx, dataPlaneID)
	if err != nil {
		return err
	}
	_, err = r.client.Workspaces.Delete(ctx, workspaceID, append(opts, workspaceTenantOption(workspaceID))...)
	if isLangSmithNotFound(err) {
		return fmt.Errorf("%w: %s", errWorkspaceNotFound, workspaceID)
	}
	return err
}

const dataPlanesSummaryPath = "api/v1/orgs/current/data-planes?view=SUMMARY"

type dataPlaneSummary struct {
	ID     string `json:"id"`
	APIURL string `json:"api_url"`
}

// dataPlaneOptions routes a workspace mutation through the data plane, because
// the control plane only assigns a data plane to requests the data plane proxies.
func (r *WorkspaceResource) dataPlaneOptions(ctx context.Context, dataPlaneID string) ([]option.RequestOption, error) {
	if dataPlaneID == "" {
		return nil, nil
	}
	dataPlanes, err := r.listDataPlanes(ctx)
	if err != nil {
		return nil, err
	}
	for _, dataPlane := range dataPlanes {
		if dataPlane.ID == dataPlaneID {
			baseURL, err := dataPlaneBaseURL(dataPlane.APIURL)
			if err != nil {
				return nil, fmt.Errorf("data plane %s: %w", dataPlaneID, err)
			}
			return []option.RequestOption{option.WithBaseURL(baseURL)}, nil
		}
	}
	return nil, fmt.Errorf("data plane %s not found in the current organization", dataPlaneID)
}

func (r *WorkspaceResource) dataPlaneIDForURL(ctx context.Context, apiURL string) (string, error) {
	dataPlanes, err := r.listDataPlanes(ctx)
	if err != nil {
		return "", err
	}
	for _, dataPlane := range dataPlanes {
		if normalizeAPIURL(dataPlane.APIURL) == normalizeAPIURL(apiURL) {
			return dataPlane.ID, nil
		}
	}
	return "", fmt.Errorf("no data plane in the current organization has API URL %s", apiURL)
}

func (r *WorkspaceResource) listDataPlanes(ctx context.Context) ([]dataPlaneSummary, error) {
	var result struct {
		DataPlanes []dataPlaneSummary `json:"data_planes"`
	}
	if err := r.client.Get(ctx, dataPlanesSummaryPath, nil, &result); err != nil {
		return nil, fmt.Errorf("list data planes: %w", err)
	}
	return result.DataPlanes, nil
}

// dataPlaneBaseURL rejects data plane URLs that would expose the API key over
// plain HTTP or carry extra URL parts.
func dataPlaneBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid API URL: %w", err)
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("API URL %q must be an absolute https URL without credentials, query, or fragment", raw)
	}
	return normalizeAPIURL(u.String()), nil
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
