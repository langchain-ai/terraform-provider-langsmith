package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var (
	_                              resource.Resource                = &AgentResource{}
	_                              resource.ResourceWithImportState = &AgentResource{}
	agentIDPattern                                                  = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
	agentEnvironmentAttributeTypes                                  = map[string]attr.Type{
		"id":                  types.StringType,
		"tracer_session_id":   types.StringType,
		"tracer_session_name": types.StringType,
	}
)

const agentsPath = "api/v1/agents"

func NewAgentResource() resource.Resource { return &AgentResource{} }

type AgentResource struct{ client *langsmith.Client }

type agentResourceModel struct {
	ID           types.String `tfsdk:"id"`
	AgentID      types.String `tfsdk:"agent_id"`
	WorkspaceID  types.String `tfsdk:"workspace_id"`
	Name         types.String `tfsdk:"name"`
	SourceType   types.String `tfsdk:"source_type"`
	AgentType    types.String `tfsdk:"agent_type"`
	CreatedAt    types.String `tfsdk:"created_at"`
	Environments types.Map    `tfsdk:"environments"`
}

type agentAPI struct {
	ID           string                `json:"id"`
	Name         string                `json:"name"`
	SourceType   string                `json:"source_type"`
	AgentType    string                `json:"agent_type"`
	CreatedAt    string                `json:"created_at"`
	Environments []agentEnvironmentAPI `json:"environments"`
}

type agentEnvironmentAPI struct {
	ID                string `json:"id"`
	AgentID           string `json:"agent_id"`
	Environment       string `json:"environment"`
	TracerSessionID   string `json:"tracer_session_id"`
	TracerSessionName string `json:"tracer_session_name"`
}

type agentCreatePayload struct {
	ID         string  `json:"id"`
	Name       *string `json:"name,omitempty"`
	SourceType string  `json:"source_type"`
	AgentType  string  `json:"agent_type"`
}

type agentUpdatePayload struct {
	Name      *string `json:"name,omitempty"`
	AgentType string  `json:"agent_type"`
}

func agentResourcePath(id string) string { return agentsPath + "/" + url.PathEscape(id) }

func (r *AgentResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent"
}

func (r *AgentResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a workspace-scoped LangSmith agent and its automatically created LOCAL, DEVELOPMENT, STAGING, and PRODUCTION environments. Environments are owned by the agent, not separate Terraform resources. Import with `<agent_id>` or `<workspace_id>/<agent_id>`. Requires a LangSmith deployment that supports the Agent Platform API.\n\n**Warning:** Destroying or replacing an agent also deletes its environments, tracing projects, and attached runtimes. Use Terraform `prevent_destroy` to protect production agents. The API may report successful deletion even if downstream runtime or project cleanup fails.",
		Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Agent identifier (same as agent_id)."},
			"agent_id":     schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.RegexMatches(agentIDPattern, "must be 1–63 lowercase letters, digits, or hyphens, starting with a letter and ending with a letter or digit")}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, MarkdownDescription: "Immutable workspace-unique agent identifier. Changing this destroys and recreates the agent and its environments."},
			"workspace_id": schema.StringAttribute{Optional: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, MarkdownDescription: "Workspace (tenant) ID. Defaults to the provider workspace. Changing this replaces the agent."},
			"name":         schema.StringAttribute{Optional: true, Computed: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 255), agentNameValidator{}}, MarkdownDescription: "Display name. Defaults to agent_id on creation; updates in place."},
			"source_type":  schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.OneOf("LSD", "MDA", "EXTERNAL", "FLEET")}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, MarkdownDescription: "Agent source: LSD, MDA, EXTERNAL, or FLEET. Changes require replacement because the API restricts source transitions."},
			"agent_type":   schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("CUSTOM"), Validators: []validator.String{stringvalidator.OneOf("CUSTOM", "CODING")}, MarkdownDescription: "Agent type: CUSTOM or CODING. Defaults to CUSTOM; updates in place."},
			"created_at":   schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Creation timestamp."},
			"environments": schema.MapNestedAttribute{Computed: true, MarkdownDescription: "Environments keyed by LOCAL, DEVELOPMENT, STAGING, or PRODUCTION. Use tracer_session_id to reference each environment's tracing project in other resources. Refreshed from the API, including externally deleted environments.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"id":                  schema.StringAttribute{Computed: true, MarkdownDescription: "Agent environment UUID."},
				"tracer_session_id":   schema.StringAttribute{Computed: true, MarkdownDescription: "Tracing project UUID."},
				"tracer_session_name": schema.StringAttribute{Computed: true, MarkdownDescription: "Tracing project name."},
			}}},
		},
	}
}

func (r *AgentResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*langsmith.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", fmt.Sprintf("Expected *langsmith.Client, got %T", req.ProviderData))
		return
	}
	r.client = client
}

func (r *AgentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan agentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.createAgent(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Agent", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *AgentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state agentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.readAgent(ctx, state)
	if err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Agent", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *AgentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan agentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.updateAgent(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Agent", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *AgentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state agentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.deleteAgent(ctx, state); err != nil {
		resp.Diagnostics.AddError("Unable to Delete LangSmith Agent", err.Error())
	}
}

func (r *AgentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	id := parts[len(parts)-1]
	if len(parts) > 2 || !agentIDPattern.MatchString(id) || (len(parts) == 2 && parts[0] == "") {
		resp.Diagnostics.AddError("Invalid Agent Import ID", "Use <agent_id> or <workspace_id>/<agent_id>, where agent_id is a valid lowercase agent slug.")
		return
	}
	if len(parts) == 2 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("workspace_id"), parts[0])...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("agent_id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

func (r *AgentResource) createAgent(ctx context.Context, plan agentResourceModel) (agentResourceModel, error) {
	var result agentAPI
	payload := agentCreatePayload{ID: plan.AgentID.ValueString(), Name: agentNamePointer(plan.Name), SourceType: plan.SourceType.ValueString(), AgentType: plan.AgentType.ValueString()}
	if err := r.client.Post(ctx, agentsPath, payload, &result, workspaceOpts(plan.WorkspaceID)...); err != nil {
		return plan, err
	}
	return agentModelFromAPI(result, plan)
}

func (r *AgentResource) readAgent(ctx context.Context, state agentResourceModel) (agentResourceModel, error) {
	var result agentAPI
	if err := r.client.Get(ctx, agentResourcePath(state.AgentID.ValueString()), nil, &result, workspaceOpts(state.WorkspaceID)...); err != nil {
		return state, err
	}
	return agentModelFromAPI(result, state)
}

func (r *AgentResource) updateAgent(ctx context.Context, plan agentResourceModel) (agentResourceModel, error) {
	var result agentAPI
	payload := agentUpdatePayload{Name: agentNamePointer(plan.Name), AgentType: plan.AgentType.ValueString()}
	if err := r.client.Patch(ctx, agentResourcePath(plan.AgentID.ValueString()), payload, &result, workspaceOpts(plan.WorkspaceID)...); err != nil {
		return plan, err
	}
	return agentModelFromAPI(result, plan)
}

func (r *AgentResource) deleteAgent(ctx context.Context, state agentResourceModel) error {
	err := r.client.Delete(ctx, agentResourcePath(state.AgentID.ValueString()), nil, nil, workspaceOpts(state.WorkspaceID)...)
	if isLangSmithNotFound(err) {
		return nil
	}
	return err
}

func agentNamePointer(name types.String) *string {
	if name.IsNull() || name.IsUnknown() {
		return nil
	}
	value := name.ValueString()
	return &value
}

func agentModelFromAPI(api agentAPI, previous agentResourceModel) (agentResourceModel, error) {
	if api.ID == "" {
		return previous, errors.New("LangSmith did not return an agent ID")
	}
	previous.ID = types.StringValue(api.ID)
	previous.AgentID = types.StringValue(api.ID)
	previous.Name = types.StringValue(api.Name)
	previous.SourceType = types.StringValue(api.SourceType)
	previous.AgentType = types.StringValue(api.AgentType)
	previous.CreatedAt = nullableString(api.CreatedAt)
	elements := make(map[string]attr.Value, len(api.Environments))
	for _, environment := range api.Environments {
		value, diagnostics := types.ObjectValue(agentEnvironmentAttributeTypes, map[string]attr.Value{
			"id":                  types.StringValue(environment.ID),
			"tracer_session_id":   types.StringValue(environment.TracerSessionID),
			"tracer_session_name": types.StringValue(environment.TracerSessionName),
		})
		if diagnostics.HasError() {
			return previous, agentDiagnosticsError(diagnostics)
		}
		elements[environment.Environment] = value
	}
	var diagnostics diag.Diagnostics
	previous.Environments, diagnostics = types.MapValue(types.ObjectType{AttrTypes: agentEnvironmentAttributeTypes}, elements)
	if diagnostics.HasError() {
		return previous, agentDiagnosticsError(diagnostics)
	}
	return previous, nil
}

func agentDiagnosticsError(diagnostics diag.Diagnostics) error {
	return fmt.Errorf("unable to map agent environments: %v", diagnostics)
}

type agentNameValidator struct{}

func (agentNameValidator) Description(ctx context.Context) string {
	return "Name must not contain leading or trailing whitespace."
}

func (v agentNameValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (agentNameValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()
	if value != strings.TrimSpace(value) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Agent Name", "Name must not contain leading or trailing whitespace.")
	}
}
