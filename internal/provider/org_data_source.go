package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var _ datasource.DataSource = &OrgDataSource{}

func NewOrgDataSource() datasource.DataSource {
	return &OrgDataSource{}
}

type OrgDataSource struct {
	client *langsmith.Client
}

type orgDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	DisplayName types.String `tfsdk:"display_name"`
	Handle      types.String `tfsdk:"handle"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

func (d *OrgDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_org"
}

func (d *OrgDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the current LangSmith organization from the authenticated provider context.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Organization ID.",
			},
			"display_name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Organization display name.",
			},
			"handle": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Organization handle when returned by the API.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Creation timestamp when returned by the API.",
			},
		},
	}
}

func (d *OrgDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureDataSourceClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *OrgDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var result organizationAPI
	if err := d.client.Get(ctx, "api/v1/orgs/current", nil, &result); err != nil {
		resp.Diagnostics.AddError("Unable to Read LangSmith Organization", err.Error())
		return
	}

	data, err := orgDataSourceModelFromAPI(result)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Decode LangSmith Organization", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func orgDataSourceModelFromAPI(result organizationAPI) (orgDataSourceModel, error) {
	if result.ID == "" {
		return orgDataSourceModel{}, fmt.Errorf("LangSmith did not return an organization ID")
	}

	createdAt := types.StringNull()
	if !result.CreatedAt.IsZero() {
		createdAt = types.StringValue(result.CreatedAt.Format(time.RFC3339))
	}

	return orgDataSourceModel{
		ID:          types.StringValue(result.ID),
		DisplayName: nullableString(result.DisplayName),
		Handle:      nullableString(result.Handle),
		CreatedAt:   createdAt,
	}, nil
}
