package provider

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

const dashboardTestProjectID = "11111111-1111-4111-8111-111111111111"

// TestAccDashboardResourcesOffline drives langsmith_dashboard and
// langsmith_dashboard_json through real plans and applies against a fake API
// that, like the real one, fills in null optional fields on every read.
func TestAccDashboardResourcesOffline(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the offline Terraform acceptance test")
	}

	backend := &dashboardContractBackend{t: t, dashboards: map[string]map[string]any{}}
	server := httptest.NewServer(backend)
	t.Cleanup(server.Close)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(New("test")()),
		},
		Steps: []resource.TestStep{
			{
				Config: dashboardAcceptanceConfig(server.URL, "created"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("langsmith_dashboard.test", "id"),
					resource.TestCheckResourceAttr("langsmith_dashboard.test", "charts.#", "4"),
					resource.TestCheckResourceAttr("langsmith_dashboard.test", "charts.1.series.0.metric.percentile", "0.99"),
					resource.TestCheckResourceAttr("langsmith_dashboard.test", "charts.1.series.1.name", "p50"),
					resource.TestCheckNoResourceAttr("langsmith_dashboard.test", "layout.0.charts.0.width"),
					resource.TestCheckResourceAttrSet("langsmith_dashboard_json.test", "id"),
				),
			},
			// Server-filled nulls and default widths are not drift.
			{
				Config:             dashboardAcceptanceConfig(server.URL, "created"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
			{
				Config: dashboardAcceptanceConfig(server.URL, "updated"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("langsmith_dashboard.test", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("langsmith_dashboard_json.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("langsmith_dashboard.test", "title", "Service health (updated)"),
					resource.TestCheckResourceAttr("langsmith_dashboard.test", "charts.#", "3"),
					resource.TestCheckResourceAttr("langsmith_dashboard.test", "layout.0.charts.0.width", "80"),
					resource.TestCheckResourceAttr("langsmith_dashboard_json.test", "dashboard", dashboardJSONConfigValue("updated")),
				),
			},
			{
				Config:             dashboardAcceptanceConfig(server.URL, "updated"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
			{
				ResourceName:      "langsmith_dashboard.test",
				ImportState:       true,
				ImportStateVerify: true,
				// With no configuration to follow, an import lists series in the
				// order the API returns them, sorted by name.
				ImportStateVerifyIgnore: []string{"charts.1.series"},
			},
			// A change made outside Terraform shows up in the next plan.
			{
				PreConfig: func() {
					backend.editTitles("Edited in the UI")
				},
				Config:             dashboardAcceptanceConfig(server.URL, "updated"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: dashboardAcceptanceConfig(server.URL, "updated"),
				Check:  resource.TestCheckResourceAttr("langsmith_dashboard.test", "title", "Service health (updated)"),
			},
		},
	})

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.conditionalWrites == 0 {
		t.Fatal("no update sent a matching If-Match")
	}
	if len(backend.dashboards) != 0 {
		t.Fatalf("Terraform destroy left %d dashboards", len(backend.dashboards))
	}
}

func dashboardJSONConfigValue(phase string) string {
	title := "From JSON"
	if phase == "updated" {
		title = "From JSON (updated)"
	}
	definition := map[string]any{
		"title": title,
		"charts": []any{map[string]any{
			"key": "runs", "title": "Runs", "type": "bar",
			"series": []any{
				map[string]any{"name": "runs", "metric": map[string]any{"type": "count"}, "project_ids": []any{dashboardTestProjectID}},
				// Out of name order: the comparison ignores series order.
				map[string]any{"name": "errors", "metric": map[string]any{"type": "count", "filter": "eq(error, true)"}, "project_ids": []any{dashboardTestProjectID}},
			},
		}},
	}
	out, _ := json.Marshal(definition)
	return string(out)
}

func dashboardAcceptanceConfig(apiURL, phase string) string {
	title, latencyTitle, charts, layout := "Service health", "p99 latency", `
    {
      key   = "errors"
      type  = "bar"
      title = "Errors"
      series = [{
        name        = "errors"
        metric      = { type = "count", filter = "eq(status, \"error\")" }
        project_ids = [local.project_id]
        group_by    = [{ attribute = "metadata", path = "env" }]
      }]
    },
    {
      key      = "notes"
      type     = "text"
      markdown = "Owned by the platform team."
    },`, `
  layout = [
    { height = 4, charts = [{ key = "requests" }, { key = "latency" }] },
    { height = 3, charts = [{ key = "errors" }, { key = "notes" }] },
  ]`
	if phase == "updated" {
		title, latencyTitle, charts, layout = "Service health (updated)", "Latency", `
    {
      key   = "error_rate"
      type  = "kpi"
      title = "Error rate"
      series = [{
        name = "error rate"
        metric = {
          type        = "ratio"
          numerator   = { type = "count", filter = "eq(status, \"error\")" }
          denominator = { type = "count" }
        }
        project_ids = [local.project_id]
      }]
    },`, `
  layout = [
    { height = 4, charts = [{ key = "requests", width = 80 }, { key = "latency", width = 40 }] },
  ]`
	}

	return fmt.Sprintf(`
provider "langsmith" {
  api_url      = %[1]q
  api_key      = "offline-test-key"
  workspace_id = "offline-workspace"
}

locals {
  project_id = %[2]q
}

resource "langsmith_dashboard" "test" {
  title       = %[3]q
  description = "Managed by Terraform"

  charts = [
    {
      key   = "requests"
      type  = "line"
      title = "Requests"
      series = [{
        name          = "runs"
        metric        = { type = "count" }
        project_ids   = [local.project_id]
        run_filter    = "eq(is_root, true)"
        metadata_json = jsonencode({ value_format = "number" })
      }]
    },
    {
      key   = "latency"
      type  = "line"
      title = %[4]q
      # Listed out of name order: the API returns series sorted by name.
      series = [
        {
          name        = "p99"
          metric      = { type = "percentile", field = "latency_seconds", percentile = 0.99 }
          project_ids = [local.project_id]
        },
        {
          name        = "p50"
          metric      = { type = "percentile", field = "latency_seconds", percentile = 0.5 }
          project_ids = [local.project_id]
        },
      ]
    },%[5]s
  ]
%[6]s
}

resource "langsmith_dashboard_json" "test" {
  dashboard = %[7]q
}
`, apiURL, dashboardTestProjectID, title, latencyTitle, charts, layout, dashboardJSONConfigValue(phase))
}

// dashboardContractBackend is an in-memory stand-in for the dashboard
// definition endpoints.
type dashboardContractBackend struct {
	t          *testing.T
	mu         sync.Mutex
	nextID     int
	dashboards map[string]map[string]any
	// conditionalWrites counts writes that sent a matching If-Match.
	conditionalWrites int
}

func (b *dashboardContractBackend) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()

	path := strings.TrimPrefix(req.URL.Path, "/")
	sectionPrefix := "api/v1/charts/section/"
	switch {
	case strings.HasSuffix(path, "/definition") && strings.HasPrefix(path, sectionPrefix):
		id := strings.TrimSuffix(strings.TrimPrefix(path, sectionPrefix), "/definition")
		stored, exists := b.dashboards[id]
		switch req.Method {
		case http.MethodGet:
			if !exists {
				http.Error(w, `{"detail":"Section not found"}`, http.StatusNotFound)
				return
			}
			w.Header().Set("ETag", dashboardTestETag(stored))
			writeDashboardJSON(w, stored)
		case http.MethodPut:
			if ifMatch := req.Header.Get("If-Match"); ifMatch != "" {
				if !exists || ifMatch != dashboardTestETag(stored) {
					http.Error(w, `{"detail":"Dashboard has changed since it was read"}`, http.StatusPreconditionFailed)
					return
				}
				b.conditionalWrites++
			}
			b.write(w, req, id, !exists)
		default:
			b.reject(w, req, "unexpected method")
		}
	case strings.HasPrefix(path, sectionPrefix) && req.Method == http.MethodDelete:
		delete(b.dashboards, strings.TrimPrefix(path, sectionPrefix))
		writeDashboardJSON(w, nil)
	default:
		b.reject(w, req, "unexpected route")
	}
}

func (b *dashboardContractBackend) write(w http.ResponseWriter, req *http.Request, id string, created bool) {
	if req.Header.Get("X-Tenant-Id") != "offline-workspace" {
		b.reject(w, req, "missing workspace header")
		return
	}
	var definition map[string]any
	if err := json.NewDecoder(req.Body).Decode(&definition); err != nil {
		b.reject(w, req, err.Error())
		return
	}
	if _, ok := definition["id"]; ok {
		b.reject(w, req, "request body carries an id")
		return
	}
	previous := b.dashboards[id]
	stored := fillDashboardDefaults(definition)
	stored["id"] = id
	b.assignChartIDs(stored, previous)
	b.dashboards[id] = stored
	w.Header().Set("ETag", dashboardTestETag(stored))
	if created {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
	}
	writeDashboardJSON(w, stored)
}

// assignChartIDs keeps the ID of each chart whose key was already on the
// dashboard and gives new charts new IDs.
func (b *dashboardContractBackend) assignChartIDs(definition, previous map[string]any) {
	previousIDs := map[string]any{}
	if previousCharts, ok := previous["charts"].([]any); ok {
		for _, rawChart := range previousCharts {
			if chart, ok := rawChart.(map[string]any); ok {
				previousIDs[fmt.Sprint(chart["key"])] = chart["id"]
			}
		}
	}
	charts, _ := definition["charts"].([]any)
	for _, rawChart := range charts {
		chart, ok := rawChart.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := previousIDs[fmt.Sprint(chart["key"])]; ok {
			chart["id"] = id
			continue
		}
		b.nextID++
		chart["id"] = fmt.Sprintf("00000000-0000-4000-9000-%012d", b.nextID)
	}
}

func dashboardTestETag(definition map[string]any) string {
	raw, _ := json.Marshal(definition)
	return fmt.Sprintf("%q", fmt.Sprintf("%x", sha256.Sum256(raw)))
}

func (b *dashboardContractBackend) editTitles(title string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, dashboard := range b.dashboards {
		dashboard["title"] = title
	}
}

func (b *dashboardContractBackend) reject(w http.ResponseWriter, req *http.Request, reason string) {
	b.t.Errorf("%s %s: %s", req.Method, req.URL.RequestURI(), reason)
	http.Error(w, reason, http.StatusBadRequest)
}

// fillDashboardDefaults adds the null and default fields the real API
// returns for anything a request leaves out, sorts series by name, and fills
// in the even widths of layout rows that leave them unset.
func fillDashboardDefaults(definition map[string]any) map[string]any {
	setDefault := func(object map[string]any, key string, value any) {
		if _, ok := object[key]; !ok {
			object[key] = value
		}
	}
	var fillMetric func(metric map[string]any)
	fillMetric = func(metric map[string]any) {
		for _, key := range []string{"field", "percentile", "feedback_key", "filter", "numerator", "denominator"} {
			setDefault(metric, key, nil)
		}
		for _, side := range []string{"numerator", "denominator"} {
			if leaf, ok := metric[side].(map[string]any); ok {
				fillMetric(leaf)
			}
		}
	}
	setDefault(definition, "description", nil)
	setDefault(definition, "layout", nil)
	setDefault(definition, "charts", []any{})
	charts, _ := definition["charts"].([]any)
	for _, rawChart := range charts {
		chart, ok := rawChart.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"title", "description", "markdown", "metadata"} {
			setDefault(chart, key, nil)
		}
		setDefault(chart, "series", []any{})
		seriesList, _ := chart["series"].([]any)
		for _, rawSeries := range seriesList {
			series, ok := rawSeries.(map[string]any)
			if !ok {
				continue
			}
			for _, key := range []string{"project_ids", "dataset_ids", "run_filter", "trace_filter", "tree_filter", "group_by", "metadata"} {
				setDefault(series, key, nil)
			}
			if metric, ok := series["metric"].(map[string]any); ok {
				fillMetric(metric)
			}
		}
		sort.SliceStable(seriesList, func(i, j int) bool {
			return seriesName(seriesList[i]) < seriesName(seriesList[j])
		})
	}
	rows, _ := definition["layout"].([]any)
	for _, rawRow := range rows {
		row, _ := rawRow.(map[string]any)
		items, _ := row["charts"].([]any)
		widths := defaultLayoutWidths(len(items), dashboardMediumWidthUnits)
		for k, rawItem := range items {
			if item, ok := rawItem.(map[string]any); ok {
				setDefault(item, "width", float64(widths[k]))
			}
		}
	}
	return definition
}

func seriesName(series any) string {
	if object, ok := series.(map[string]any); ok {
		return fmt.Sprint(object["name"])
	}
	return ""
}

func writeDashboardJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
