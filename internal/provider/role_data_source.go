package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var _ datasource.DataSource = &RoleDataSource{}

func NewOrgRoleDataSource() datasource.DataSource {
	return &RoleDataSource{
		typeNameSuffix: "_org_role",
		accessScope:    accessScopeOrganization,
	}
}

func NewWorkspaceRoleDataSource() datasource.DataSource {
	return &RoleDataSource{
		typeNameSuffix: "_workspace_role",
		accessScope:    accessScopeWorkspace,
	}
}

type RoleDataSource struct {
	client         *langsmith.Client
	typeNameSuffix string
	accessScope    string
}

type roleDataSourceModel struct {
	ID             types.String   `tfsdk:"id"`
	Name           types.String   `tfsdk:"name"`
	DisplayName    types.String   `tfsdk:"display_name"`
	Description    types.String   `tfsdk:"description"`
	OrganizationID types.String   `tfsdk:"organization_id"`
	Permissions    []types.String `tfsdk:"permissions"`
}

func (d *RoleDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + d.typeNameSuffix
}

func (d *RoleDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a LangSmith organization or workspace role.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Role ID.",
			},
			"name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Role machine name.",
			},
			"display_name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Role display name.",
			},
			"description": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Role description.",
			},
			"organization_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Organization ID for the role.",
			},
			"permissions": schema.ListAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Permission names assigned to the role.",
			},
		},
	}
}

func (d *RoleDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureDataSourceClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *RoleDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data roleDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	roles, err := listRoles(ctx, d.client)
	if err != nil {
		resp.Diagnostics.AddError("Unable to List LangSmith Roles", err.Error())
		return
	}

	role, err := findRoleByLookup(roles, stringConfig(data.Name), stringConfig(data.DisplayName), d.accessScope)
	if err != nil {
		resp.Diagnostics.AddError("LangSmith Role Not Found", err.Error())
		return
	}

	data.ID = types.StringValue(role.ID)
	data.Name = types.StringValue(role.Name)
	data.DisplayName = types.StringValue(role.DisplayName)
	data.Description = nullableString(role.Description)
	data.OrganizationID = nullableString(role.OrganizationID)
	data.Permissions = stringListValue(role.Permissions)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func configureDataSourceClient(providerData any, diagnostics interface {
	AddError(summary string, detail string)
}) (*langsmith.Client, bool) {
	if providerData == nil {
		return nil, false
	}

	client, ok := providerData.(*langsmith.Client)
	if !ok {
		diagnostics.AddError("Unexpected Data Source Configure Type", fmt.Sprintf("Expected *langsmith.Client, got %T", providerData))
		return nil, false
	}
	return client, true
}

func stringListValue(values []string) []types.String {
	result := make([]types.String, 0, len(values))
	for _, value := range values {
		result = append(result, types.StringValue(value))
	}
	return result
}
