package provider

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	frameworkresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestAccAgentOfflineLifecycle(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run offline Terraform acceptance tests")
	}
	for _, workspace := range []string{"", "override-workspace"} {
		t.Run("workspace="+workspace, func(t *testing.T) {
			backend := &agentContractBackend{t: t, workspace: firstNonEmpty(workspace, "provider-workspace")}
			server := httptest.NewServer(backend)
			defer server.Close()
			config := func(attributes string) string {
				selector := ""
				if workspace != "" {
					selector = fmt.Sprintf("workspace_id = %q", workspace)
				}
				return fmt.Sprintf(`
provider "langsmith" {
  api_url = %q
  api_key = "offline-test-key"
  workspace_id = "provider-workspace"
}
resource "langsmith_agent" "test" {
  agent_id = "test-agent"
  %s
  %s
}
data "langsmith_agent" "test" {
  agent_id = langsmith_agent.test.id
  workspace_id = langsmith_agent.test.workspace_id
}
resource "terraform_data" "production" {
  triggers_replace = langsmith_agent.test.environments["PRODUCTION"].tracer_session_id
}
`, server.URL+"/api/v1", selector, attributes)
			}
			mutate := func(change func()) func() { return func() { backend.mu.Lock(); defer backend.mu.Unlock(); change() } }
			importID := "test-agent"
			if workspace != "" {
				importID = workspace + "/" + importID
			}
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"langsmith": providerserver.NewProtocol6WithError(New("test")())},
				Steps: []resource.TestStep{
					{Config: config(""), Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("langsmith_agent.test", "name", "test-agent"),
						resource.TestCheckResourceAttr("langsmith_agent.test", "source_type", "EXTERNAL"),
						resource.TestCheckResourceAttr("langsmith_agent.test", "agent_type", "CUSTOM"),
						resource.TestCheckResourceAttr("langsmith_agent.test", "environments.%", "4"),
						resource.TestCheckResourceAttr("data.langsmith_agent.test", "environments.PRODUCTION.tracer_session_id", "project-PRODUCTION"),
						resource.TestCheckNoResourceAttr("langsmith_agent.test", "owner_id"),
					)},
					{Config: config(""), PlanOnly: true, ExpectNonEmptyPlan: false},
					{ResourceName: "langsmith_agent.test", ImportState: true, ImportStateId: importID, ImportStateVerify: true},
					{Config: config(`name = "Updated"`), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("terraform_data.production", plancheck.ResourceActionNoop)}}, Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("langsmith_agent.test", "name", "Updated"),
						resource.TestCheckResourceAttr("langsmith_agent.test", "environments.PRODUCTION.id", "env-PRODUCTION"),
					)},
					{PreConfig: mutate(func() { backend.agent["name"] = "Drifted" }), Config: config(`name = "Updated"`), PlanOnly: true, ExpectNonEmptyPlan: true},
					{Config: config(`name = "Updated"`), Check: resource.TestCheckResourceAttr("langsmith_agent.test", "name", "Updated")},
					{PreConfig: mutate(func() { backend.readProductionOnly = true }), Config: config(`name = "Filtered"`), Check: resource.TestCheckResourceAttr("langsmith_agent.test", "environments.%", "1")},
					{PreConfig: mutate(func() {
						backend.readProductionOnly = false
						backend.agent["name"] = "Unmanaged"
						backend.agent["environments"] = []map[string]string{{"id": "env-PRODUCTION", "agent_id": "test-agent", "environment": "PRODUCTION", "tracer_session_id": "project-PRODUCTION", "tracer_session_name": "test-agent"}}
					}), Config: config(""), PlanOnly: true, ExpectNonEmptyPlan: false},
					{Config: config(""), Check: resource.TestCheckResourceAttr("langsmith_agent.test", "environments.%", "1")},
					{ResourceName: "langsmith_agent.test", ImportState: true, ImportStateId: importID, ImportStateVerify: true},
					{Config: config(""), PlanOnly: true, ExpectNonEmptyPlan: false},
					{PreConfig: mutate(func() { backend.agent = nil }), Config: config(""), Check: resource.TestCheckResourceAttr("langsmith_agent.test", "environments.%", "4")},
					{Config: config(`source_type = "LSD"`), Check: resource.TestCheckResourceAttr("langsmith_agent.test", "source_type", "LSD")},
					{Config: config(`source_type = "EXTERNAL"`), ExpectError: regexp.MustCompile("source transition forbidden")},
					{Config: config(`source_type = "LSD"`), PlanOnly: true, ExpectNonEmptyPlan: false},
					{Config: strings.Replace(config(`source_type = "LSD"`), `workspace_id = "provider-workspace"`, `workspace_id = "different-provider-workspace"`, 1), PlanOnly: true, ExpectNonEmptyPlan: false},
				},
			})
			backend.mu.Lock()
			defer backend.mu.Unlock()
			if backend.agent != nil {
				t.Fatal("destroy left the agent behind")
			}
		})
	}
}

// Exercise state retention through the resource, including generic routing 404s.
func TestAgentReadFailurePreservesState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		removed bool
	}{
		{"absent", 404, `{"detail":"Agent not found"}`, true},
		{"unsupported route", 404, `{"detail":"Not Found"}`, false},
		{"forbidden", 403, `{"detail":"Agent is not accessible"}`, false},
		{"unauthenticated", 401, `{}`, false},
		{"server failure", 500, `{}`, false},
		{"invalid response", 200, `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/api/v1/agents/test-agent" || req.Header.Get("X-Tenant-Id") != "override-workspace" {
					t.Errorf("unexpected request: %s, workspace %s", req.URL.Path, req.Header.Get("X-Tenant-Id"))
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			r := &AgentResource{client: langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("offline-test-key"), option.WithTenantID("provider-workspace"), option.WithMaxRetries(0))}
			var schema frameworkresource.SchemaResponse
			r.Schema(t.Context(), frameworkresource.SchemaRequest{}, &schema)
			state := tfsdk.State{Schema: schema.Schema}
			data := agentModel{ID: types.StringValue("test-agent"), AgentID: types.StringValue("test-agent"), WorkspaceID: types.StringValue("override-workspace"), Environments: types.MapNull(agentEnvironmentType)}
			if diags := state.Set(t.Context(), data); diags.HasError() {
				t.Fatal(diags)
			}
			resp := frameworkresource.ReadResponse{State: state}
			r.Read(t.Context(), frameworkresource.ReadRequest{State: state}, &resp)
			if resp.State.Raw.IsNull() != tc.removed {
				t.Fatalf("removed = %t, want %t", resp.State.Raw.IsNull(), tc.removed)
			}
			if resp.Diagnostics.HasError() == tc.removed {
				t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
			}
		})
	}
}

func TestAccAgentOfflineReplacement(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run offline Terraform acceptance tests")
	}
	var mu sync.Mutex
	agents := map[string]map[string]any{}
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		workspace := req.Header.Get("X-Tenant-Id")
		id := strings.TrimPrefix(req.URL.Path, "/api/v1/agents/")
		key := workspace + "/" + id
		switch req.Method {
		case http.MethodPost:
			if req.URL.Path != "/api/v1/agents" {
				t.Errorf("unexpected path %s", req.URL.Path)
			}
			var body struct {
				ID string `json:"id"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			key = workspace + "/" + body.ID
			agents[key] = map[string]any{"id": body.ID, "name": body.ID, "source_type": "EXTERNAL", "agent_type": "CUSTOM", "created_at": "2026-01-01T00:00:00Z", "environments": []map[string]string{{"id": key + "-env", "agent_id": body.ID, "environment": "PRODUCTION", "tracer_session_id": key + "-project", "tracer_session_name": body.ID}}}
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			if agents[key] == nil {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"detail":"Agent not found"}`))
				return
			}
		case http.MethodDelete:
			if agents[key] == nil {
				t.Errorf("delete targeted missing agent %s", key)
			}
			delete(agents, key)
			deleted = append(deleted, key)
			w.WriteHeader(http.StatusNoContent)
			return
		default:
			t.Errorf("unexpected method %s", req.Method)
			w.WriteHeader(405)
			return
		}
		if err := json.NewEncoder(w).Encode(agents[key]); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	config := func(id, workspace string) string {
		return fmt.Sprintf(`
provider "langsmith" {
  api_url = %q
  api_key = "offline-test-key"
  workspace_id = "workspace-a"
}
resource "langsmith_agent" "test" {
  agent_id = %q
  workspace_id = %q
}`, server.URL, id, workspace)
	}
	replace := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("langsmith_agent.test", plancheck.ResourceActionDestroyBeforeCreate)}}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"langsmith": providerserver.NewProtocol6WithError(New("test")())},
		Steps: []resource.TestStep{
			{Config: config("first-agent", "workspace-a")},
			{Config: config("second-agent", "workspace-a"), ConfigPlanChecks: replace},
			{Config: config("second-agent", "workspace-b"), ConfigPlanChecks: replace,
				Check: resource.TestCheckResourceAttr("langsmith_agent.test", "environments.PRODUCTION.tracer_session_id", "workspace-b/second-agent-project")},
		},
	})
	mu.Lock()
	defer mu.Unlock()
	if len(agents) != 0 || strings.Join(deleted, ",") != "workspace-a/first-agent,workspace-a/second-agent,workspace-b/second-agent" {
		t.Fatalf("replacement/destroy left agents %v; deleted %v", agents, deleted)
	}
}

type agentContractBackend struct {
	t                  *testing.T
	mu                 sync.Mutex
	workspace          string
	agent              map[string]any
	readProductionOnly bool
}

func (b *agentContractBackend) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if req.Header.Get("X-Tenant-Id") != b.workspace {
		b.t.Errorf("workspace = %q, want %q", req.Header.Get("X-Tenant-Id"), b.workspace)
	}
	if req.Method == http.MethodPost && req.URL.Path == "/api/v1/agents" {
		if b.agent != nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			b.t.Error(err)
			w.WriteHeader(400)
			return
		}
		if body["id"] != "test-agent" {
			b.t.Errorf("unexpected ID: %v", body["id"])
		}
		b.agent = map[string]any{"id": "test-agent", "name": "test-agent", "source_type": "EXTERNAL", "agent_type": "CUSTOM", "owner_id": nil, "created_at": "2026-01-01T00:00:00Z"}
		for key, value := range body {
			b.agent[key] = value
		}
		var environments []map[string]string
		for _, name := range []string{"STAGING", "PRODUCTION", "LOCAL", "DEVELOPMENT"} {
			environments = append(environments, map[string]string{"id": "env-" + name, "agent_id": "test-agent", "environment": name, "tracer_session_id": "project-" + name, "tracer_session_name": "test-agent-" + name})
		}
		b.agent["environments"] = environments
		w.WriteHeader(http.StatusCreated)
	} else if req.URL.Path != "/api/v1/agents/test-agent" {
		b.t.Errorf("unexpected API path %s", req.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		return
	} else if req.Method == http.MethodDelete {
		b.agent = nil
		w.WriteHeader(http.StatusNoContent)
		return
	} else if b.agent == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Agent not found"}`))
		return
	} else if req.Method == http.MethodPatch {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			b.t.Error(err)
			w.WriteHeader(400)
			return
		}
		if source, ok := body["source_type"]; ok && source != b.agent["source_type"] && b.agent["source_type"] != "EXTERNAL" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"detail":"source transition forbidden"}`))
			return
		}
		for key, value := range body {
			if key != "name" && key != "source_type" && key != "agent_type" {
				b.t.Errorf("unexpected update field %s", key)
			}
			if value == b.agent[key] {
				b.t.Errorf("unnecessarily updating unchanged field %s", key)
			}
			b.agent[key] = value
		}
	} else if req.Method != http.MethodGet {
		b.t.Errorf("unexpected method %s", req.Method)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	response := b.agent
	if b.readProductionOnly && req.Method == http.MethodGet {
		response = maps.Clone(b.agent)
		environments, ok := b.agent["environments"].([]map[string]string)
		if !ok {
			b.t.Error("invalid environment fixture")
			w.WriteHeader(500)
			return
		}
		var visible []map[string]string
		for _, env := range environments {
			if env["environment"] == "PRODUCTION" {
				visible = append(visible, env)
			}
		}
		response["environments"] = visible
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		b.t.Error(err)
	}
}
