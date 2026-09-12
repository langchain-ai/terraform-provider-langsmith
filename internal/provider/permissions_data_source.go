package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var _ datasource.DataSource = &PermissionsDataSource{}

func NewPermissionsDataSource() datasource.DataSource {
	return &PermissionsDataSource{}
}

type PermissionsDataSource struct {
	client *langsmith.Client
}

type permissionsDataSourceModel struct {
	ID          types.String                           `tfsdk:"id"`
	Permissions []permissionsDataSourcePermissionModel `tfsdk:"permissions"`
}

type permissionsDataSourcePermissionModel struct {
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	AccessScope types.String `tfsdk:"access_scope"`
}

func (d *PermissionsDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_permissions"
}

func (d *PermissionsDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists LangSmith permissions available for roles.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Static data source ID.",
			},
			"permissions": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "LangSmith permissions available for roles.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Permission name.",
						},
						"description": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Permission description.",
						},
						"access_scope": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Permission access scope.",
						},
					},
				},
			},
		},
	}
}

func (d *PermissionsDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureDataSourceClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *PermissionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data permissionsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var result []permissionAPI
	if err := d.client.Get(ctx, "api/v1/orgs/permissions", nil, &result); err != nil {
		resp.Diagnostics.AddError("Unable to List LangSmith Permissions", err.Error())
		return
	}

	data.ID = types.StringValue("permissions")
	data.Permissions = permissionModelList(result)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func permissionModelList(values []permissionAPI) []permissionsDataSourcePermissionModel {
	result := make([]permissionsDataSourcePermissionModel, 0, len(values))
	for _, value := range values {
		result = append(result, permissionsDataSourcePermissionModel{
			Name:        types.StringValue(value.Name),
			Description: nullableString(value.Description),
			AccessScope: types.StringValue(value.AccessScope),
		})
	}
	return result
}
