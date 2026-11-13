package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
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
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

const managedToolKitsPath = "v1/managed-tools/kits"

// managedToolKitMaxTools mirrors the API's per-kit tool limit.
const managedToolKitMaxTools = 10000

var (
	_ resource.Resource                   = &managedToolKitResource{}
	_ resource.ResourceWithConfigure      = &managedToolKitResource{}
	_ resource.ResourceWithImportState    = &managedToolKitResource{}
	_ resource.ResourceWithValidateConfig = &managedToolKitResource{}
)

type managedToolKitResource struct {
	client *langsmith.Client
}

type managedToolKitModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	Description      types.String `tfsdk:"description"`
	ConnectionPolicy types.String `tfsdk:"connection_policy"`
	WorkspaceID      types.String `tfsdk:"workspace_id"`
	Tools            types.Set    `tfsdk:"tools"`
	MCPPath          types.String `tfsdk:"mcp_path"`
}

type managedToolKitToolModel struct {
	ServerID         types.String `tfsdk:"server_id"`
	BuiltinServerKey types.String `tfsdk:"builtin_server_key"`
	ToolName         types.String `tfsdk:"tool_name"`
	ExposedName      types.String `tfsdk:"exposed_name"`
}

var managedToolKitToolTypes = map[string]attr.Type{
	"server_id":          types.StringType,
	"builtin_server_key": types.StringType,
	"tool_name":          types.StringType,
	"exposed_name":       types.StringType,
}

type managedToolKitToolAPI struct {
	ServerID         string `json:"server_id,omitempty"`
	BuiltinServerKey string `json:"builtin_server_key,omitempty"`
	ToolName         string `json:"tool_name"`
	ExposedName      string `json:"exposed_name,omitempty"`
}

type managedToolKitAPI struct {
	ID               string                  `json:"id"`
	Name             string                  `json:"name"`
	Description      string                  `json:"description"`
	ConnectionPolicy string                  `json:"connection_policy"`
	Scope            string                  `json:"scope"`
	MCPPath          string                  `json:"mcp_path"`
	Tools            []managedToolKitToolAPI `json:"tools"`
}

func NewManagedToolKitResource() resource.Resource {
	return &managedToolKitResource{}
}

func managedToolKitPath(id string) string {
	return managedToolKitsPath + "/" + url.PathEscape(id)
}

func (r *managedToolKitResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_managed_tool_kit"
}

func (r *managedToolKitResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	nonblank := stringvalidator.RegexMatches(regexp.MustCompile(`^\S(?:[\s\S]*\S)?$`), "must be nonblank without leading or trailing whitespace")
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a workspace-scoped LangSmith Managed Tools kit: a named selection of tools from registered servers and built-in integrations, exposed through one MCP endpoint. Requires a user-owned API key with `mcp-servers:create`, `mcp-servers:update`, `mcp-servers:delete`, and `mcp-servers:invoke`. LangSmith checks that each linked server exists but not that each tool name exists on it. Updating `tools` replaces the kit's whole selection.",
		Attributes: map[string]schema.Attribute{
			"id":                schema.StringAttribute{Computed: true, MarkdownDescription: "Kit ID.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":              schema.StringAttribute{Required: true, MarkdownDescription: "Kit name.", Validators: []validator.String{stringvalidator.LengthBetween(1, 256), nonblank}},
			"description":       schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), MarkdownDescription: "Kit description.", Validators: []validator.String{stringvalidator.LengthAtMost(4096), stringvalidator.RegexMatches(regexp.MustCompile(`^(\S(?:[\s\S]*\S)?)?$`), "must not have leading or trailing whitespace")}},
			"connection_policy": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("ALLOW_PARTIAL"), MarkdownDescription: "`ALLOW_PARTIAL` (default) lists tools from servers the caller has not connected and asks them to connect on call; `REQUIRE_ALL` blocks tool discovery until every credential is connected.", Validators: []validator.String{stringvalidator.OneOf("ALLOW_PARTIAL", "REQUIRE_ALL")}},
			"workspace_id":      schema.StringAttribute{Optional: true, MarkdownDescription: "Workspace that owns this kit. Defaults to the provider workspace. Use `workspace_id/kit_id` when importing into a workspace other than the provider default.", Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^\S+$`), "must be nonblank without whitespace")}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"mcp_path":          schema.StringAttribute{Computed: true, MarkdownDescription: "Streamable HTTP MCP endpoint path for this kit.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"tools": schema.SetNestedAttribute{
				Required:            true,
				MarkdownDescription: "Selected tools. Set exactly one of `server_id` or `builtin_server_key` per tool.",
				Validators:          []validator.Set{setvalidator.SizeBetween(1, managedToolKitMaxTools)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"server_id":          schema.StringAttribute{Optional: true, MarkdownDescription: "Registered workspace server to select from.", Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^\S+$`), "must be nonblank without whitespace"), stringvalidator.ExactlyOneOf(path.MatchRelative().AtParent().AtName("builtin_server_key"))}},
					"builtin_server_key": schema.StringAttribute{Optional: true, MarkdownDescription: "LangSmith built-in integration key to select from.", Validators: []validator.String{stringvalidator.LengthBetween(1, 64), nonblank}},
					"tool_name":          schema.StringAttribute{Required: true, MarkdownDescription: "Tool name on the source server.", Validators: []validator.String{stringvalidator.LengthBetween(1, 128), nonblank}},
					"exposed_name":       schema.StringAttribute{Optional: true, MarkdownDescription: "Optional display label.", Validators: []validator.String{stringvalidator.LengthBetween(1, 128), nonblank}},
				}},
			},
		},
	}
}

func (r *managedToolKitResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ValidateConfig rejects duplicate tool links, which the API refuses. Set
// semantics only collapse links that are identical in every attribute.
func (r *managedToolKitResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config managedToolKitModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() || config.Tools.IsNull() || config.Tools.IsUnknown() {
		return
	}
	var tools []managedToolKitToolModel
	resp.Diagnostics.Append(config.Tools.ElementsAs(ctx, &tools, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		if tool.ServerID.IsUnknown() || tool.BuiltinServerKey.IsUnknown() || tool.ToolName.IsUnknown() {
			continue
		}
		key := "server:" + tool.ServerID.ValueString() + "\n" + tool.ToolName.ValueString()
		if !tool.BuiltinServerKey.IsNull() {
			key = "builtin:" + tool.BuiltinServerKey.ValueString() + "\n" + tool.ToolName.ValueString()
		}
		if seen[key] {
			resp.Diagnostics.AddAttributeError(path.Root("tools"), "Duplicate Tool", fmt.Sprintf("Tool %q is selected more than once from the same source.", tool.ToolName.ValueString()))
			return
		}
		seen[key] = true
	}
}

func (r *managedToolKitResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan managedToolKitModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tools, err := managedToolKitToolsToAPI(ctx, plan.Tools)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Managed Tool Kit Tools", err.Error())
		return
	}
	body := map[string]any{
		"name":              plan.Name.ValueString(),
		"description":       plan.Description.ValueString(),
		"connection_policy": plan.ConnectionPolicy.ValueString(),
		"scope":             "workspace",
		"tools":             tools,
	}
	var kit managedToolKitAPI
	if err := r.client.Post(ctx, managedToolKitsPath, body, &kit, workspaceOpts(plan.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Unable to Create Managed Tool Kit", err.Error())
		return
	}
	r.setState(ctx, kit, plan, &resp.State, &resp.Diagnostics)
}

func (r *managedToolKitResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state managedToolKitModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var kit managedToolKitAPI
	if err := r.client.Get(ctx, managedToolKitPath(state.ID.ValueString()), nil, &kit, workspaceOpts(state.WorkspaceID)...); err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read Managed Tool Kit", err.Error())
		return
	}
	r.setState(ctx, kit, state, &resp.State, &resp.Diagnostics)
}

func (r *managedToolKitResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state managedToolKitModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tools, err := managedToolKitToolsToAPI(ctx, plan.Tools)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Managed Tool Kit Tools", err.Error())
		return
	}
	body := map[string]any{
		"name":              plan.Name.ValueString(),
		"description":       plan.Description.ValueString(),
		"connection_policy": plan.ConnectionPolicy.ValueString(),
		"tools":             tools,
	}
	var kit managedToolKitAPI
	if err := r.client.Patch(ctx, managedToolKitPath(state.ID.ValueString()), body, &kit, workspaceOpts(state.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Unable to Update Managed Tool Kit", err.Error())
		return
	}
	r.setState(ctx, kit, plan, &resp.State, &resp.Diagnostics)
}

func (r *managedToolKitResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state managedToolKitModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Delete(ctx, managedToolKitPath(state.ID.ValueString()), nil, nil, workspaceOpts(state.WorkspaceID)...); err != nil && !isLangSmithNotFound(err) {
		resp.Diagnostics.AddError("Unable to Delete Managed Tool Kit", err.Error())
	}
}

func (r *managedToolKitResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	workspaceID, id, ok := splitWorkspaceImportID(req.ID)
	if !ok {
		resp.Diagnostics.AddError("Invalid Managed Tool Kit Import ID", "Use kit_id for the provider workspace, or workspace_id/kit_id for an explicit workspace.")
		return
	}
	if workspaceID != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("workspace_id"), workspaceID)...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

func (r *managedToolKitResource) setState(ctx context.Context, kit managedToolKitAPI, previous managedToolKitModel, state *tfsdk.State, diags *diag.Diagnostics) {
	next, err := managedToolKitModelFromAPI(kit, previous)
	if err != nil {
		diags.AddError("Unable to Read Managed Tool Kit", err.Error())
		return
	}
	diags.Append(state.Set(ctx, &next)...)
}

func managedToolKitToolsToAPI(ctx context.Context, set types.Set) ([]managedToolKitToolAPI, error) {
	var tools []managedToolKitToolModel
	if diags := set.ElementsAs(ctx, &tools, false); diags.HasError() {
		return nil, fmt.Errorf("decode tools: %v", diags)
	}
	links := make([]managedToolKitToolAPI, 0, len(tools))
	for _, tool := range tools {
		links = append(links, managedToolKitToolAPI{
			ServerID:         tool.ServerID.ValueString(),
			BuiltinServerKey: tool.BuiltinServerKey.ValueString(),
			ToolName:         tool.ToolName.ValueString(),
			ExposedName:      tool.ExposedName.ValueString(),
		})
	}
	// Sort for deterministic request bodies; the API keeps its own order.
	sort.Slice(links, func(i, j int) bool {
		a, b := links[i], links[j]
		if a.ServerID+a.BuiltinServerKey != b.ServerID+b.BuiltinServerKey {
			return a.ServerID+a.BuiltinServerKey < b.ServerID+b.BuiltinServerKey
		}
		return a.ToolName < b.ToolName
	})
	return links, nil
}

func managedToolKitModelFromAPI(kit managedToolKitAPI, previous managedToolKitModel) (managedToolKitModel, error) {
	if kit.ID == "" {
		return previous, errors.New("LangSmith returned a tool kit without an ID")
	}
	if kit.Scope != "" && kit.Scope != "workspace" {
		return previous, fmt.Errorf("tool kit %s has scope %q; only workspace kits can be managed by this resource", kit.ID, kit.Scope)
	}
	tools := make([]attr.Value, 0, len(kit.Tools))
	for _, tool := range kit.Tools {
		value, diags := types.ObjectValue(managedToolKitToolTypes, map[string]attr.Value{
			"server_id":          nullableString(tool.ServerID),
			"builtin_server_key": nullableString(tool.BuiltinServerKey),
			"tool_name":          types.StringValue(tool.ToolName),
			"exposed_name":       nullableString(tool.ExposedName),
		})
		if diags.HasError() {
			return previous, fmt.Errorf("decode tool %q: %v", tool.ToolName, diags)
		}
		tools = append(tools, value)
	}
	toolSet, diags := types.SetValue(types.ObjectType{AttrTypes: managedToolKitToolTypes}, tools)
	if diags.HasError() {
		return previous, fmt.Errorf("decode tools: %v", diags)
	}
	next := previous
	next.ID = types.StringValue(kit.ID)
	next.Name = types.StringValue(kit.Name)
	next.Description = types.StringValue(kit.Description)
	next.ConnectionPolicy = types.StringValue(kit.ConnectionPolicy)
	next.MCPPath = types.StringValue(kit.MCPPath)
	next.Tools = toolSet
	return next, nil
}
