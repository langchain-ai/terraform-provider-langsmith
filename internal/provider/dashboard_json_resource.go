package provider

// Manages a LangSmith dashboard from a JSON definition, such as one exported
// from the API's GET /charts/section/{id}/definition.

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

var (
	_ resource.Resource                = &dashboardJSONResource{}
	_ resource.ResourceWithConfigure   = &dashboardJSONResource{}
	_ resource.ResourceWithImportState = &dashboardJSONResource{}
)

func NewDashboardJSONResource() resource.Resource {
	return &dashboardJSONResource{}
}

type dashboardJSONResource struct {
	client *langsmith.Client
}

type dashboardJSONResourceModel struct {
	ID          types.String `tfsdk:"id"`
	WorkspaceID types.String `tfsdk:"workspace_id"`
	Dashboard   types.String `tfsdk:"dashboard"`
}

func (r *dashboardJSONResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboard_json"
}

func (r *dashboardJSONResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a LangSmith dashboard from a JSON definition. Use it to manage a dashboard " +
			"built in the UI: export it with `GET /api/v1/charts/section/{id}/definition`, or import the dashboard " +
			"and copy the definition from state. For dashboards written in HCL, use `langsmith_dashboard`.\n\n" +
			"Only the fields set in `dashboard` are compared with the live dashboard, so values the API fills " +
			"in (such as null optional fields) do not show as drift.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Dashboard ID.",
			},
			"workspace_id": schema.StringAttribute{
				Optional:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "LangSmith workspace (tenant) ID that owns this dashboard. When unset, the resource uses the workspace configured on the provider block.",
			},
			"dashboard": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "The dashboard definition as a JSON object with `title`, optional `description`, " +
					"`charts` and optional `layout`. Any `id` field is ignored.",
			},
		},
	}
}

func (r *dashboardJSONResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *dashboardJSONResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dashboardJSONResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := dashboardJSONBody(plan.Dashboard.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid dashboard", err.Error())
		return
	}
	var result map[string]any
	// Creating is not idempotent, so a retried request could create a second dashboard.
	opts := append(workspaceOpts(plan.WorkspaceID), option.WithMaxRetries(0))
	if err := r.client.Post(ctx, dashboardDefinitionCreatePath, body, &result, opts...); err != nil {
		resp.Diagnostics.AddError("Failed to create dashboard", err.Error())
		return
	}
	id, _ := result["id"].(string)
	plan.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dashboardJSONResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dashboardJSONResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var live map[string]any
	if err := r.client.Get(ctx, dashboardDefinitionPath(state.ID.ValueString()), nil, &live, workspaceOpts(state.WorkspaceID)...); err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read dashboard", err.Error())
		return
	}
	delete(live, "id")

	dashboard, err := reconcileDashboardJSON(state.Dashboard, live)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read dashboard", err.Error())
		return
	}
	state.Dashboard = dashboard
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *dashboardJSONResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state dashboardJSONResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := dashboardJSONBody(plan.Dashboard.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid dashboard", err.Error())
		return
	}
	if err := r.client.Put(ctx, dashboardDefinitionPath(state.ID.ValueString()), body, nil, workspaceOpts(plan.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Failed to update dashboard", err.Error())
		return
	}
	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dashboardJSONResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dashboardJSONResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Delete(ctx, dashboardPath(state.ID.ValueString()), nil, nil, workspaceOpts(state.WorkspaceID)...); err != nil && !isLangSmithNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete dashboard", err.Error())
	}
}

func (r *dashboardJSONResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// dashboardJSONBody parses a dashboard definition and drops its id, which
// names the dashboard it was exported from rather than the one being written.
func dashboardJSONBody(raw string) (map[string]any, error) {
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return nil, fmt.Errorf("dashboard must be a JSON object: %w", err)
	}
	delete(body, "id")
	return body, nil
}

// reconcileDashboardJSON keeps the configured string while the live dashboard
// still matches every field it sets, and otherwise returns the live
// definition so the next plan shows the drift.
func reconcileDashboardJSON(configured types.String, live map[string]any) (types.String, error) {
	if !configured.IsNull() && !configured.IsUnknown() {
		if want, err := dashboardJSONBody(configured.ValueString()); err == nil && jsonSubset(want, live) {
			return configured, nil
		}
	}
	out, err := json.Marshal(live)
	if err != nil {
		return types.StringNull(), fmt.Errorf("marshal dashboard: %w", err)
	}
	return types.StringValue(string(out)), nil
}

// jsonSubset reports whether got has every value set in want. Objects may
// carry extra keys, and a null in want also matches a missing key; arrays
// must have the same length and match element by element.
func jsonSubset(want, got any) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for key, wantValue := range w {
			gotValue, present := g[key]
			if wantValue == nil {
				if present && gotValue != nil {
					return false
				}
				continue
			}
			if !present || !jsonSubset(wantValue, gotValue) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !jsonSubset(w[i], g[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(want, got)
	}
}
