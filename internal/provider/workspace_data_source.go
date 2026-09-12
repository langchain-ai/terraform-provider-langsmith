package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var _ datasource.DataSource = &WorkspaceDataSource{}

func NewWorkspaceDataSource() datasource.DataSource {
	return &WorkspaceDataSource{}
}

type WorkspaceDataSource struct {
	client *langsmith.Client
}

type workspaceDataSourceModel struct {
	ID             types.String `tfsdk:"id"`
	DisplayName    types.String `tfsdk:"display_name"`
	TenantHandle   types.String `tfsdk:"tenant_handle"`
	OrganizationID types.String `tfsdk:"organization_id"`
	DataPlaneURL   types.String `tfsdk:"data_plane_url"`
	IsPersonal     types.Bool   `tfsdk:"is_personal"`
	IsDeleted      types.Bool   `tfsdk:"is_deleted"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

func (d *WorkspaceDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workspace"
}

func (d *WorkspaceDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a LangSmith workspace by ID or exact display name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Workspace ID.",
			},
			"display_name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Workspace display name.",
			},
			"tenant_handle": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Workspace handle.",
			},
			"organization_id": schema.StringAttribute{
				Computed:            true,
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
				MarkdownDescription: "Creation timestamp.",
			},
		},
	}
}

func (d *WorkspaceDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureDataSourceClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *WorkspaceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data workspaceDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	workspace, err := d.lookupWorkspace(ctx, stringConfig(data.ID), stringConfig(data.DisplayName))
	if err != nil {
		resp.Diagnostics.AddError("LangSmith Workspace Not Found", err.Error())
		return
	}

	next := workspaceDataSourceModelFromListResponse(workspace)
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (d *WorkspaceDataSource) lookupWorkspace(ctx context.Context, id string, displayName string) (langsmith.WorkspaceListResponse, error) {
	if id == "" && displayName == "" {
		return langsmith.WorkspaceListResponse{}, fmt.Errorf("either id or display_name must be provided")
	}

	workspaces, err := d.client.Workspaces.List(ctx, langsmith.WorkspaceListParams{IncludeDeleted: langsmith.Bool(true)})
	if err != nil {
		return langsmith.WorkspaceListResponse{}, err
	}

	var matches []langsmith.WorkspaceListResponse
	for _, workspace := range *workspaces {
		if workspace.IsDeleted {
			continue
		}
		if id != "" && workspace.ID != id {
			continue
		}
		if displayName != "" && workspace.DisplayName != displayName {
			continue
		}
		matches = append(matches, workspace)
	}

	switch len(matches) {
	case 0:
		return langsmith.WorkspaceListResponse{}, fmt.Errorf("no active workspace matched id %q and display_name %q", id, displayName)
	case 1:
		return matches[0], nil
	default:
		return langsmith.WorkspaceListResponse{}, fmt.Errorf("multiple active workspaces matched id %q and display_name %q; specify id", id, displayName)
	}
}

func workspaceDataSourceModelFromListResponse(workspace langsmith.WorkspaceListResponse) workspaceDataSourceModel {
	return workspaceDataSourceModel{
		ID:             types.StringValue(workspace.ID),
		DisplayName:    types.StringValue(workspace.DisplayName),
		TenantHandle:   nullableString(workspace.TenantHandle),
		OrganizationID: nullableString(workspace.OrganizationID),
		DataPlaneURL:   nullableString(workspace.DataPlaneURL),
		IsPersonal:     types.BoolValue(workspace.IsPersonal),
		IsDeleted:      types.BoolValue(workspace.IsDeleted),
		CreatedAt:      timeValue(workspace.CreatedAt),
	}
}
