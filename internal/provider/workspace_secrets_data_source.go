package provider

// Lists the key names of a workspace's secrets.

import (
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var _ datasource.DataSource = &WorkspaceSecretsDataSource{}

func NewWorkspaceSecretsDataSource() datasource.DataSource {
	return &WorkspaceSecretsDataSource{}
}

type WorkspaceSecretsDataSource struct {
	client *langsmith.Client
}

type workspaceSecretsDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	Keys        types.Set    `tfsdk:"keys"`
	WorkspaceID types.String `tfsdk:"workspace_id"`
}

func (d *WorkspaceSecretsDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workspace_secrets"
}

func (d *WorkspaceSecretsDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Key names of the secrets set on a workspace.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Static data source ID.",
			},
			"keys": schema.SetAttribute{
				ElementType:         types.StringType,
				Computed:            true,
				MarkdownDescription: "Secret key names, sorted.",
			},
			"workspace_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Workspace (tenant) ID to read. Defaults to the workspace configured on the provider block.",
			},
		},
	}
}

func (d *WorkspaceSecretsDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureDataSourceClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *WorkspaceSecretsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data workspaceSecretsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var apiKeys []workspaceSecretKeyAPI
	if err := d.client.Get(ctx, workspaceSecretsPath, nil, &apiKeys, workspaceOpts(data.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Failed to read workspace secrets", err.Error())
		return
	}

	names := make([]string, 0, len(apiKeys))
	for _, k := range apiKeys {
		names = append(names, k.Key)
	}
	slices.Sort(names)

	keys, diags := types.SetValueFrom(ctx, types.StringType, names)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	data.Keys = keys
	data.ID = types.StringValue("workspace_secrets")

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
