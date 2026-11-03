package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestDashboardPathsUsePublicPrefix(t *testing.T) {
	for _, p := range []string{dashboardPath("id"), dashboardDefinitionPath("id")} {
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

func ptr[T any](v T) *T { return &v }

func seriesWithMetric(metric *dashboardMetricModel) dashboardSeriesModel {
	return dashboardSeriesModel{
		Name: types.StringValue("s"), Metric: metric, ProjectIDs: []types.String{types.StringValue("p")},
		RunFilter: types.StringNull(), TraceFilter: types.StringNull(), TreeFilter: types.StringNull(),
		MetadataJSON: types.StringNull(),
	}
}

// The configuration maps field for field onto the API's format, and reading
// the API's form back gives the same configuration.
func TestDashboardMetricRoundTrip(t *testing.T) {
	p99 := leafMetric("percentile")
	p99.Field, p99.Percentile = types.StringValue("latency_seconds"), types.Float64Value(0.99)
	errors := leafMetric("count")
	errors.Filter = types.StringValue(`eq(status, "error")`)
	ratio := metricModel(leafMetric("ratio"))
	ratio.Numerator, ratio.Denominator = &errors, ptr(leafMetric("count"))

	for name, tc := range map[string]struct {
		metric *dashboardMetricModel
		want   string
	}{
		"count":      {metricModel(leafMetric("count")), `{"type":"count"}`},
		"percentile": {metricModel(p99), `{"type":"percentile","field":"latency_seconds","percentile":0.99}`},
		"ratio": {ratio, `{"type":"ratio","numerator":{"type":"count","filter":"eq(status, \"error\")"},` +
			`"denominator":{"type":"count"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			series, err := dashboardSeriesFromModel(seriesWithMetric(tc.metric))
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(series.Metric)
			if string(raw) != tc.want {
				t.Fatalf("got %s, want %s", raw, tc.want)
			}
			back := dashboardSeriesModelFromAPI(series, dashboardSeriesModel{})
			again, _ := dashboardSeriesFromModel(back)
			if rawAgain, _ := json.Marshal(again.Metric); string(rawAgain) != tc.want {
				t.Fatalf("round trip gave %s", rawAgain)
			}
		})
	}
}

func TestDashboardLayoutRows(t *testing.T) {
	item := func(key string, width types.Int64) dashboardRowItemModel {
		return dashboardRowItemModel{Key: types.StringValue(key), Width: width}
	}
	rows := []dashboardRowModel{
		{Height: types.Int64Value(4), Charts: []dashboardRowItemModel{item("a", types.Int64Null()), item("b", types.Int64Null())}},
		{Height: types.Int64Value(2), Charts: []dashboardRowItemModel{item("c", types.Int64Value(120))}},
	}
	definition, err := dashboardDefinitionFromModel(dashboardResourceModel{Title: types.StringValue("t"), Layout: rows})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(definition.Layout)
	want := `[{"height":4,"charts":[{"key":"a"},{"key":"b"}]},{"height":2,"charts":[{"key":"c","width":120}]}]`
	if string(raw) != want {
		t.Fatalf("got  %s\nwant %s", raw, want)
	}

	// The API fills in widths; reading them back keeps unset widths unset.
	sixty, full := int64(60), int64(120)
	api := []dashboardRowAPI{
		{Height: 4, Charts: []dashboardRowChartAPI{{Key: "a", Width: &sixty}, {Key: "b", Width: &sixty}}},
		{Height: 2, Charts: []dashboardRowChartAPI{{Key: "c", Width: &full}}},
	}
	back := dashboardLayoutModelFromAPI(api, rows)
	if !back[0].Charts[0].Width.IsNull() || back[1].Charts[0].Width.ValueInt64() != 120 {
		t.Fatalf("unexpected widths: %+v", back)
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

func TestPairSeriesWithPriorKeepsConfigOrder(t *testing.T) {
	series := func(name string) dashboardSeriesModel {
		s := seriesWithMetric(metricModel(leafMetric("count")))
		s.Name = types.StringValue(name)
		return s
	}
	apiForm := func(m dashboardSeriesModel) dashboardSeriesAPI {
		out, err := dashboardSeriesFromModel(m)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	prior := []dashboardSeriesModel{series("b"), series("a"), series("c")}
	changed := series("c")
	changed.RunFilter = types.StringValue("eq(is_root, true)")
	// The API returns series sorted by name; "c" changed outside Terraform.
	api := []dashboardSeriesAPI{apiForm(series("a")), apiForm(series("b")), apiForm(changed)}

	var names []string
	for _, pair := range pairSeriesWithPrior(api, prior) {
		names = append(names, pair.api.Name)
	}
	if strings.Join(names, ",") != "b,a,c" {
		t.Fatalf("order = %v, want b,a,c with the changed series last", names)
	}
}

func TestJSONSubsetIgnoresSeriesOrderAndChartIDs(t *testing.T) {
	var want, live map[string]any
	_ = json.Unmarshal([]byte(`{"charts":[{"id":"old","key":"a","series":[{"name":"x"},{"name":"y"}]}]}`), &want)
	_ = json.Unmarshal([]byte(`{"charts":[{"id":"new","key":"a","series":[{"name":"y"},{"name":"x"}]}]}`), &live)
	if !jsonSubset(withoutChartIDs(want), withoutChartIDs(live)) {
		t.Fatal("series order or chart IDs counted as drift")
	}
	_ = json.Unmarshal([]byte(`{"charts":[{"id":"new","key":"a","series":[{"name":"x"},{"name":"x"}]}]}`), &live)
	if jsonSubset(withoutChartIDs(want), withoutChartIDs(live)) {
		t.Fatal("a changed series was not detected")
	}
}
