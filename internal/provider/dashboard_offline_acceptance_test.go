package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
					resource.TestCheckNoResourceAttr("langsmith_dashboard.test", "layout.0.charts.0.width_units"),
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
					resource.TestCheckResourceAttr("langsmith_dashboard.test", "layout.0.charts.0.width_units", "80"),
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
			"key": "runs", "title": "Runs", "chart_type": "bar",
			"series": []any{map[string]any{
				"name":              "runs",
				"metric_definition": map[string]any{"type": "count"},
				"filter_definition": map[string]any{"source_type": "tracing_project", "project_ids": []any{dashboardTestProjectID}},
			}},
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
    { height_units = 4, charts = [{ key = "requests" }, { key = "latency" }] },
    { height_units = 3, charts = [{ key = "errors" }, { key = "notes" }] },
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
    { height_units = 4, charts = [{ key = "requests", width_units = 80 }, { key = "latency", width_units = 40 }] },
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
      series = [{
        name        = "p99"
        metric      = { type = "percentile", field = "latency_seconds", percentile = 0.99 }
        project_ids = [local.project_id]
      }]
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
}

func (b *dashboardContractBackend) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()

	path := strings.TrimPrefix(req.URL.Path, "/")
	sectionPrefix := "api/v1/charts/section/"
	switch {
	case path == dashboardDefinitionCreatePath && req.Method == http.MethodPost:
		b.nextID++
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", b.nextID)
		b.write(w, req, id)
	case strings.HasSuffix(path, "/definition") && strings.HasPrefix(path, sectionPrefix):
		id := strings.TrimSuffix(strings.TrimPrefix(path, sectionPrefix), "/definition")
		if _, ok := b.dashboards[id]; !ok {
			http.Error(w, `{"detail":"Section not found"}`, http.StatusNotFound)
			return
		}
		switch req.Method {
		case http.MethodGet:
			writeDashboardJSON(w, b.dashboards[id])
		case http.MethodPut:
			b.write(w, req, id)
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

func (b *dashboardContractBackend) write(w http.ResponseWriter, req *http.Request, id string) {
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
	stored := fillDashboardDefaults(definition)
	stored["id"] = id
	b.dashboards[id] = stored
	writeDashboardJSON(w, stored)
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
// returns for anything a request leaves out.
func fillDashboardDefaults(definition map[string]any) map[string]any {
	setDefault := func(object map[string]any, key string, value any) {
		if _, ok := object[key]; !ok {
			object[key] = value
		}
	}
	setDefault(definition, "description", nil)
	setDefault(definition, "layout", nil)
	setDefault(definition, "charts", []any{})
	for _, rawChart := range definition["charts"].([]any) {
		chart := rawChart.(map[string]any)
		setDefault(chart, "metadata", nil)
		if chart["chart_type"] == dashboardTextChartType {
			continue
		}
		setDefault(chart, "description", nil)
		setDefault(chart, "common_filters", nil)
		for _, rawSeries := range chart["series"].([]any) {
			series := rawSeries.(map[string]any)
			for _, key := range []string{"metadata", "filters", "metric", "project_metric", "feedback_key", "workspace_id", "group_by", "group_by_definitions"} {
				setDefault(series, key, nil)
			}
			metric := series["metric_definition"].(map[string]any)
			setDefault(metric, "filter", nil)
			for _, side := range []string{"numerator", "denominator"} {
				if leaf, ok := metric[side].(map[string]any); ok {
					setDefault(leaf, "filter", nil)
				}
			}
			filter := series["filter_definition"].(map[string]any)
			if filter["source_type"] == "tracing_project" {
				for _, key := range []string{"run_filter", "trace_filter", "tree_filter"} {
					setDefault(filter, key, nil)
				}
			}
		}
	}
	if layout, ok := definition["layout"].(map[string]any); ok {
		setDefault(layout, "version", float64(1))
	}
	return definition
}

func writeDashboardJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
