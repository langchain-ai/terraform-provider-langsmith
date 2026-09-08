package provider

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	frameworkvalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var _ datasource.DataSource = &DeploymentRevisionsDataSource{}

func NewDeploymentRevisionsDataSource() datasource.DataSource {
	return &DeploymentRevisionsDataSource{}
}

type DeploymentRevisionsDataSource struct {
	client *langsmith.Client
}

type deploymentRevisionsDataSourceModel struct {
	DeploymentID types.String              `tfsdk:"deployment_id"`
	Limit        types.Int64               `tfsdk:"limit"`
	Offset       types.Int64               `tfsdk:"offset"`
	Status       types.String              `tfsdk:"status"`
	Revisions    []deploymentRevisionModel `tfsdk:"revisions"`
	NextOffset   types.Int64               `tfsdk:"next_offset"`
}

type deploymentRevisionsAPI struct {
	Resources []deploymentRevisionAPI `json:"resources"`
	Offset    int64                   `json:"offset"`
}

func (d *DeploymentRevisionsDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deployment_revisions"
}

func (d *DeploymentRevisionsDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists revisions for a deployment.",
		Attributes: map[string]schema.Attribute{
			"deployment_id": schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}, MarkdownDescription: "Deployment ID."},
			"limit":         schema.Int64Attribute{Optional: true, Validators: []frameworkvalidator.Int64{int64validator.Between(1, 100)}, MarkdownDescription: "Maximum number of revisions to return."},
			"offset":        schema.Int64Attribute{Optional: true, Validators: []frameworkvalidator.Int64{int64validator.AtLeast(0)}, MarkdownDescription: "Pagination offset."},
			"status":        schema.StringAttribute{Optional: true, MarkdownDescription: "Comma-separated revision statuses to include."},
			"revisions": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Deployment revisions.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":                     schema.StringAttribute{Computed: true},
					"created_at":             schema.StringAttribute{Computed: true},
					"updated_at":             schema.StringAttribute{Computed: true},
					"status":                 schema.StringAttribute{Computed: true},
					"status_message":         schema.StringAttribute{Computed: true},
					"source":                 schema.StringAttribute{Computed: true},
					"source_revision_config": deploymentRevisionSourceConfigSchema(),
				}},
			},
			"next_offset": schema.Int64Attribute{Computed: true, MarkdownDescription: "Offset to pass to a subsequent request."},
		},
	}
}

func (d *DeploymentRevisionsDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureControlPlaneClient(req.ProviderData, &resp.Diagnostics, "Data Source")
	if !ok {
		return
	}
	d.client = client
}

func (d *DeploymentRevisionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data deploymentRevisionsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := d.listRevisions(ctx, data)
	if err != nil {
		resp.Diagnostics.AddError("Unable to List Deployment Revisions", err.Error())
		return
	}

	data.Revisions = make([]deploymentRevisionModel, 0, len(result.Resources))
	for _, revision := range result.Resources {
		model, diags := deploymentRevisionModelFromAPI(ctx, revision)
		resp.Diagnostics.Append(diags...)
		data.Revisions = append(data.Revisions, model)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	data.NextOffset = types.Int64Value(result.Offset)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (d *DeploymentRevisionsDataSource) listRevisions(ctx context.Context, data deploymentRevisionsDataSourceModel) (deploymentRevisionsAPI, error) {
	query := url.Values{}
	if !data.Limit.IsNull() && !data.Limit.IsUnknown() {
		query.Set("limit", strconv.FormatInt(data.Limit.ValueInt64(), 10))
	}
	if !data.Offset.IsNull() && !data.Offset.IsUnknown() {
		query.Set("offset", strconv.FormatInt(data.Offset.ValueInt64(), 10))
	}
	if !data.Status.IsNull() && !data.Status.IsUnknown() {
		query.Set("status", data.Status.ValueString())
	}
	path := fmt.Sprintf("v2/deployments/%s/revisions", url.PathEscape(data.DeploymentID.ValueString()))
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var result deploymentRevisionsAPI
	err := d.client.Get(ctx, path, nil, &result)
	return result, err
}
