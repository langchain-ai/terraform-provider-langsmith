package provider

// Manages a LangSmith dashboard, its charts and its layout as one resource.

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/langchain-ai/langsmith-go/option"
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
	HeightUnits types.Int64             `tfsdk:"height_units"`
	Charts      []dashboardRowItemModel `tfsdk:"charts"`
}

type dashboardRowItemModel struct {
	Key        types.String `tfsdk:"key"`
	WidthUnits types.Int64  `tfsdk:"width_units"`
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
						"height_units": schema.Int64Attribute{
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
									"width_units": schema.Int64Attribute{
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
	// Creating is not idempotent, so a retried request could create a second dashboard.
	opts := append(workspaceOpts(plan.WorkspaceID), option.WithMaxRetries(0))
	if err := r.client.Post(ctx, dashboardDefinitionCreatePath, body, &result, opts...); err != nil {
		resp.Diagnostics.AddError("Failed to create dashboard", err.Error())
		return
	}
	r.setState(ctx, result, plan, &resp.State, &resp.Diagnostics)
}

func (r *dashboardResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dashboardResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var result dashboardDefinitionAPI
	if err := r.client.Get(ctx, dashboardDefinitionPath(state.ID.ValueString()), nil, &result, workspaceOpts(state.WorkspaceID)...); err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read dashboard", err.Error())
		return
	}
	r.setState(ctx, result, state, &resp.State, &resp.Diagnostics)
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
	var result dashboardDefinitionAPI
	if err := r.client.Put(ctx, dashboardDefinitionPath(state.ID.ValueString()), body, &result, workspaceOpts(plan.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Failed to update dashboard", err.Error())
		return
	}
	r.setState(ctx, result, plan, &resp.State, &resp.Diagnostics)
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
	model, err := dashboardModelFromAPI(result, prior)
	if err != nil {
		diags.AddError("Unsupported dashboard", err.Error())
		return
	}
	diags.Append(state.Set(ctx, &model)...)
}

// Terraform model -> API

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
	layout, err := dashboardLayoutFromModel(m.Layout)
	if err != nil {
		return out, err
	}
	out.Layout = layout
	return out, nil
}

func dashboardChartFromModel(m dashboardChartModel) (dashboardChartAPI, error) {
	metadata, err := jsonObjectFromString(m.MetadataJSON, "metadata_json")
	if err != nil {
		return dashboardChartAPI{}, err
	}
	out := dashboardChartAPI{
		Key:       m.Key.ValueString(),
		ChartType: m.Type.ValueString(),
		Metadata:  metadata,
	}
	if out.ChartType == dashboardTextChartType {
		if m.Markdown.IsNull() {
			return out, errors.New("text blocks need markdown")
		}
		if !m.Title.IsNull() || !m.Description.IsNull() || len(m.Series) > 0 {
			return out, errors.New("text blocks take only markdown and metadata_json, not title, description or series")
		}
		out.Markdown = m.Markdown.ValueStringPointer()
		return out, nil
	}
	if m.Title.IsNull() {
		return out, errors.New("charts need a title")
	}
	if !m.Markdown.IsNull() {
		return out, errors.New("markdown is only for charts of type \"text\"")
	}
	out.Title = m.Title.ValueStringPointer()
	out.Description = m.Description.ValueStringPointer()
	out.Series = make([]dashboardSeriesAPI, 0, len(m.Series))
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
	metric, err := dashboardMetricFromModel(m.Metric)
	if err != nil {
		return dashboardSeriesAPI{}, err
	}
	out := dashboardSeriesAPI{Name: m.Name.ValueString(), Metadata: metadata, MetricDefinition: metric}

	switch {
	case len(m.ProjectIDs) > 0 && len(m.DatasetIDs) > 0:
		return out, errors.New("set project_ids or dataset_ids, not both")
	case len(m.ProjectIDs) > 0:
		out.FilterDefinition = &dashboardFilterAPI{
			SourceType:  "tracing_project",
			ProjectIDs:  stringValues(m.ProjectIDs),
			RunFilter:   m.RunFilter.ValueStringPointer(),
			TraceFilter: m.TraceFilter.ValueStringPointer(),
			TreeFilter:  m.TreeFilter.ValueStringPointer(),
		}
	case len(m.DatasetIDs) > 0:
		if !m.RunFilter.IsNull() || !m.TraceFilter.IsNull() || !m.TreeFilter.IsNull() {
			return out, errors.New("run_filter, trace_filter and tree_filter only apply with project_ids")
		}
		out.FilterDefinition = &dashboardFilterAPI{SourceType: "dataset", DatasetIDs: stringValues(m.DatasetIDs)}
	default:
		return out, errors.New("set project_ids or dataset_ids")
	}

	for _, group := range m.GroupBy {
		out.GroupByDefinitions = append(out.GroupByDefinitions, dashboardGroupByAPI{
			Attribute: group.Attribute.ValueString(),
			Path:      group.Path.ValueStringPointer(),
		})
	}
	return out, nil
}

func dashboardMetricFromModel(m *dashboardMetricModel) (*dashboardMetricAPI, error) {
	if m.Type.ValueString() != "ratio" {
		if m.Numerator != nil || m.Denominator != nil {
			return nil, errors.New("numerator and denominator are only for ratio metrics")
		}
		return dashboardLeafMetricFromModel(dashboardMetricLeafModel{
			Type: m.Type, Field: m.Field, Percentile: m.Percentile, FeedbackKey: m.FeedbackKey, Filter: m.Filter,
		})
	}
	if m.Numerator == nil || m.Denominator == nil {
		return nil, errors.New("ratio metrics need a numerator and a denominator")
	}
	if !m.Field.IsNull() || !m.Percentile.IsNull() || !m.FeedbackKey.IsNull() || !m.Filter.IsNull() {
		return nil, errors.New("ratio metrics set field, percentile, feedback_key and filter on the numerator and denominator")
	}
	numerator, err := dashboardLeafMetricFromModel(*m.Numerator)
	if err != nil {
		return nil, fmt.Errorf("numerator: %w", err)
	}
	denominator, err := dashboardLeafMetricFromModel(*m.Denominator)
	if err != nil {
		return nil, fmt.Errorf("denominator: %w", err)
	}
	return &dashboardMetricAPI{Type: "ratio", Numerator: numerator, Denominator: denominator}, nil
}

func dashboardLeafMetricFromModel(m dashboardMetricLeafModel) (*dashboardMetricAPI, error) {
	out := &dashboardMetricAPI{Type: m.Type.ValueString(), Filter: m.Filter.ValueStringPointer()}
	if out.Type == "count" {
		if !m.Field.IsNull() || !m.Percentile.IsNull() {
			return nil, errors.New("count metrics take no field or percentile")
		}
		if !m.FeedbackKey.IsNull() {
			entity := "feedback"
			out.Entity = &entity
			out.Params = &dashboardMetricParamsAPI{FeedbackKey: m.FeedbackKey.ValueStringPointer()}
		}
		return out, nil
	}

	if m.Field.IsNull() {
		return nil, fmt.Errorf("%s metrics need a field", out.Type)
	}
	out.Field = m.Field.ValueStringPointer()
	if m.FeedbackKey.IsNull() != (m.Field.ValueString() != "feedback_score") {
		return nil, errors.New("feedback_key is required with field = \"feedback_score\" and not allowed otherwise")
	}
	if (out.Type == "percentile") == m.Percentile.IsNull() {
		return nil, errors.New("percentile is required for percentile metrics and not allowed otherwise")
	}
	if !m.FeedbackKey.IsNull() || !m.Percentile.IsNull() {
		out.Params = &dashboardMetricParamsAPI{
			P:           m.Percentile.ValueFloat64Pointer(),
			FeedbackKey: m.FeedbackKey.ValueStringPointer(),
		}
	}
	return out, nil
}

// dashboardLayoutFromModel builds both breakpoints from the wide-screen rows:
// on narrow screens each chart gets a full-width row of its own.
func dashboardLayoutFromModel(rows []dashboardRowModel) (*dashboardLayoutAPI, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	layout := &dashboardLayoutAPI{Version: 1}
	layout.Breakpoints.SM.Rows = []dashboardLayoutRowAPI{}
	for i, row := range rows {
		widths := defaultLayoutWidths(len(row.Charts), dashboardMediumWidthUnits)
		widthsSet := 0
		for _, item := range row.Charts {
			if !item.WidthUnits.IsNull() {
				widthsSet++
			}
		}
		if widthsSet != 0 && widthsSet != len(row.Charts) {
			return nil, fmt.Errorf("layout row %d: set width_units on every chart in the row or on none", i)
		}

		mdRow := dashboardLayoutRowAPI{HeightUnits: row.HeightUnits.ValueInt64()}
		for j, item := range row.Charts {
			width := widths[j]
			if widthsSet > 0 {
				width = item.WidthUnits.ValueInt64()
			}
			mdRow.Items = append(mdRow.Items, dashboardLayoutItemAPI{ChartKey: item.Key.ValueString(), WidthUnits: width})
			layout.Breakpoints.SM.Rows = append(layout.Breakpoints.SM.Rows, dashboardLayoutRowAPI{
				HeightUnits: mdRow.HeightUnits,
				Items:       []dashboardLayoutItemAPI{{ChartKey: item.Key.ValueString(), WidthUnits: dashboardSmallWidthUnits}},
			})
		}
		layout.Breakpoints.MD.Rows = append(layout.Breakpoints.MD.Rows, mdRow)
	}
	return layout, nil
}

// API -> Terraform model

// dashboardModelFromAPI converts an API definition to the Terraform model.
// Where the API and prior hold equivalent values (metadata JSON with
// different formatting, or layout widths the row would get by default), it
// keeps prior's value so a refresh does not report a change.
func dashboardModelFromAPI(api dashboardDefinitionAPI, prior dashboardResourceModel) (dashboardResourceModel, error) {
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
		model, err := dashboardChartModelFromAPI(chart, priorCharts[chart.Key])
		if err != nil {
			return out, fmt.Errorf("chart %q: %w", chart.Key, err)
		}
		out.Charts = append(out.Charts, model)
	}
	// Keep an empty list when prior had one, rather than flip it to null.
	if out.Charts == nil && prior.Charts != nil {
		out.Charts = []dashboardChartModel{}
	}
	out.Layout = dashboardLayoutModelFromAPI(api.Layout, prior.Layout)
	return out, nil
}

func dashboardChartModelFromAPI(api dashboardChartAPI, prior dashboardChartModel) (dashboardChartModel, error) {
	out := dashboardChartModel{
		Key:          types.StringValue(api.Key),
		Type:         types.StringValue(api.ChartType),
		Title:        types.StringPointerValue(api.Title),
		Description:  types.StringPointerValue(api.Description),
		Markdown:     types.StringPointerValue(api.Markdown),
		MetadataJSON: jsonStringFromObject(api.Metadata, prior.MetadataJSON),
	}
	if api.ChartType == dashboardTextChartType {
		return out, nil
	}
	for i, series := range api.Series {
		var priorSeries dashboardSeriesModel
		if i < len(prior.Series) {
			priorSeries = prior.Series[i]
		}
		model, err := dashboardSeriesModelFromAPI(series, priorSeries)
		if err != nil {
			return out, fmt.Errorf("series %q: %w", series.Name, err)
		}
		out.Series = append(out.Series, model)
	}
	return out, nil
}

func dashboardSeriesModelFromAPI(api dashboardSeriesAPI, prior dashboardSeriesModel) (dashboardSeriesModel, error) {
	if api.MetricDefinition == nil || api.FilterDefinition == nil {
		return dashboardSeriesModel{}, errors.New("this series uses the legacy chart format, which langsmith_dashboard " +
			"cannot manage; edit it in the LangSmith UI to upgrade it, or manage the dashboard with langsmith_dashboard_json")
	}
	metric, err := dashboardMetricModelFromAPI(*api.MetricDefinition)
	if err != nil {
		return dashboardSeriesModel{}, err
	}
	out := dashboardSeriesModel{
		Name:         types.StringValue(api.Name),
		Metric:       metric,
		RunFilter:    types.StringPointerValue(api.FilterDefinition.RunFilter),
		TraceFilter:  types.StringPointerValue(api.FilterDefinition.TraceFilter),
		TreeFilter:   types.StringPointerValue(api.FilterDefinition.TreeFilter),
		MetadataJSON: jsonStringFromObject(api.Metadata, prior.MetadataJSON),
	}
	switch api.FilterDefinition.SourceType {
	case "tracing_project":
		out.ProjectIDs = stringModels(api.FilterDefinition.ProjectIDs)
	case "dataset":
		out.DatasetIDs = stringModels(api.FilterDefinition.DatasetIDs)
	default:
		return out, fmt.Errorf("unsupported filter source_type %q", api.FilterDefinition.SourceType)
	}
	for _, group := range api.GroupByDefinitions {
		out.GroupBy = append(out.GroupBy, dashboardGroupByModel{
			Attribute: types.StringValue(group.Attribute),
			Path:      types.StringPointerValue(group.Path),
		})
	}
	return out, nil
}

func dashboardMetricModelFromAPI(api dashboardMetricAPI) (*dashboardMetricModel, error) {
	if api.Type != "ratio" {
		leaf := dashboardLeafMetricModelFromAPI(api)
		return &dashboardMetricModel{
			Type: leaf.Type, Field: leaf.Field, Percentile: leaf.Percentile, FeedbackKey: leaf.FeedbackKey, Filter: leaf.Filter,
			Numerator: nil, Denominator: nil,
		}, nil
	}
	if api.Numerator == nil || api.Denominator == nil {
		return nil, errors.New("ratio metric is missing its numerator or denominator")
	}
	numerator := dashboardLeafMetricModelFromAPI(*api.Numerator)
	denominator := dashboardLeafMetricModelFromAPI(*api.Denominator)
	return &dashboardMetricModel{
		Type: types.StringValue("ratio"), Field: types.StringNull(), Percentile: types.Float64Null(),
		FeedbackKey: types.StringNull(), Filter: types.StringNull(),
		Numerator: &numerator, Denominator: &denominator,
	}, nil
}

func dashboardLeafMetricModelFromAPI(api dashboardMetricAPI) dashboardMetricLeafModel {
	out := dashboardMetricLeafModel{
		Type:        types.StringValue(api.Type),
		Field:       types.StringPointerValue(api.Field),
		Percentile:  types.Float64Null(),
		FeedbackKey: types.StringNull(),
		Filter:      types.StringPointerValue(api.Filter),
	}
	if api.Params != nil {
		out.Percentile = types.Float64PointerValue(api.Params.P)
		out.FeedbackKey = types.StringPointerValue(api.Params.FeedbackKey)
	}
	return out
}

// dashboardLayoutModelFromAPI reads the wide-screen rows. The narrow-screen
// rows are always derived from them, so they are not read back.
func dashboardLayoutModelFromAPI(api *dashboardLayoutAPI, prior []dashboardRowModel) []dashboardRowModel {
	if api == nil {
		if prior != nil && len(prior) == 0 {
			return []dashboardRowModel{}
		}
		return nil
	}
	var out []dashboardRowModel
	for i, row := range api.Breakpoints.MD.Rows {
		var priorRow dashboardRowModel
		if i < len(prior) {
			priorRow = prior[i]
		}
		// Keep widths unset when the config left them unset and the row still
		// has the widths that implies.
		keepUnset := len(priorRow.Charts) == len(row.Items) &&
			!slices.ContainsFunc(priorRow.Charts, func(item dashboardRowItemModel) bool { return !item.WidthUnits.IsNull() })
		defaults := defaultLayoutWidths(len(row.Items), dashboardMediumWidthUnits)
		for j, item := range row.Items {
			if item.WidthUnits != defaults[j] {
				keepUnset = false
			}
		}

		model := dashboardRowModel{HeightUnits: types.Int64Value(row.HeightUnits)}
		for _, item := range row.Items {
			width := types.Int64Value(item.WidthUnits)
			if keepUnset {
				width = types.Int64Null()
			}
			model.Charts = append(model.Charts, dashboardRowItemModel{Key: types.StringValue(item.ChartKey), WidthUnits: width})
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
