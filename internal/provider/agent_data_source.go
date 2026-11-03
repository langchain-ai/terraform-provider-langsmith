package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/langchain-ai/langsmith-go"
)

var _ datasource.DataSource = &AgentDataSource{}

func NewAgentDataSource() datasource.DataSource { return &AgentDataSource{} }

type AgentDataSource struct{ client *langsmith.Client }

func (d *AgentDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent"
}

func (d *AgentDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads an existing agent and its environments by agent ID.",
		Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{Computed: true, MarkdownDescription: "Public agent ID."},
			"agent_id":     schema.StringAttribute{Required: true, Validators: []validator.String{nonEmptyStringValidator{}}, MarkdownDescription: "Exact public agent ID, not its display name."},
			"workspace_id": schema.StringAttribute{Optional: true, Validators: []validator.String{nonEmptyStringValidator{}}, MarkdownDescription: "Workspace override. When omitted, uses the provider workspace."},
			"name":         schema.StringAttribute{Computed: true, MarkdownDescription: "Display name."},
			"source_type":  schema.StringAttribute{Computed: true, MarkdownDescription: "Runtime kind."},
			"agent_type":   schema.StringAttribute{Computed: true, MarkdownDescription: "Agent classification."},
			"owner_id":     schema.StringAttribute{Computed: true, MarkdownDescription: "Owner user ID, when present."},
			"created_at":   schema.StringAttribute{Computed: true, MarkdownDescription: "Agent creation timestamp."},
			"environments": schema.MapAttribute{Computed: true, ElementType: agentEnvironmentType, MarkdownDescription: "Visible environments keyed by LOCAL, DEVELOPMENT, STAGING, or PRODUCTION."},
		},
	}
}

func (d *AgentDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client, _ = configureDataSourceClient(req.ProviderData, &resp.Diagnostics)
}

func (d *AgentDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data agentModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	agent, err := d.client.Agents.Get(ctx, data.AgentID.ValueString(), langsmith.AgentGetParams{}, workspaceOpts(data.WorkspaceID)...)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read LangSmith Agent", err.Error())
		return
	}
	resp.Diagnostics.Append(data.setAgent(ctx, agent)...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}
}
