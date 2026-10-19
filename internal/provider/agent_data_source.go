package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/langchain-ai/langsmith-go"
)

var _ datasource.DataSource = &AgentDataSource{}

func NewAgentDataSource() datasource.DataSource {
	return &AgentDataSource{}
}

type AgentDataSource struct {
	client *langsmith.Client
}

func (d *AgentDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent"
}

func (d *AgentDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a workspace-scoped LangSmith agent and its environments.",
		Attributes: map[string]schema.Attribute{
			"agent_id":     schema.StringAttribute{Required: true, MarkdownDescription: "Workspace-unique agent identifier."},
			"workspace_id": schema.StringAttribute{Optional: true, MarkdownDescription: "Workspace (tenant) ID. Defaults to the provider workspace."},
			"id":           schema.StringAttribute{Computed: true, MarkdownDescription: "Agent identifier (same as agent_id)."},
			"name":         schema.StringAttribute{Computed: true, MarkdownDescription: "Agent display name."},
			"source_type":  schema.StringAttribute{Computed: true, MarkdownDescription: "Agent source type."},
			"agent_type":   schema.StringAttribute{Computed: true, MarkdownDescription: "Agent type."},
			"created_at":   schema.StringAttribute{Computed: true, MarkdownDescription: "Creation timestamp."},
			"environments": schema.MapNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Agent environments keyed by environment name.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":                  schema.StringAttribute{Computed: true, MarkdownDescription: "Agent environment UUID."},
					"tracer_session_id":   schema.StringAttribute{Computed: true, MarkdownDescription: "Tracing project UUID."},
					"tracer_session_name": schema.StringAttribute{Computed: true, MarkdownDescription: "Tracing project name."},
				}},
			},
		},
	}
}

func (d *AgentDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *AgentDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data agentResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d.client == nil {
		resp.Diagnostics.AddError("Unable to Read LangSmith Agent", "The LangSmith client is not configured.")
		return
	}
	var result agentAPI
	if err := d.client.Get(ctx, agentResourcePath(data.AgentID.ValueString()), nil, &result, workspaceOpts(data.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Unable to Read LangSmith Agent", err.Error())
		return
	}
	model, err := agentModelFromAPI(result, data)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read LangSmith Agent", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}
