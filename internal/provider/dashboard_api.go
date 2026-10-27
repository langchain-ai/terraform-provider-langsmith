package provider

// Wire types and helpers shared by langsmith_dashboard and
// langsmith_dashboard_json. Both manage a dashboard (a chart "section" in the
// API) through its definition endpoints, which read and replace the
// dashboard's title, description, charts and layout in one call.

import (
	"net/url"
)

const dashboardDefinitionCreatePath = "api/v1/charts/section/definition"

func dashboardPath(id string) string {
	return "api/v1/charts/section/" + url.PathEscape(id)
}

func dashboardDefinitionPath(id string) string {
	return dashboardPath(id) + "/definition"
}

// Layout grid widths, in width units, of the API's two breakpoints.
const (
	dashboardSmallWidthUnits  = 60
	dashboardMediumWidthUnits = 120
)

type dashboardDefinitionAPI struct {
	ID          string              `json:"id,omitempty"`
	Title       string              `json:"title"`
	Description *string             `json:"description"`
	Charts      []dashboardChartAPI `json:"charts"`
	Layout      *dashboardLayoutAPI `json:"layout"`
}

// dashboardChartAPI is a chart or, when ChartType is "text", a text block.
type dashboardChartAPI struct {
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
	SM dashboardBreakpointAPI `json:"sm"`
	MD dashboardBreakpointAPI `json:"md"`
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
