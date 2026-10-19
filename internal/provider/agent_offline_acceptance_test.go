package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccAgentOfflineLifecycle(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the offline Terraform acceptance test")
	}
	if os.Getenv("TF_ACC_TERRAFORM_PATH") == "" {
		if _, err := exec.LookPath("terraform"); err != nil {
			t.Skip("Terraform is not installed")
		}
	}
	for _, override := range []bool{false, true} {
		t.Run(fmt.Sprintf("workspace_override=%v", override), func(t *testing.T) {
			workspace, selector, importID := "provider-workspace", "", "offline-agent"
			if override {
				workspace, selector, importID = "override-workspace", `workspace_id = "override-workspace"`, "override-workspace/offline-agent"
			}
			backend := &agentOfflineBackend{t: t, workspace: workspace}
			server := httptest.NewServer(backend)
			defer server.Close()
			config := func(update bool) string { return agentOfflineConfig(server.URL, selector, update) }
			checks := func(name, agentType, projectName string) resource.TestCheckFunc {
				return resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("langsmith_agent.test", "id", "offline-agent"),
					resource.TestCheckResourceAttr("langsmith_agent.test", "name", name),
					resource.TestCheckResourceAttr("langsmith_agent.test", "agent_type", agentType),
					resource.TestCheckResourceAttr("langsmith_agent.test", "source_type", "EXTERNAL"),
					resource.TestCheckResourceAttr("langsmith_agent.test", "environments.%", "4"),
					resource.TestCheckResourceAttr("langsmith_agent.test", "environments.PRODUCTION.id", "env-PRODUCTION"),
					resource.TestCheckResourceAttr("langsmith_agent.test", "environments.PRODUCTION.tracer_session_id", "project-PRODUCTION"),
					resource.TestCheckResourceAttr("langsmith_agent.test", "environments.PRODUCTION.tracer_session_name", projectName),
					resource.TestCheckResourceAttr("data.langsmith_agent.test", "name", name),
					resource.TestCheckResourceAttr("data.langsmith_agent.test", "environments.PRODUCTION.tracer_session_id", "project-PRODUCTION"),
					resource.TestCheckResourceAttr("data.langsmith_agent_environment.test", "agent_id", "offline-agent"),
					resource.TestCheckResourceAttr("data.langsmith_agent_environment.test", "environment", "PRODUCTION"),
					resource.TestCheckResourceAttr("data.langsmith_agent_environment.test", "tracer_session_id", "project-PRODUCTION"),
					resource.TestCheckResourceAttr("data.langsmith_agent_environment.test", "tracer_session_name", projectName),
					resource.TestCheckOutput("production_project", "project-PRODUCTION"),
					func(s *terraform.State) error {
						for _, address := range []string{"langsmith_agent.test", "data.langsmith_agent.test", "data.langsmith_agent_environment.test"} {
							got := s.RootModule().Resources[address].Primary.Attributes["workspace_id"]
							want := ""
							if override {
								want = workspace
							}
							if got != want {
								return fmt.Errorf("%s.workspace_id = %q, want %q", address, got, want)
							}
						}
						return nil
					},
				)
			}
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
					"langsmith": providerserver.NewProtocol6WithError(New("test")()),
				},
				CheckDestroy: func(*terraform.State) error {
					backend.mu.Lock()
					defer backend.mu.Unlock()
					if backend.exists || backend.posts != 1 || backend.patches != 1 || backend.deletes != 1 || backend.environmentGets == 0 {
						return fmt.Errorf("lifecycle: exists=%v POST=%d PATCH=%d DELETE=%d environment GET=%d", backend.exists, backend.posts, backend.patches, backend.deletes, backend.environmentGets)
					}
					return nil
				},
				Steps: []resource.TestStep{
					{Config: config(false), Check: checks("offline-agent", "CUSTOM", "Production")},
					{Config: config(false), PlanOnly: true, ExpectNonEmptyPlan: false},
					{ResourceName: "langsmith_agent.test", ImportState: true, ImportStateId: importID, ImportStateVerify: true},
					{Config: config(true), Check: checks("Renamed offline agent", "CODING", "Production")},
					{Config: config(true), PlanOnly: true, ExpectNonEmptyPlan: false},
					{
						PreConfig: func() { backend.mu.Lock(); defer backend.mu.Unlock(); backend.projectName = "Production refreshed" },
						Config:    config(true), Check: checks("Renamed offline agent", "CODING", "Production refreshed"),
					},
					{ResourceName: "langsmith_agent.test", ImportState: true, ImportStateId: importID, ImportStateVerify: true},
				},
			})
		})
	}
}

func agentOfflineConfig(serverURL, selector string, update bool) string {
	mutable := ""
	if update {
		mutable = "name = \"Renamed offline agent\"\nagent_type = \"CODING\""
	}
	return fmt.Sprintf(`
provider "langsmith" {
 api_url = %q
 api_key = "offline-key"
 workspace_id = "provider-workspace"
}
resource "langsmith_agent" "test" {
 agent_id = "offline-agent"
 source_type = "EXTERNAL"
 %s
 %s
}
data "langsmith_agent" "test" {
 agent_id = langsmith_agent.test.agent_id
 %s
 depends_on = [langsmith_agent.test]
}
data "langsmith_agent_environment" "test" {
 id = langsmith_agent.test.environments["PRODUCTION"].id
 %s
 depends_on = [langsmith_agent.test]
}
output "production_project" {
 value = langsmith_agent.test.environments["PRODUCTION"].tracer_session_id
}
`, serverURL+"/api/v1", selector, mutable, selector, selector)
}

type agentOfflineBackend struct {
	t                                        *testing.T
	mu                                       sync.Mutex
	workspace                                string
	exists                                   bool
	name                                     string
	agentType                                string
	projectName                              string
	posts, patches, deletes, environmentGets int
}

func (b *agentOfflineBackend) agent() agentAPI {
	projectName := b.projectName
	if projectName == "" {
		projectName = "Production"
	}
	environments := make([]agentEnvironmentAPI, 0, 4)
	for _, name := range []string{"LOCAL", "DEVELOPMENT", "STAGING", "PRODUCTION"} {
		tracerName := name
		if name == "PRODUCTION" {
			tracerName = projectName
		}
		environments = append(environments, agentEnvironmentAPI{ID: "env-" + name, AgentID: "offline-agent", Environment: name, TracerSessionID: "project-" + name, TracerSessionName: tracerName})
	}
	return agentAPI{ID: "offline-agent", Name: b.name, SourceType: "EXTERNAL", AgentType: b.agentType, CreatedAt: "2026-01-01T00:00:00Z", Environments: environments}
}

func (b *agentOfflineBackend) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if got := req.Header.Get("X-Tenant-Id"); got != b.workspace {
		b.t.Errorf("%s %s workspace = %q, want %q", req.Method, req.URL.RequestURI(), got, b.workspace)
	}
	switch req.Method + " " + req.URL.RequestURI() {
	case "POST /api/v1/agents":
		assertAgentTestPayload(b.t, req, map[string]any{"id": "offline-agent", "source_type": "EXTERNAL", "agent_type": "CUSTOM"})
		if b.exists {
			b.t.Error("duplicate agent create")
			http.Error(w, "already exists", http.StatusConflict)
			return
		}
		b.exists, b.name, b.agentType = true, "offline-agent", "CUSTOM"
		b.posts++
		writeJSON(b.t, w, b.agent())
	case "GET /api/v1/agents/offline-agent":
		if !b.exists {
			http.NotFound(w, req)
			return
		}
		writeJSON(b.t, w, b.agent())
	case "PATCH /api/v1/agents/offline-agent":
		if !b.exists {
			http.NotFound(w, req)
			return
		}
		assertAgentTestPayload(b.t, req, map[string]any{"name": "Renamed offline agent", "agent_type": "CODING"})
		b.name, b.agentType = "Renamed offline agent", "CODING"
		b.patches++
		writeJSON(b.t, w, b.agent())
	case "DELETE /api/v1/agents/offline-agent":
		b.exists = false
		b.deletes++
		w.WriteHeader(http.StatusNoContent)
	case "GET /api/v1/agent-environments/env-PRODUCTION":
		if !b.exists {
			http.NotFound(w, req)
			return
		}
		b.environmentGets++
		writeJSON(b.t, w, b.agent().Environments[3])
	default:
		b.t.Errorf("unexpected offline request: %s %s", req.Method, req.URL.RequestURI())
		http.NotFound(w, req)
	}
}
