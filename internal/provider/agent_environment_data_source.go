package provider

import (
	"context"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var _ datasource.DataSource = &AgentEnvironmentDataSource{}

func NewAgentEnvironmentDataSource() datasource.DataSource {
	return &AgentEnvironmentDataSource{}
}

type AgentEnvironmentDataSource struct {
	client *langsmith.Client
}

type agentEnvironmentDataSourceModel struct {
	ID                types.String `tfsdk:"id"`
	WorkspaceID       types.String `tfsdk:"workspace_id"`
	AgentID           types.String `tfsdk:"agent_id"`
	Environment       types.String `tfsdk:"environment"`
	TracerSessionID   types.String `tfsdk:"tracer_session_id"`
	TracerSessionName types.String `tfsdk:"tracer_session_name"`
}

func (d *AgentEnvironmentDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent_environment"
}

func (d *AgentEnvironmentDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up an agent environment by UUID. Environments are owned by agents; the API does not expose public environment create, update, or delete operations.",
		Attributes: map[string]schema.Attribute{
			"id":                  schema.StringAttribute{Required: true, MarkdownDescription: "Agent environment UUID."},
			"workspace_id":        schema.StringAttribute{Optional: true, MarkdownDescription: "Workspace (tenant) ID. Defaults to the provider workspace."},
			"agent_id":            schema.StringAttribute{Computed: true, MarkdownDescription: "Owning agent identifier."},
			"environment":         schema.StringAttribute{Computed: true, MarkdownDescription: "Environment name."},
			"tracer_session_id":   schema.StringAttribute{Computed: true, MarkdownDescription: "Tracing project UUID."},
			"tracer_session_name": schema.StringAttribute{Computed: true, MarkdownDescription: "Tracing project name."},
		},
	}
}

func (d *AgentEnvironmentDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *AgentEnvironmentDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data agentEnvironmentDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d.client == nil {
		resp.Diagnostics.AddError("Unable to Read LangSmith Agent Environment", "The LangSmith client is not configured.")
		return
	}
	var result agentEnvironmentAPI
	if err := d.client.Get(ctx, "api/v1/agent-environments/"+url.PathEscape(data.ID.ValueString()), nil, &result, workspaceOpts(data.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Unable to Read LangSmith Agent Environment", err.Error())
		return
	}
	if result.ID == "" {
		resp.Diagnostics.AddError("Unable to Read LangSmith Agent Environment", "LangSmith did not return an agent environment ID.")
		return
	}
	data.ID = types.StringValue(result.ID)
	data.AgentID = types.StringValue(result.AgentID)
	data.Environment = types.StringValue(result.Environment)
	data.TracerSessionID = types.StringValue(result.TracerSessionID)
	data.TracerSessionName = types.StringValue(result.TracerSessionName)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
