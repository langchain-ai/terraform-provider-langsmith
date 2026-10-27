package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestDashboardPathsUsePublicPrefix(t *testing.T) {
	for _, p := range []string{dashboardDefinitionCreatePath, dashboardPath("id"), dashboardDefinitionPath("id")} {
		if !strings.HasPrefix(p, "api/") {
			t.Errorf("path %q must start with api/", p)
		}
	}
}

func TestDefaultLayoutWidthsMatchTheAPI(t *testing.T) {
	// Expected values come from the API's _get_default_layout_widths.
	for count, want := range map[int][]int64{
		1: {120},
		2: {60, 60},
		5: {24, 24, 24, 24, 24},
		7: {17, 17, 17, 17, 17, 17, 18},
	} {
		got := defaultLayoutWidths(count, dashboardMediumWidthUnits)
		if !slicesEqual(got, want) {
			t.Errorf("defaultLayoutWidths(%d) = %v, want %v", count, got, want)
		}
	}
	if got := defaultLayoutWidths(7, 60); !slicesEqual(got, []int64{9, 9, 8, 8, 8, 9, 9}) {
		t.Errorf("defaultLayoutWidths(7, 60) = %v", got)
	}
}

func slicesEqual(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func leafMetric(metricType string) dashboardMetricLeafModel {
	return dashboardMetricLeafModel{
		Type: types.StringValue(metricType), Field: types.StringNull(), Percentile: types.Float64Null(),
		FeedbackKey: types.StringNull(), Filter: types.StringNull(),
	}
}

func metricModel(leaf dashboardMetricLeafModel) *dashboardMetricModel {
	return &dashboardMetricModel{
		Type: leaf.Type, Field: leaf.Field, Percentile: leaf.Percentile, FeedbackKey: leaf.FeedbackKey, Filter: leaf.Filter,
	}
}

func TestDashboardMetricFromModel(t *testing.T) {
	feedbackCount := leafMetric("count")
	feedbackCount.FeedbackKey = types.StringValue("correctness")
	p99 := leafMetric("percentile")
	p99.Field, p99.Percentile = types.StringValue("latency_seconds"), types.Float64Value(0.99)
	feedbackAvg := leafMetric("avg")
	feedbackAvg.Field, feedbackAvg.FeedbackKey = types.StringValue("feedback_score"), types.StringValue("helpful")
	ratio := &dashboardMetricModel{
		Type: types.StringValue("ratio"), Field: types.StringNull(), Percentile: types.Float64Null(),
		FeedbackKey: types.StringNull(), Filter: types.StringNull(),
		Numerator: &feedbackCount, Denominator: ptr(leafMetric("count")),
	}

	for name, tc := range map[string]struct {
		metric *dashboardMetricModel
		want   string
	}{
		"count":          {metricModel(leafMetric("count")), `{"type":"count"}`},
		"feedback count": {metricModel(feedbackCount), `{"type":"count","entity":"feedback","params":{"feedback_key":"correctness"}}`},
		"percentile":     {metricModel(p99), `{"type":"percentile","field":"latency_seconds","params":{"p":0.99}}`},
		"feedback avg":   {metricModel(feedbackAvg), `{"type":"avg","field":"feedback_score","params":{"feedback_key":"helpful"}}`},
		"ratio": {ratio, `{"type":"ratio","numerator":{"type":"count","entity":"feedback","params":{"feedback_key":"correctness"}},` +
			`"denominator":{"type":"count"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := dashboardMetricFromModel(tc.metric)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(got)
			if string(raw) != tc.want {
				t.Fatalf("got %s, want %s", raw, tc.want)
			}
			// Reading the API form back gives the same model.
			back, err := dashboardMetricModelFromAPI(*got)
			if err != nil {
				t.Fatal(err)
			}
			again, _ := dashboardMetricFromModel(back)
			if rawAgain, _ := json.Marshal(again); string(rawAgain) != tc.want {
				t.Fatalf("round trip gave %s", rawAgain)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestDashboardMetricFromModelRejectsInvalidCombinations(t *testing.T) {
	missingField := leafMetric("sum")
	missingPercentile := leafMetric("percentile")
	missingPercentile.Field = types.StringValue("latency_seconds")
	feedbackWithoutKey := leafMetric("avg")
	feedbackWithoutKey.Field = types.StringValue("feedback_score")
	countWithField := leafMetric("count")
	countWithField.Field = types.StringValue("latency_seconds")
	ratioWithoutSides := metricModel(leafMetric("ratio"))
	countWithSides := metricModel(leafMetric("count"))
	countWithSides.Numerator = ptr(leafMetric("count"))

	for name, metric := range map[string]*dashboardMetricModel{
		"missing field":         metricModel(missingField),
		"missing percentile":    metricModel(missingPercentile),
		"feedback without key":  metricModel(feedbackWithoutKey),
		"count with field":      metricModel(countWithField),
		"ratio without sides":   ratioWithoutSides,
		"non-ratio with a side": countWithSides,
	} {
		if _, err := dashboardMetricFromModel(metric); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDashboardLayoutFromModel(t *testing.T) {
	item := func(key string, width types.Int64) dashboardRowItemModel {
		return dashboardRowItemModel{Key: types.StringValue(key), WidthUnits: width}
	}
	layout, err := dashboardLayoutFromModel([]dashboardRowModel{
		{HeightUnits: types.Int64Value(4), Charts: []dashboardRowItemModel{item("a", types.Int64Null()), item("b", types.Int64Null())}},
		{HeightUnits: types.Int64Value(2), Charts: []dashboardRowItemModel{item("c", types.Int64Value(120))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(layout)
	want := `{"version":1,"breakpoints":{` +
		`"sm":{"rows":[{"height_units":4,"items":[{"chart_key":"a","width_units":60}]},` +
		`{"height_units":4,"items":[{"chart_key":"b","width_units":60}]},` +
		`{"height_units":2,"items":[{"chart_key":"c","width_units":60}]}]},` +
		`"md":{"rows":[{"height_units":4,"items":[{"chart_key":"a","width_units":60},{"chart_key":"b","width_units":60}]},` +
		`{"height_units":2,"items":[{"chart_key":"c","width_units":120}]}]}}}`
	if string(raw) != want {
		t.Fatalf("got  %s\nwant %s", raw, want)
	}

	// Reading it back keeps widths unset where the config left them unset.
	rows := dashboardLayoutModelFromAPI(layout, []dashboardRowModel{
		{Charts: []dashboardRowItemModel{item("a", types.Int64Null()), item("b", types.Int64Null())}},
		{Charts: []dashboardRowItemModel{item("c", types.Int64Value(120))}},
	})
	if !rows[0].Charts[0].WidthUnits.IsNull() || rows[1].Charts[0].WidthUnits.ValueInt64() != 120 {
		t.Fatalf("unexpected widths: %+v", rows)
	}

	if _, err := dashboardLayoutFromModel([]dashboardRowModel{
		{HeightUnits: types.Int64Value(4), Charts: []dashboardRowItemModel{item("a", types.Int64Value(60)), item("b", types.Int64Null())}},
	}); err == nil {
		t.Fatal("expected an error for a row with some widths set")
	}
}

func TestDashboardModelFromAPIRejectsLegacySeries(t *testing.T) {
	legacy := "run_count"
	_, err := dashboardModelFromAPI(dashboardDefinitionAPI{
		ID: "id", Title: "t",
		Charts: []dashboardChartAPI{{Key: "a", ChartType: "line", Series: []dashboardSeriesAPI{{Name: "s", Metric: &legacy}}}},
	}, dashboardResourceModel{})
	if err == nil || !strings.Contains(err.Error(), "langsmith_dashboard_json") {
		t.Fatalf("err = %v, want a pointer to langsmith_dashboard_json", err)
	}
}

func TestJSONSubset(t *testing.T) {
	var live map[string]any
	_ = json.Unmarshal([]byte(`{"title":"t","description":null,"charts":[{"key":"a","metadata":null,"series":[1,2]}]}`), &live)
	for want, expected := range map[string]bool{
		`{"title":"t"}`:                                       true,
		`{"title":"t","description":null}`:                    true,
		`{"title":"t","layout":null}`:                         true,
		`{"charts":[{"key":"a","series":[1,2]}]}`:             true,
		`{"title":"other"}`:                                   false,
		`{"charts":[]}`:                                       false,
		`{"charts":[{"key":"a","series":[1]}]}`:               false,
		`{"title":"t","extra":"field"}`:                       false,
		`{"charts":[{"key":"a","metadata":{"x":1}}]}`:         false,
		`{"charts":[{"key":"a","series":[1,2]},{"key":"b"}]}`: false,
	} {
		var parsed map[string]any
		_ = json.Unmarshal([]byte(want), &parsed)
		if got := jsonSubset(parsed, live); got != expected {
			t.Errorf("jsonSubset(%s) = %t, want %t", want, got, expected)
		}
	}
}

func TestReconcileDashboardJSON(t *testing.T) {
	live := map[string]any{"title": "t", "description": nil, "charts": []any{}}
	configured := types.StringValue(`{"id":"exported-id","title":"t","charts":[]}`)
	got, err := reconcileDashboardJSON(configured, live)
	if err != nil || got != configured {
		t.Fatalf("got %v, %v; want the configured value kept", got, err)
	}

	live["title"] = "changed"
	got, err = reconcileDashboardJSON(configured, live)
	if err != nil || got.ValueString() != `{"charts":[],"description":null,"title":"changed"}` {
		t.Fatalf("got %v, %v; want the live definition", got, err)
	}
}
