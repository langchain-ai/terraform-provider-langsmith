package provider

// Wire types and helpers shared by langsmith_dashboard and
// langsmith_dashboard_json. Both manage a dashboard (a chart "section" in the
// API) through its definition endpoints, which read and replace the
// dashboard's title, description, charts and layout in one call.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func dashboardPath(id string) string {
	return "api/v1/charts/section/" + url.PathEscape(id)
}

func dashboardDefinitionPath(id string) string {
	return dashboardPath(id) + "/definition"
}

// dashboardMediumWidthUnits is the width of the API's wide-screen layout
// grid. The resources send only that breakpoint; the API derives the
// narrow-screen one.
const dashboardMediumWidthUnits = 120

// newDashboardID returns an ID for a new dashboard. Choosing it here, and
// creating the dashboard with PUT, makes a retried create safe.
func newDashboardID() string {
	return uuid.Must(uuid.NewV7()).String()
}

// dashboardETagKey is the private state key holding the dashboard's entity
// tag from the last read or write.
const dashboardETagKey = "etag"

type privateStateReader interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
}

type privateStateWriter interface {
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

func loadDashboardETag(ctx context.Context, private privateStateReader) (string, diag.Diagnostics) {
	raw, diags := private.GetKey(ctx, dashboardETagKey)
	if diags.HasError() || len(raw) == 0 {
		return "", diags
	}
	var etag string
	if err := json.Unmarshal(raw, &etag); err != nil {
		return "", diags
	}
	return etag, diags
}

func saveDashboardETag(ctx context.Context, private privateStateWriter, etag string) diag.Diagnostics {
	if etag == "" {
		return nil
	}
	raw, _ := json.Marshal(etag)
	return private.SetKey(ctx, dashboardETagKey, raw)
}

// getDashboardDefinition reads a definition and returns its entity tag.
func getDashboardDefinition(ctx context.Context, client *langsmith.Client, id string, workspaceID types.String, result any) (string, error) {
	var httpResp *http.Response
	opts := append(workspaceOpts(workspaceID), option.WithResponseInto(&httpResp))
	err := client.Get(ctx, dashboardDefinitionPath(id), nil, result, opts...)
	return responseETag(httpResp), err
}

// putDashboardDefinition creates or replaces a definition and returns its new
// entity tag. With ifMatch set, the API refuses the write if the dashboard has
// changed since that tag was read.
func putDashboardDefinition(ctx context.Context, client *langsmith.Client, id string, workspaceID types.String, ifMatch string, body, result any) (string, error) {
	var httpResp *http.Response
	opts := append(workspaceOpts(workspaceID), option.WithResponseInto(&httpResp))
	if ifMatch != "" {
		opts = append(opts, option.WithHeader("If-Match", ifMatch))
	}
	err := client.Put(ctx, dashboardDefinitionPath(id), body, result, opts...)
	if isLangSmithStatus(err, http.StatusPreconditionFailed) {
		return "", errors.New("the dashboard changed in LangSmith after Terraform last read it; " +
			"run terraform plan again to see the change before applying")
	}
	return responseETag(httpResp), err
}

func responseETag(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	return resp.Header.Get("ETag")
}

func isLangSmithStatus(err error, status int) bool {
	var apiErr *langsmith.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}

type dashboardDefinitionAPI struct {
	ID          string              `json:"id,omitempty"`
	Title       string              `json:"title"`
	Description *string             `json:"description"`
	Charts      []dashboardChartAPI `json:"charts"`
	Layout      *dashboardLayoutAPI `json:"layout"`
}

// dashboardChartAPI is a chart or, when ChartType is "text", a text block.
type dashboardChartAPI struct {
	ID          *string              `json:"id,omitempty"`
	Key         string               `json:"key"`
	ChartType   string               `json:"chart_type"`
	Title       *string              `json:"title,omitempty"`
	Description *string              `json:"description,omitempty"`
	Markdown    *string              `json:"markdown,omitempty"`
	Series      []dashboardSeriesAPI `json:"series,omitempty"`
	Metadata    map[string]any       `json:"metadata,omitempty"`
}

type dashboardSeriesAPI struct {
	Name               string                `json:"name"`
	Metadata           map[string]any        `json:"metadata,omitempty"`
	MetricDefinition   *dashboardMetricAPI   `json:"metric_definition,omitempty"`
	GroupByDefinitions []dashboardGroupByAPI `json:"group_by_definitions,omitempty"`
	FilterDefinition   *dashboardFilterAPI   `json:"filter_definition,omitempty"`
	// Metric is set on legacy series, which the typed resource cannot manage.
	Metric *string `json:"metric,omitempty"`
}

type dashboardMetricAPI struct {
	Type        string                    `json:"type"`
	Entity      *string                   `json:"entity,omitempty"`
	Field       *string                   `json:"field,omitempty"`
	Filter      *string                   `json:"filter,omitempty"`
	Params      *dashboardMetricParamsAPI `json:"params,omitempty"`
	Numerator   *dashboardMetricAPI       `json:"numerator,omitempty"`
	Denominator *dashboardMetricAPI       `json:"denominator,omitempty"`
}

type dashboardMetricParamsAPI struct {
	P           *float64 `json:"p,omitempty"`
	FeedbackKey *string  `json:"feedback_key,omitempty"`
}

type dashboardGroupByAPI struct {
	Attribute string  `json:"attribute"`
	Path      *string `json:"path,omitempty"`
}

type dashboardFilterAPI struct {
	SourceType  string   `json:"source_type"`
	ProjectIDs  []string `json:"project_ids,omitempty"`
	DatasetIDs  []string `json:"dataset_ids,omitempty"`
	RunFilter   *string  `json:"run_filter,omitempty"`
	TraceFilter *string  `json:"trace_filter,omitempty"`
	TreeFilter  *string  `json:"tree_filter,omitempty"`
}

type dashboardLayoutAPI struct {
	Version     int                           `json:"version"`
	Breakpoints dashboardLayoutBreakpointsAPI `json:"breakpoints"`
}

type dashboardLayoutBreakpointsAPI struct {
	SM *dashboardBreakpointAPI `json:"sm,omitempty"`
	MD dashboardBreakpointAPI  `json:"md"`
}

type dashboardBreakpointAPI struct {
	Rows []dashboardLayoutRowAPI `json:"rows"`
}

type dashboardLayoutRowAPI struct {
	HeightUnits int64                    `json:"height_units"`
	Items       []dashboardLayoutItemAPI `json:"items"`
}

type dashboardLayoutItemAPI struct {
	ChartKey   string `json:"chart_key"`
	WidthUnits int64  `json:"width_units"`
}

// defaultLayoutWidths splits a row's width across its charts the way the
// LangSmith UI and API do: evenly, with any remainder handed out alternately
// from the right and left ends.
func defaultLayoutWidths(count int, total int64) []int64 {
	if count <= 0 {
		return nil
	}
	base, remainder := total/int64(count), total%int64(count)
	widths := make([]int64, count)
	for i := range widths {
		widths[i] = base
	}
	for i := 0; i < int(remainder); i++ {
		target := (i - 1) / 2
		if i%2 == 0 {
			target = count - 1 - i/2
		}
		widths[target]++
	}
	return widths
}
