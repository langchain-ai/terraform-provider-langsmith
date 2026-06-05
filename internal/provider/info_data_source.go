package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/langchain-ai/langsmith-go"
)

var _ datasource.DataSource = &InfoDataSource{}

func NewInfoDataSource() datasource.DataSource {
	return &InfoDataSource{}
}

type InfoDataSource struct {
	client *langsmith.Client
}

type infoDataSourceModel struct {
	ID                    types.String `tfsdk:"id"`
	Version               types.String `tfsdk:"version"`
	LicenseExpirationTime types.String `tfsdk:"license_expiration_time"`
	BatchIngestConfig     types.String `tfsdk:"batch_ingest_config"`
	InstanceFlags         types.String `tfsdk:"instance_flags"`
}

type infoResponse struct {
	Version               string          `json:"version"`
	LicenseExpirationTime *string         `json:"license_expiration_time"`
	BatchIngestConfig     json.RawMessage `json:"batch_ingest_config"`
	InstanceFlags         json.RawMessage `json:"instance_flags"`
}

func (d *InfoDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_info"
}

func (d *InfoDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads LangSmith server information from `/api/v1/info`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Static data source ID.",
			},
			"version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "LangSmith server version.",
			},
			"license_expiration_time": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "LangSmith license expiration time, when present.",
			},
			"batch_ingest_config": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Raw JSON batch ingest configuration, when present.",
			},
			"instance_flags": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Raw JSON instance flags, when present.",
			},
		},
	}
}

func (d *InfoDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*langsmith.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Data Source Configure Type", fmt.Sprintf("Expected *langsmith.Client, got %T", req.ProviderData))
		return
	}
	d.client = client
}

func (d *InfoDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data infoDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var result infoResponse
	if err := d.client.Get(ctx, "api/v1/info", nil, &result); err != nil {
		resp.Diagnostics.AddError("Unable to Read LangSmith Info", err.Error())
		return
	}

	data.ID = types.StringValue("info")
	data.Version = types.StringValue(result.Version)
	data.LicenseExpirationTime = optionalString(result.LicenseExpirationTime)
	data.BatchIngestConfig = rawJSONValue(result.BatchIngestConfig)
	data.InstanceFlags = rawJSONValue(result.InstanceFlags)

	tflog.Trace(ctx, "read LangSmith info")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func optionalString(value *string) types.String {
	if value == nil {
		return types.StringNull()
	}
	return types.StringValue(*value)
}

func rawJSONValue(value json.RawMessage) types.String {
	if len(value) == 0 || string(value) == "null" {
		return types.StringNull()
	}
	return types.StringValue(string(value))
}
