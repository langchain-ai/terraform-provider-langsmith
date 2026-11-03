package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

var (
	_ resource.Resource                = &AgentResource{}
	_ resource.ResourceWithImportState = &AgentResource{}
)

func NewAgentResource() resource.Resource { return &AgentResource{} }

type AgentResource struct{ client *langsmith.Client }

type agentModel struct {
	ID           types.String `tfsdk:"id"`
	AgentID      types.String `tfsdk:"agent_id"`
	WorkspaceID  types.String `tfsdk:"workspace_id"`
	Name         types.String `tfsdk:"name"`
	SourceType   types.String `tfsdk:"source_type"`
	AgentType    types.String `tfsdk:"agent_type"`
	OwnerID      types.String `tfsdk:"owner_id"`
	CreatedAt    types.String `tfsdk:"created_at"`
	Environments types.Map    `tfsdk:"environments"`
}

var agentEnvironmentType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"id":                  types.StringType,
	"tracer_session_id":   types.StringType,
	"tracer_session_name": types.StringType,
}}

type agentEnvironmentModel struct {
	ID                types.String `tfsdk:"id"`
	TracerSessionID   types.String `tfsdk:"tracer_session_id"`
	TracerSessionName types.String `tfsdk:"tracer_session_name"`
}

func (r *AgentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent"
}

func (r *AgentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a LangSmith agent. Its environments and tracing projects are created automatically.\n\nDeleting or replacing an agent deletes its environments and can also delete associated runtimes, tracing projects, and traces.",
		Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "Public agent ID, equal to agent_id."},
			"agent_id":     schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`), "must be 1–63 lowercase letters, digits, or hyphens, starting with a letter and ending with a letter or digit")}, MarkdownDescription: "Unique agent ID within the workspace. Changing it replaces the agent."},
			"workspace_id": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()}, Validators: []validator.String{nonEmptyStringValidator{}}, MarkdownDescription: "Workspace override. When omitted, resolves the provider workspace and saves it in state. Changing it replaces the agent."},
			"name":         schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep, Validators: []validator.String{agentNameValidator{}}, MarkdownDescription: "Display name. Defaults to agent_id on create; when omitted on an existing agent, preserves its current value."},
			"source_type":  schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep, Validators: []validator.String{stringvalidator.OneOf("EXTERNAL", "LSD", "MDA", "FLEET")}, MarkdownDescription: "Runtime kind. Defaults to EXTERNAL on create; omission preserves an existing value. Only EXTERNAL agents without runtime bindings can change source type. Unsupported transitions fail without replacing the agent."},
			"agent_type":   schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep, Validators: []validator.String{stringvalidator.OneOf("CUSTOM", "CODING")}, MarkdownDescription: "Classification: CUSTOM or CODING. Defaults to CUSTOM on create; omission preserves an existing value. Changes require workspace settings management permission."},
			"owner_id":     schema.StringAttribute{Computed: true, MarkdownDescription: "Owner user ID, when present."},
			"created_at":   schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "Agent creation timestamp."},
			"environments": schema.MapAttribute{Computed: true, ElementType: agentEnvironmentType, PlanModifiers: []planmodifier.Map{mapplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Visible environments keyed by LOCAL, DEVELOPMENT, STAGING, or PRODUCTION. Existing agents may have fewer environments."},
		},
	}
}

func (r *AgentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*langsmith.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", fmt.Sprintf("Expected *langsmith.Client, got %T", req.ProviderData))
	}
}

func (r *AgentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data agentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(data.resolveWorkspace(ctx, r.client)...)
	if resp.Diagnostics.HasError() {
		return
	}
	params := langsmith.AgentNewParams{ID: langsmith.String(data.AgentID.ValueString())}
	if !data.Name.IsUnknown() && !data.Name.IsNull() {
		params.Name = langsmith.String(data.Name.ValueString())
	}
	if !data.SourceType.IsUnknown() && !data.SourceType.IsNull() {
		params.SourceType = langsmith.F(langsmith.AgentNewParamsSourceType(data.SourceType.ValueString()))
	}
	if !data.AgentType.IsUnknown() && !data.AgentType.IsNull() {
		params.AgentType = langsmith.F(langsmith.AgentNewParamsAgentType(data.AgentType.ValueString()))
	}
	agent, err := r.client.Agents.New(ctx, params, append(workspaceOpts(data.WorkspaceID), option.WithMaxRetries(0))...)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Agent", err.Error()+"\nIf the agent already exists, import it. If the request outcome is uncertain, check the exact agent ID before retrying.")
		return
	}
	resp.Diagnostics.Append(data.setAgent(ctx, agent)...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}
}

func (r *AgentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data agentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(data.resolveWorkspace(ctx, r.client)...)
	if resp.Diagnostics.HasError() {
		return
	}
	agent, err := r.client.Agents.Get(ctx, data.AgentID.ValueString(), langsmith.AgentGetParams{}, workspaceOpts(data.WorkspaceID)...)
	if err != nil {
		if isAgentNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Agent", err.Error())
		return
	}
	resp.Diagnostics.Append(data.setAgent(ctx, agent)...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}
}

func (r *AgentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data, state agentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	params := langsmith.AgentUpdateParams{}
	if !data.Name.Equal(state.Name) {
		params.Name = langsmith.String(data.Name.ValueString())
	}
	if !data.SourceType.Equal(state.SourceType) {
		params.SourceType = langsmith.F(langsmith.AgentUpdateParamsSourceType(data.SourceType.ValueString()))
	}
	if !data.AgentType.Equal(state.AgentType) {
		params.AgentType = langsmith.F(langsmith.AgentUpdateParamsAgentType(data.AgentType.ValueString()))
	}
	agent, err := r.client.Agents.Update(ctx, state.AgentID.ValueString(), params, workspaceOpts(state.WorkspaceID)...)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Agent", err.Error())
		return
	}
	// Scalar updates do not change environments. PATCH may include environments
	// hidden by GET's read permissions; refresh their visible state on Read.
	environments := data.Environments
	resp.Diagnostics.Append(data.setAgent(ctx, agent)...)
	data.Environments = environments
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}
}

func (r *AgentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data agentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Agents.Delete(ctx, data.AgentID.ValueString(), workspaceOpts(data.WorkspaceID)...); err != nil && !isAgentNotFound(err) {
		resp.Diagnostics.AddError("Unable to Delete LangSmith Agent", err.Error())
	}
}

func (r *AgentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") {
		resp.Diagnostics.AddError("Invalid Agent Import ID", "Use <agent_id> or <workspace_id>/<agent_id>.")
		return
	}
	if len(parts) == 2 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("workspace_id"), parts[0])...)
	}
	id := parts[len(parts)-1]
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("agent_id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

func (m *agentModel) resolveWorkspace(ctx context.Context, client *langsmith.Client) diag.Diagnostics {
	var diagnostics diag.Diagnostics
	if !m.WorkspaceID.IsNull() && !m.WorkspaceID.IsUnknown() {
		return diagnostics
	}
	headers, err := client.AuthHeaders(ctx)
	if err != nil {
		diagnostics.AddError("Unable to Resolve Agent Workspace", err.Error())
		return diagnostics
	}
	workspace := headers.Get("X-Tenant-Id")
	if workspace == "" {
		diagnostics.AddError("Missing Agent Workspace", "Set workspace_id on the resource or configure a workspace on the provider, environment, or SDK profile.")
		return diagnostics
	}
	m.WorkspaceID = types.StringValue(workspace)
	return diagnostics
}

func (m *agentModel) setAgent(ctx context.Context, agent *langsmith.Agent) diag.Diagnostics {
	var diagnostics diag.Diagnostics
	if agent == nil || agent.ID == "" || agent.ID != m.AgentID.ValueString() {
		diagnostics.AddError("Invalid LangSmith Agent Response", "The API did not return the requested agent ID.")
		return diagnostics
	}
	m.ID = types.StringValue(agent.ID)
	m.Name = types.StringValue(agent.Name)
	m.SourceType = types.StringValue(string(agent.SourceType))
	m.AgentType = types.StringValue(string(agent.AgentType))
	m.OwnerID = nullableString(agent.OwnerID)
	m.CreatedAt = types.StringValue(agent.CreatedAt.Format(time.RFC3339Nano))
	environments := make(map[string]agentEnvironmentModel, len(agent.Environments))
	for _, env := range agent.Environments {
		environments[string(env.Environment)] = agentEnvironmentModel{ID: types.StringValue(env.ID), TracerSessionID: types.StringValue(env.TracerSessionID), TracerSessionName: types.StringValue(env.TracerSessionName)}
	}
	m.Environments, diagnostics = types.MapValueFrom(ctx, agentEnvironmentType, environments)
	return diagnostics
}

// A routing 404 from an older server must not remove an existing agent from state.
func isAgentNotFound(err error) bool {
	var apiErr *langsmith.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 404 {
		return false
	}
	var problem struct {
		Detail string `json:"detail"`
	}
	return json.Unmarshal([]byte(apiErr.JSON.RawJSON()), &problem) == nil && problem.Detail == "Agent not found"
}

type agentNameValidator struct{}

func (agentNameValidator) Description(context.Context) string {
	return "must contain 1–255 characters without leading or trailing whitespace"
}
func (v agentNameValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v agentNameValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	name := req.ConfigValue.ValueString()
	if name == "" || strings.TrimSpace(name) != name || utf8.RuneCountInString(name) > 255 {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Agent Name", v.Description(ctx))
	}
}
