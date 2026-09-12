package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var _ datasource.DataSource = &ProjectDataSource{}

func NewProjectDataSource() datasource.DataSource {
	return &ProjectDataSource{}
}

type ProjectDataSource struct {
	client *langsmith.Client
}

type projectDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
}

func (d *ProjectDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (d *ProjectDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a LangSmith tracing project by exact name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Tracing project ID.",
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Tracing project name.",
			},
			"description": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Tracing project description.",
			},
		},
	}
}

func (d *ProjectDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *ProjectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data projectDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := data.Name.ValueString()
	page, err := d.client.Sessions.List(ctx, langsmith.SessionListParams{
		Name:  langsmith.String(name),
		Limit: langsmith.Int(100),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to List LangSmith Projects", err.Error())
		return
	}

	var matches []langsmith.TracerSession
	for _, project := range page.Items {
		if project.Name == name {
			matches = append(matches, project)
		}
	}
	switch len(matches) {
	case 0:
		resp.Diagnostics.AddError("LangSmith Project Not Found", fmt.Sprintf("No tracing project named %q was found.", name))
		return
	case 1:
		data.ID = types.StringValue(matches[0].ID)
		data.Description = nullableString(matches[0].Description)
	default:
		resp.Diagnostics.AddError("Multiple LangSmith Projects Found", fmt.Sprintf("Multiple tracing projects named %q were found; use session_id directly on alert resources.", name))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func nullableString(value string) types.String {
	if value == "" {
		return types.StringNull()
	}
	return types.StringValue(value)
}
