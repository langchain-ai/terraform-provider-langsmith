package provider

// Manages a LangSmith dashboard, its charts and its layout as one resource.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var (
	_ resource.Resource                = &dashboardResource{}
	_ resource.ResourceWithConfigure   = &dashboardResource{}
	_ resource.ResourceWithImportState = &dashboardResource{}
)

func NewDashboardResource() resource.Resource {
	return &dashboardResource{}
}

type dashboardResource struct {
	client *langsmith.Client
}

type dashboardResourceModel struct {
	ID          types.String          `tfsdk:"id"`
	WorkspaceID types.String          `tfsdk:"workspace_id"`
	Title       types.String          `tfsdk:"title"`
	Description types.String          `tfsdk:"description"`
	Charts      []dashboardChartModel `tfsdk:"charts"`
	Layout      []dashboardRowModel   `tfsdk:"layout"`
}

type dashboardChartModel struct {
	ID           types.String           `tfsdk:"id"`
	Key          types.String           `tfsdk:"key"`
	Type         types.String           `tfsdk:"type"`
	Title        types.String           `tfsdk:"title"`
	Description  types.String           `tfsdk:"description"`
	Markdown     types.String           `tfsdk:"markdown"`
	MetadataJSON types.String           `tfsdk:"metadata_json"`
	Series       []dashboardSeriesModel `tfsdk:"series"`
}

type dashboardSeriesModel struct {
	Name         types.String            `tfsdk:"name"`
	Metric       *dashboardMetricModel   `tfsdk:"metric"`
	ProjectIDs   []types.String          `tfsdk:"project_ids"`
	DatasetIDs   []types.String          `tfsdk:"dataset_ids"`
	RunFilter    types.String            `tfsdk:"run_filter"`
	TraceFilter  types.String            `tfsdk:"trace_filter"`
	TreeFilter   types.String            `tfsdk:"tree_filter"`
	GroupBy      []dashboardGroupByModel `tfsdk:"group_by"`
	MetadataJSON types.String            `tfsdk:"metadata_json"`
}

type dashboardMetricModel struct {
	Type        types.String              `tfsdk:"type"`
	Field       types.String              `tfsdk:"field"`
	Percentile  types.Float64             `tfsdk:"percentile"`
	FeedbackKey types.String              `tfsdk:"feedback_key"`
	Filter      types.String              `tfsdk:"filter"`
	Numerator   *dashboardMetricLeafModel `tfsdk:"numerator"`
	Denominator *dashboardMetricLeafModel `tfsdk:"denominator"`
}

// dashboardMetricLeafModel is one side of a ratio metric.
type dashboardMetricLeafModel struct {
	Type        types.String  `tfsdk:"type"`
	Field       types.String  `tfsdk:"field"`
	Percentile  types.Float64 `tfsdk:"percentile"`
	FeedbackKey types.String  `tfsdk:"feedback_key"`
	Filter      types.String  `tfsdk:"filter"`
}

type dashboardGroupByModel struct {
	Attribute types.String `tfsdk:"attribute"`
	Path      types.String `tfsdk:"path"`
}

type dashboardRowModel struct {
	Height types.Int64             `tfsdk:"height"`
	Charts []dashboardRowItemModel `tfsdk:"charts"`
}

type dashboardRowItemModel struct {
	Key   types.String `tfsdk:"key"`
	Width types.Int64  `tfsdk:"width"`
}

const dashboardTextChartType = "text"

var (
	dashboardChartTypes      = []string{"line", "bar", "table", "kpi", "top-k", "pie", dashboardTextChartType}
	dashboardLeafMetricTypes = []string{"count", "sum", "max", "min", "avg", "percentile"}
	dashboardGroupByAttrs    = []string{"name", "run_type", "tag", "project", "status", "metadata", "feedback_label"}
)

func dashboardMetricLeafAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"type": schema.StringAttribute{
			Required: true,
			MarkdownDescription: "Aggregation: `count`, `sum`, `max`, `min`, `avg` or `percentile` (and `ratio` " +
				"on the top-level metric).",
		},
		"field": schema.StringAttribute{
			Optional: true,
			MarkdownDescription: "Run field to aggregate, such as `latency_seconds`, `total_tokens`, `total_cost` " +
				"or `feedback_score`. Required for every type except `count` and `ratio`.",
		},
		"percentile": schema.Float64Attribute{
			Optional:            true,
			MarkdownDescription: "Percentile between 0 and 1, such as `0.99`. Required when `type` is `percentile`.",
		},
		"feedback_key": schema.StringAttribute{
			Optional: true,
			MarkdownDescription: "Feedback key. With `type = \"count\"` it counts feedback with this key; with " +
				"`field = \"feedback_score\"` it selects which scores to aggregate.",
		},
		"filter": schema.StringAttribute{
			Optional:            true,
			MarkdownDescription: "Filter, in LangSmith's filter query language, applied to the rows being aggregated.",
		},
	}
}

func (r *dashboardResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboard"
}

func (r *dashboardResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	metricAttributes := dashboardMetricLeafAttributes()
	metricAttributes["type"] = schema.StringAttribute{
		Required:            true,
		MarkdownDescription: "Aggregation: `count`, `sum`, `max`, `min`, `avg`, `percentile` or `ratio`.",
		Validators:          []validator.String{stringvalidator.OneOf(append(slices.Clone(dashboardLeafMetricTypes), "ratio")...)},
	}
	for _, side := range []string{"numerator", "denominator"} {
		leaf := dashboardMetricLeafAttributes()
		leaf["type"] = schema.StringAttribute{
			Required:            true,
			MarkdownDescription: "Aggregation: `count`, `sum`, `max`, `min`, `avg` or `percentile`.",
			Validators:          []validator.String{stringvalidator.OneOf(dashboardLeafMetricTypes...)},
		}
		metricAttributes[side] = schema.SingleNestedAttribute{
			Optional:            true,
			MarkdownDescription: fmt.Sprintf("The ratio's %s. Required when `type` is `ratio`.", side),
			Attributes:          leaf,
		}
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a LangSmith dashboard: its charts, text blocks and layout. The whole " +
			"dashboard is written in one request, so an apply never leaves it half updated.\n\n" +
			"Charts are identified by `key`. Changing a chart's other attributes updates it in place; " +
			"removing a chart deletes it. To manage a dashboard exported from the UI as JSON, use " +
			"`langsmith_dashboard_json`.",
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
			"title": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Dashboard title.",
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Dashboard description.",
			},
			"charts": schema.ListNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Charts and text blocks, in display order. Charts not placed by `layout` are shown after it.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Optional: true,
							MarkdownDescription: "ID of a chart already on the dashboard to manage under this `key`. " +
								"Use it after importing a dashboard built in the UI, whose charts read back with their " +
								"IDs as keys, to give a chart a new key without recreating it.",
						},
						"key": schema.StringAttribute{
							Required: true,
							MarkdownDescription: "Identifies the chart within the dashboard and in `layout`. Up to 64 " +
								"letters, digits, `_`, `.` or `-`, starting with a letter or digit.",
							Validators: []validator.String{stringvalidator.RegexMatches(dashboardChartKeyPattern, "must be 1-64 letters, digits, '_', '.' or '-', starting with a letter or digit")},
						},
						"type": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Chart type: `line`, `bar`, `table`, `kpi`, `top-k`, `pie`, or `text` for a Markdown text block.",
							Validators:          []validator.String{stringvalidator.OneOf(dashboardChartTypes...)},
						},
						"title": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Chart title. Required for charts; not used by text blocks.",
						},
						"description": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Chart description. Not used by text blocks.",
						},
						"markdown": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Markdown content. Required for text blocks; not used by charts.",
						},
						"metadata_json": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Display settings the LangSmith UI stores on the chart, as a JSON object.",
						},
						"series": schema.ListNestedAttribute{
							Optional:            true,
							MarkdownDescription: "The chart's series. Charts need between one and three; text blocks have none.",
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"name": schema.StringAttribute{
										Required:            true,
										MarkdownDescription: "Series name, shown in the legend.",
									},
									"metric": schema.SingleNestedAttribute{
										Required:            true,
										MarkdownDescription: "What the series measures.",
										Attributes:          metricAttributes,
									},
									"project_ids": schema.ListAttribute{
										ElementType: types.StringType,
										Optional:    true,
										MarkdownDescription: "Tracing project IDs to chart runs from. Set this or `dataset_ids`. " +
											"Look projects up with the `langsmith_project` data source.",
										Validators: []validator.List{listvalidator.SizeAtLeast(1)},
									},
									"dataset_ids": schema.ListAttribute{
										ElementType:         types.StringType,
										Optional:            true,
										MarkdownDescription: "Dataset IDs to chart experiment runs from. Set this or `project_ids`.",
										Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
									},
									"run_filter": schema.StringAttribute{
										Optional:            true,
										MarkdownDescription: "Filter on runs. Only with `project_ids`.",
									},
									"trace_filter": schema.StringAttribute{
										Optional:            true,
										MarkdownDescription: "Filter on the root run of each run's trace. Only with `project_ids`.",
									},
									"tree_filter": schema.StringAttribute{
										Optional:            true,
										MarkdownDescription: "Filter on any run in each run's trace. Only with `project_ids`.",
									},
									"group_by": schema.ListNestedAttribute{
										Optional:            true,
										MarkdownDescription: "Splits the series into one line per group.",
										NestedObject: schema.NestedAttributeObject{
											Attributes: map[string]schema.Attribute{
												"attribute": schema.StringAttribute{
													Required: true,
													MarkdownDescription: "Attribute to group by: `name`, `run_type`, `tag`, `project`, " +
														"`status`, `metadata` or `feedback_label`.",
													Validators: []validator.String{stringvalidator.OneOf(dashboardGroupByAttrs...)},
												},
												"path": schema.StringAttribute{
													Optional:            true,
													MarkdownDescription: "Metadata key or feedback key. Required for `metadata` and `feedback_label`.",
												},
											},
										},
									},
									"metadata_json": schema.StringAttribute{
										Optional:            true,
										MarkdownDescription: "Display settings the LangSmith UI stores on the series, such as `value_format`, as a JSON object.",
									},
								},
							},
						},
					},
				},
			},
			"layout": schema.ListNestedAttribute{
				Optional: true,
				MarkdownDescription: "Rows of charts for wide screens, top to bottom. On narrow screens each chart " +
					"gets its own full-width row, in the same order.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"height": schema.Int64Attribute{
							Required:            true,
							MarkdownDescription: "Row height, in grid units.",
						},
						"charts": schema.ListNestedAttribute{
							Required:            true,
							MarkdownDescription: "Charts in the row, left to right.",
							Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"key": schema.StringAttribute{
										Required:            true,
										MarkdownDescription: "Key of a chart in `charts`.",
									},
									"width": schema.Int64Attribute{
										Optional: true,
										MarkdownDescription: "Width out of 120 units, at least 20. Set it on every chart " +
											"in the row, totalling 120, or on none to share the row evenly.",
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func (r *dashboardResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *dashboardResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dashboardResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := dashboardDefinitionFromModel(plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid dashboard", err.Error())
		return
	}
	var result dashboardDefinitionAPI
	etag, err := putDashboardDefinition(ctx, r.client, newDashboardID(), plan.WorkspaceID, "", body, &result)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create dashboard", err.Error())
		return
	}
	r.setState(ctx, result, plan, &resp.State, &resp.Diagnostics)
	resp.Diagnostics.Append(saveDashboardETag(ctx, resp.Private, etag)...)
}

func (r *dashboardResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dashboardResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var result dashboardDefinitionAPI
	etag, err := getDashboardDefinition(ctx, r.client, state.ID.ValueString(), state.WorkspaceID, &result)
	if err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read dashboard", err.Error())
		return
	}
	r.setState(ctx, result, state, &resp.State, &resp.Diagnostics)
	resp.Diagnostics.Append(saveDashboardETag(ctx, resp.Private, etag)...)
}

func (r *dashboardResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state dashboardResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := dashboardDefinitionFromModel(plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid dashboard", err.Error())
		return
	}
	// Writing only if the dashboard is as last read turns an edit made in the
	// UI between plan and apply into an error rather than a silent overwrite.
	ifMatch, diags := loadDashboardETag(ctx, req.Private)
	resp.Diagnostics.Append(diags...)
	var result dashboardDefinitionAPI
	etag, err := putDashboardDefinition(ctx, r.client, state.ID.ValueString(), plan.WorkspaceID, ifMatch, body, &result)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update dashboard", err.Error())
		return
	}
	r.setState(ctx, result, plan, &resp.State, &resp.Diagnostics)
	resp.Diagnostics.Append(saveDashboardETag(ctx, resp.Private, etag)...)
}

func (r *dashboardResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dashboardResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Delete(ctx, dashboardPath(state.ID.ValueString()), nil, nil, workspaceOpts(state.WorkspaceID)...); err != nil && !isLangSmithNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete dashboard", err.Error())
	}
}

func (r *dashboardResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// setState stores the API's view of the dashboard, keeping prior's spelling
// of values that mean the same thing (see dashboardModelFromAPI).
func (r *dashboardResource) setState(ctx context.Context, result dashboardDefinitionAPI, prior dashboardResourceModel, state interface {
	Set(context.Context, any) diag.Diagnostics
}, diags *diag.Diagnostics) {
	model := dashboardModelFromAPI(result, prior)
	diags.Append(state.Set(ctx, &model)...)
}

// Terraform model -> API
//
// The configuration maps field for field onto the API's definition format,
// and the API validates it, so these functions only change representation.

func dashboardDefinitionFromModel(m dashboardResourceModel) (dashboardDefinitionAPI, error) {
	out := dashboardDefinitionAPI{
		Title:       m.Title.ValueString(),
		Description: m.Description.ValueStringPointer(),
		Charts:      make([]dashboardChartAPI, 0, len(m.Charts)),
	}
	for _, chart := range m.Charts {
		apiChart, err := dashboardChartFromModel(chart)
		if err != nil {
			return out, fmt.Errorf("chart %q: %w", chart.Key.ValueString(), err)
		}
		out.Charts = append(out.Charts, apiChart)
	}
	for _, row := range m.Layout {
		apiRow := dashboardRowAPI{Height: row.Height.ValueInt64()}
		for _, item := range row.Charts {
			apiRow.Charts = append(apiRow.Charts, dashboardRowChartAPI{Key: item.Key.ValueString(), Width: item.Width.ValueInt64Pointer()})
		}
		out.Layout = append(out.Layout, apiRow)
	}
	return out, nil
}

func dashboardChartFromModel(m dashboardChartModel) (dashboardChartAPI, error) {
	metadata, err := jsonObjectFromString(m.MetadataJSON, "metadata_json")
	if err != nil {
		return dashboardChartAPI{}, err
	}
	out := dashboardChartAPI{
		ID:          m.ID.ValueStringPointer(),
		Key:         m.Key.ValueString(),
		Type:        m.Type.ValueString(),
		Title:       m.Title.ValueStringPointer(),
		Description: m.Description.ValueStringPointer(),
		Markdown:    m.Markdown.ValueStringPointer(),
		Metadata:    metadata,
	}
	for _, series := range m.Series {
		apiSeries, err := dashboardSeriesFromModel(series)
		if err != nil {
			return out, fmt.Errorf("series %q: %w", series.Name.ValueString(), err)
		}
		out.Series = append(out.Series, apiSeries)
	}
	return out, nil
}

func dashboardSeriesFromModel(m dashboardSeriesModel) (dashboardSeriesAPI, error) {
	metadata, err := jsonObjectFromString(m.MetadataJSON, "metadata_json")
	if err != nil {
		return dashboardSeriesAPI{}, err
	}
	out := dashboardSeriesAPI{
		Name:        m.Name.ValueString(),
		ProjectIDs:  stringValues(m.ProjectIDs),
		DatasetIDs:  stringValues(m.DatasetIDs),
		RunFilter:   m.RunFilter.ValueStringPointer(),
		TraceFilter: m.TraceFilter.ValueStringPointer(),
		TreeFilter:  m.TreeFilter.ValueStringPointer(),
		Metadata:    metadata,
	}
	if m.Metric != nil {
		out.Metric = &dashboardMetricAPI{
			Type:        m.Metric.Type.ValueString(),
			Field:       m.Metric.Field.ValueStringPointer(),
			Percentile:  m.Metric.Percentile.ValueFloat64Pointer(),
			FeedbackKey: m.Metric.FeedbackKey.ValueStringPointer(),
			Filter:      m.Metric.Filter.ValueStringPointer(),
			Numerator:   dashboardLeafMetricFromModel(m.Metric.Numerator),
			Denominator: dashboardLeafMetricFromModel(m.Metric.Denominator),
		}
	}
	for _, group := range m.GroupBy {
		out.GroupBy = append(out.GroupBy, dashboardGroupByAPI{Attribute: group.Attribute.ValueString(), Path: group.Path.ValueStringPointer()})
	}
	return out, nil
}

func dashboardLeafMetricFromModel(m *dashboardMetricLeafModel) *dashboardMetricAPI {
	if m == nil {
		return nil
	}
	return &dashboardMetricAPI{
		Type:        m.Type.ValueString(),
		Field:       m.Field.ValueStringPointer(),
		Percentile:  m.Percentile.ValueFloat64Pointer(),
		FeedbackKey: m.FeedbackKey.ValueStringPointer(),
		Filter:      m.Filter.ValueStringPointer(),
	}
}

// API -> Terraform model

// dashboardModelFromAPI converts an API definition to the Terraform model.
// Where the API and prior hold equivalent values (metadata JSON with
// different formatting, series in another order, or layout widths a row gets
// by default), it keeps prior's form so a refresh does not report a change.
func dashboardModelFromAPI(api dashboardDefinitionAPI, prior dashboardResourceModel) dashboardResourceModel {
	out := dashboardResourceModel{
		ID:          types.StringValue(api.ID),
		WorkspaceID: prior.WorkspaceID,
		Title:       types.StringValue(api.Title),
		Description: types.StringPointerValue(api.Description),
	}
	priorCharts := map[string]dashboardChartModel{}
	for _, chart := range prior.Charts {
		priorCharts[chart.Key.ValueString()] = chart
	}
	for _, chart := range api.Charts {
		out.Charts = append(out.Charts, dashboardChartModelFromAPI(chart, priorCharts[chart.Key]))
	}
	// Keep an empty list when prior had one, rather than flip it to null.
	if out.Charts == nil && prior.Charts != nil {
		out.Charts = []dashboardChartModel{}
	}
	out.Layout = dashboardLayoutModelFromAPI(api.Layout, prior.Layout)
	return out
}

func dashboardChartModelFromAPI(api dashboardChartAPI, prior dashboardChartModel) dashboardChartModel {
	out := dashboardChartModel{
		// id is only an input; keep whatever the configuration said.
		ID:           prior.ID,
		Key:          types.StringValue(api.Key),
		Type:         types.StringValue(api.Type),
		Title:        types.StringPointerValue(api.Title),
		Description:  types.StringPointerValue(api.Description),
		Markdown:     types.StringPointerValue(api.Markdown),
		MetadataJSON: jsonStringFromObject(api.Metadata, prior.MetadataJSON),
	}
	for _, pair := range pairSeriesWithPrior(api.Series, prior.Series) {
		out.Series = append(out.Series, dashboardSeriesModelFromAPI(pair.api, pair.prior))
	}
	return out
}

type seriesPair struct {
	api   dashboardSeriesAPI
	prior dashboardSeriesModel
}

// pairSeriesWithPrior puts the API's series in the order prior lists them.
// The API returns series sorted by name, so this keeps a configuration's
// order from showing as a change. Each series is paired with the prior one
// it equals; series with no equal, which are changes, come last in API order.
func pairSeriesWithPrior(api []dashboardSeriesAPI, prior []dashboardSeriesModel) []seriesPair {
	encoded := make([]string, len(api))
	for i, series := range api {
		raw, _ := json.Marshal(series)
		encoded[i] = string(raw)
	}
	used := make([]bool, len(api))
	var out []seriesPair
	for _, priorSeries := range prior {
		apiForm, err := dashboardSeriesFromModel(priorSeries)
		if err != nil {
			continue
		}
		raw, _ := json.Marshal(apiForm)
		for i := range api {
			if !used[i] && encoded[i] == string(raw) {
				used[i] = true
				out = append(out, seriesPair{api: api[i], prior: priorSeries})
				break
			}
		}
	}
	for i, series := range api {
		if !used[i] {
			out = append(out, seriesPair{api: series})
		}
	}
	return out
}

func dashboardSeriesModelFromAPI(api dashboardSeriesAPI, prior dashboardSeriesModel) dashboardSeriesModel {
	out := dashboardSeriesModel{
		Name:         types.StringValue(api.Name),
		ProjectIDs:   stringModels(api.ProjectIDs),
		DatasetIDs:   stringModels(api.DatasetIDs),
		RunFilter:    types.StringPointerValue(api.RunFilter),
		TraceFilter:  types.StringPointerValue(api.TraceFilter),
		TreeFilter:   types.StringPointerValue(api.TreeFilter),
		MetadataJSON: jsonStringFromObject(api.Metadata, prior.MetadataJSON),
	}
	if api.Metric != nil {
		out.Metric = &dashboardMetricModel{
			Type:        types.StringValue(api.Metric.Type),
			Field:       types.StringPointerValue(api.Metric.Field),
			Percentile:  types.Float64PointerValue(api.Metric.Percentile),
			FeedbackKey: types.StringPointerValue(api.Metric.FeedbackKey),
			Filter:      types.StringPointerValue(api.Metric.Filter),
			Numerator:   dashboardLeafMetricModelFromAPI(api.Metric.Numerator),
			Denominator: dashboardLeafMetricModelFromAPI(api.Metric.Denominator),
		}
	}
	for _, group := range api.GroupBy {
		out.GroupBy = append(out.GroupBy, dashboardGroupByModel{Attribute: types.StringValue(group.Attribute), Path: types.StringPointerValue(group.Path)})
	}
	return out
}

func dashboardLeafMetricModelFromAPI(api *dashboardMetricAPI) *dashboardMetricLeafModel {
	if api == nil {
		return nil
	}
	return &dashboardMetricLeafModel{
		Type:        types.StringValue(api.Type),
		Field:       types.StringPointerValue(api.Field),
		Percentile:  types.Float64PointerValue(api.Percentile),
		FeedbackKey: types.StringPointerValue(api.FeedbackKey),
		Filter:      types.StringPointerValue(api.Filter),
	}
}

// dashboardLayoutModelFromAPI reads the layout rows. The API fills in every
// width; a row whose configuration left widths unset keeps them unset while
// the row still has the even widths that implies.
func dashboardLayoutModelFromAPI(api []dashboardRowAPI, prior []dashboardRowModel) []dashboardRowModel {
	if api == nil {
		if prior != nil && len(prior) == 0 {
			return []dashboardRowModel{}
		}
		return nil
	}
	var out []dashboardRowModel
	for i, row := range api {
		var priorRow dashboardRowModel
		if i < len(prior) {
			priorRow = prior[i]
		}
		keepUnset := len(priorRow.Charts) == len(row.Charts) &&
			!slices.ContainsFunc(priorRow.Charts, func(item dashboardRowItemModel) bool { return !item.Width.IsNull() })
		defaults := defaultLayoutWidths(len(row.Charts), dashboardMediumWidthUnits)
		for j, item := range row.Charts {
			if item.Width == nil || *item.Width != defaults[j] {
				keepUnset = false
			}
		}

		model := dashboardRowModel{Height: types.Int64Value(row.Height)}
		for _, item := range row.Charts {
			width := types.Int64PointerValue(item.Width)
			if keepUnset {
				width = types.Int64Null()
			}
			model.Charts = append(model.Charts, dashboardRowItemModel{Key: types.StringValue(item.Key), Width: width})
		}
		out = append(out, model)
	}
	return out
}

// Helpers

// dashboardChartKeyPattern matches the chart keys the API accepts.
var dashboardChartKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func jsonObjectFromString(value types.String, field string) (map[string]any, error) {
	if value.IsNull() || value.IsUnknown() {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(value.ValueString()), &out); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object: %w", field, err)
	}
	return out, nil
}

// jsonStringFromObject returns prior when it encodes the same object, and
// the object's canonical encoding otherwise.
func jsonStringFromObject(value map[string]any, prior types.String) types.String {
	if value == nil {
		return types.StringNull()
	}
	if parsed, err := jsonObjectFromString(prior, ""); err == nil && parsed != nil && jsonSubset(parsed, value) && jsonSubset(value, parsed) {
		return prior
	}
	out, err := json.Marshal(value)
	if err != nil {
		return types.StringNull()
	}
	return types.StringValue(string(out))
}

func stringValues(values []types.String) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, v.ValueString())
	}
	return out
}

func stringModels(values []string) []types.String {
	if len(values) == 0 {
		return nil
	}
	out := make([]types.String, 0, len(values))
	for _, v := range values {
		out = append(out, types.StringValue(v))
	}
	return out
}
