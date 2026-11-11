package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	frameworkprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	frameworkresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccDeploymentOfflineAgentEnvironments(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run offline Terraform acceptance")
	}
	registry := &agentContractBackend{t: t, workspace: "offline-workspace"}
	staging, production := newDeploymentContractBackend(t), newDeploymentContractBackend(t)
	production.deploymentID = "44444444-4444-4444-4444-444444444444"
	deployments := map[string]*deploymentContractBackend{"staging": staging, "production": production}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasPrefix(req.URL.Path, "/api/v1/agents") {
			if req.Method == http.MethodDelete && (staging.created || production.created) {
				t.Error("agent deleted before its deployments")
			}
			registry.ServeHTTP(w, req)
			return
		}
		var backend *deploymentContractBackend
		switch {
		case req.URL.Path == "/v2/deployments" && req.Method == http.MethodPost:
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var payload struct {
				Agent deploymentAgentAPI `json:"agent"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if registry.agent == nil || registry.agent["id"] != payload.Agent.AgentID || registry.agent["source_type"] != "LSD" {
				t.Error("deployment created before its LSD agent")
				w.WriteHeader(http.StatusConflict)
				return
			}
			req.Body = io.NopCloser(strings.NewReader(string(body)))
			backend = deployments[payload.Agent.Environment]
		case req.URL.Path == "/v2/deployments":
			backend = deployments[req.URL.Query().Get("agent_environment")]
		default:
			for _, candidate := range deployments {
				if req.URL.Path == "/v2/deployments/"+candidate.deploymentID || strings.HasPrefix(req.URL.Path, "/v2/deployments/"+candidate.deploymentID+"/") {
					backend = candidate
				}
			}
		}
		if backend == nil {
			t.Errorf("unexpected request: %s %s", req.Method, req.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		backend.ServeHTTP(w, req)
	}))
	defer server.Close()
	deployment := func(environment, image, secret string) string {
		return fmt.Sprintf(`
resource "langsmith_deployment" %q {
  agent = {
    agent_id = langsmith_agent.test.id
    environment = %q
  }
  source = "external_docker"
  source_config = { resource_spec = {} }
  source_revision_config = { image_uri = %q }
  secrets = { API_TOKEN = %q }
}
`, environment, environment, image, secret)
	}
	config := func(includeStaging bool, image, secret string) string {
		hcl := fmt.Sprintf(`
provider "langsmith" {
  api_url = %q
  api_key = "offline-test-key"
  workspace_id = "offline-workspace"
}
resource "langsmith_agent" "test" {
  agent_id = "test-agent"
  source_type = "LSD"
}
`, server.URL+"/api/v1") + deployment("production", "registry.example.com/agent:v1", offlineSecretOne)
		if includeStaging {
			hcl += deployment("staging", image, secret)
		}
		return hcl
	}
	check := func(includeStaging bool, stagingRevisions int) resource.TestCheckFunc {
		return resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr("langsmith_agent.test", "environments.%", "4"),
			resource.TestCheckResourceAttr("langsmith_agent.test", "environments.STAGING.tracer_session_id", "project-STAGING"),
			resource.TestCheckResourceAttr("langsmith_agent.test", "environments.PRODUCTION.tracer_session_id", "project-PRODUCTION"),
			resource.TestCheckResourceAttr("langsmith_deployment.production", "id", production.deploymentID),
			resource.TestCheckResourceAttr("langsmith_deployment.production", "latest_revision_id", offlineRevisionOne),
			deploymentStateExcludesSecrets,
			func(*terraform.State) error {
				mu.Lock()
				defer mu.Unlock()
				if production.createPosts != 1 || production.revisionPosts != 0 || production.patches != 0 || production.deleted || !production.created {
					return fmt.Errorf("production was recreated, changed, or deleted")
				}
				if staging.createPosts != 1 || staging.revisionPosts != stagingRevisions || staging.created != includeStaging || staging.deleted == includeStaging {
					return fmt.Errorf("unexpected staging lifecycle: creates=%d revisions=%d created=%t deleted=%t", staging.createPosts, staging.revisionPosts, staging.created, staging.deleted)
				}
				return nil
			},
		)
	}
	initial := config(true, "registry.example.com/agent:v1", offlineSecretOne)
	updated := config(true, "registry.example.com/agent:v2", offlineSecretTwo)
	productionOnly := config(false, "", "")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: offlineDeploymentFactories(),
		CheckDestroy: func(*terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if registry.agent != nil || staging.created || production.created || !staging.deleted || !production.deleted {
				return fmt.Errorf("destroy left an agent or deployment behind")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: initial, Check: check(true, 0)},
			{Config: initial, PlanOnly: true},
			{Config: updated, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("langsmith_agent.test", plancheck.ResourceActionNoop),
				plancheck.ExpectResourceAction("langsmith_deployment.production", plancheck.ResourceActionNoop),
				plancheck.ExpectResourceAction("langsmith_deployment.staging", plancheck.ResourceActionUpdate),
			}}, Check: resource.ComposeAggregateTestCheckFunc(check(true, 1),
				resource.TestCheckResourceAttr("langsmith_deployment.staging", "latest_revision_id", offlineRevisionTwo))},
			{Config: updated, PlanOnly: true},
			{Config: productionOnly, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("langsmith_agent.test", plancheck.ResourceActionNoop),
				plancheck.ExpectResourceAction("langsmith_deployment.production", plancheck.ResourceActionNoop),
				plancheck.ExpectResourceAction("langsmith_deployment.staging", plancheck.ResourceActionDestroy),
			}}, Check: check(false, 1)},
			{Config: productionOnly, PlanOnly: true},
		},
	})
}

func TestAccDeploymentOfflineAgentTarget(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run offline Terraform acceptance")
	}
	backend := newDeploymentContractBackend(t)
	backend.occupied = true
	backend.deletePolls = 2
	backend.revisionSecret = offlineSecretOne
	server := httptest.NewServer(backend)
	defer server.Close()
	hcl := func(target, displayName, image string) string {
		config := deploymentAcceptanceConfig(server.URL, displayName, image, "1", offlineSecretOne)
		return strings.Replace(config, `name   = "offline-agent"`, target, 1)
	}
	target := func(environment string) string {
		return fmt.Sprintf(`agent = { agent_id = "helper", environment = %q }`, environment)
	}
	check := func(environment string) resource.TestCheckFunc {
		return resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr("langsmith_deployment.test", "name", "generated-helper-"+environment),
			resource.TestCheckResourceAttr("langsmith_deployment.test", "agent.agent_id", "helper"),
			resource.TestCheckResourceAttr("langsmith_deployment.test", "agent.environment", environment),
			deploymentStateExcludesSecrets,
		)
	}
	replacement := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
		plancheck.ExpectUnknownValue("langsmith_deployment.test", tfjsonpath.New("name")),
	}}
	initial := hcl(target("staging"), "", "registry.example.com/agent:v1")
	sameTarget := strings.Replace(hcl(target("production"), "", "registry.example.com/agent:v1"),
		"resource_spec = {}", `listener_id = "listener"`+"\nresource_spec = {}", 1)
	moved := strings.Split(sameTarget, "\ndata \"langsmith_deployment_revision\"")[0]
	moved = strings.Replace(moved, `source = "external_docker"`, "workspace_id = \"other-workspace\"\nsource = \"external_docker\"", 1)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: offlineDeploymentFactories(),
		Steps: []resource.TestStep{
			{Config: initial, ExpectError: regexp.MustCompile(`(?s)retry\s+terraform apply.*already exists`)},
			{Config: initial, PreConfig: func() {
				backend.mu.Lock()
				defer backend.mu.Unlock()
				if backend.deleted || backend.createPosts != 0 {
					t.Fatal("occupied deployment was modified or adopted")
				}
				backend.occupied = false
			}, Check: check("staging")},
			{Config: initial, PlanOnly: true},
			{ResourceName: "langsmith_deployment.test", ImportState: true, ImportStateId: offlineDeploymentID + "/helper/staging",
				ImportStateVerify: true, ImportStateVerifyIgnore: []string{"secrets_version", "source_config.resource_spec"}},
			{Config: hcl(target("staging"), `display_name = "Updated"`, "registry.example.com/agent:v2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectKnownValue("langsmith_deployment.test", tfjsonpath.New("name"), knownvalue.StringExact("generated-helper-staging")),
				}}, Check: check("staging")},
			{Config: hcl(target("staging"), `display_name = "Updated"`, "registry.example.com/agent:v2"), PlanOnly: true},
			// A different target can be created while the old one is cleaning up.
			{Config: hcl(target("production"), "", "registry.example.com/agent:v1"), ConfigPlanChecks: replacement, Check: check("production")},
			{Config: hcl(target("production"), "", "registry.example.com/agent:v1"), PlanOnly: true},
			// An unrelated immutable input also replaces within the same target.
			{Config: sameTarget, ConfigPlanChecks: replacement, Check: check("production")},
			{Config: moved, ConfigPlanChecks: replacement, Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("langsmith_deployment.test", "name", "generated-helper-production-other-workspace"),
				resource.TestCheckResourceAttr("langsmith_deployment.test", "workspace_id", "other-workspace"),
			)},
			{Config: moved, PlanOnly: true},
			{ResourceName: "langsmith_deployment.test", ImportState: true, ImportStateId: "other-workspace/" + offlineDeploymentID + "/helper/production",
				ImportStateVerify: true, ImportStateVerifyIgnore: []string{"secrets_version", "source_config.resource_spec"}},
		},
	})
}

func TestAccDeploymentOfflineAgentTargetUnknown(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run offline Terraform acceptance")
	}
	for _, adopt := range []bool{false, true} {
		t.Run(fmt.Sprintf("adopt=%t", adopt), func(t *testing.T) {
			backend := newDeploymentContractBackend(t)
			server := httptest.NewServer(backend)
			defer server.Close()
			named := deploymentAcceptanceConfig(server.URL, "", "registry.example.com/agent:v1", "1", offlineSecretOne)
			hcl := strings.Replace(named, `name   = "offline-agent"`, `agent = terraform_data.target.output`, 1) + `
resource "terraform_data" "target" {
  input = { agent_id = "offline-agent", environment = "production" }
}`
			var steps []resource.TestStep
			if adopt {
				mismatched := strings.Replace(hcl, `environment = "production"`, `environment = "staging"`, 1)
				steps = append(steps,
					resource.TestStep{Config: named},
					resource.TestStep{Config: mismatched,
						ExpectError: regexp.MustCompile("inconsistent final plan|does not belong to the specified agent environment")},
					resource.TestStep{Config: mismatched, PlanOnly: true, ExpectNonEmptyPlan: true,
						ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{
							plancheck.ExpectResourceAction("langsmith_deployment.test", plancheck.ResourceActionDestroyBeforeCreate),
						}}},
				)
			}
			steps = append(steps,
				resource.TestStep{Config: hcl, Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("langsmith_deployment.test", "agent.environment", "production"),
					func(*terraform.State) error {
						backend.mu.Lock()
						defer backend.mu.Unlock()
						if backend.createPosts != 1 || backend.patches != 0 || backend.revisionPosts != 0 || backend.deleted {
							return fmt.Errorf("unknown target changed the deployment: creates=%d patches=%d revisions=%d deleted=%t", backend.createPosts, backend.patches, backend.revisionPosts, backend.deleted)
						}
						return nil
					},
				)},
				resource.TestStep{Config: hcl, PlanOnly: true},
			)
			resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: offlineDeploymentFactories(), Steps: steps})
		})
	}
}

func TestAccDeploymentOfflineSwitchTargetMode(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run offline Terraform acceptance")
	}
	backend := newDeploymentContractBackend(t)
	backend.deletePolls = 2
	server := httptest.NewServer(backend)
	defer server.Close()
	hcl := deploymentAcceptanceConfig(server.URL, "", "registry.example.com/agent:v1", "1", offlineSecretOne)
	targeted := strings.Replace(hcl, `name   = "offline-agent"`, `agent = { agent_id = "offline-agent", environment = "production" }`, 1)
	named := strings.Replace(hcl, `name   = "offline-agent"`, `name = "generated-offline-agent-production"`, 1)
	replacement := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
		plancheck.ExpectResourceAction("langsmith_deployment.test", plancheck.ResourceActionDestroyBeforeCreate),
	}}
	replacementPlan := resource.ConfigPlanChecks{PostApplyPostRefresh: replacement.PreApply}
	moved := strings.Replace(targeted, `agent_id = "offline-agent"`, `agent_id = "other-agent"`, 1)
	moved = strings.Replace(moved, `source = "external_docker"`, "workspace_id = \"other-workspace\"\nsource = \"external_docker\"", 1)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: offlineDeploymentFactories(),
		Steps: []resource.TestStep{
			{Config: hcl, Check: resource.TestCheckNoResourceAttr("langsmith_deployment.test", "agent.agent_id")},
			{Config: strings.Replace(targeted, `environment = "production"`, `environment = "staging"`, 1), PlanOnly: true,
				ExpectNonEmptyPlan: true, ConfigPlanChecks: replacementPlan},
			{Config: strings.Replace(targeted, `agent_id = "offline-agent"`, `agent_id = "other-agent"`, 1), PlanOnly: true,
				ExpectNonEmptyPlan: true, ConfigPlanChecks: replacementPlan},
			{Config: strings.Replace(targeted, `source = "external_docker"`, "workspace_id = \"other-workspace\"\nsource = \"external_docker\"", 1),
				PlanOnly: true, ExpectNonEmptyPlan: true, ConfigPlanChecks: replacementPlan},
			{Config: moved, PlanOnly: true, ExpectNonEmptyPlan: true, ConfigPlanChecks: replacementPlan},
			{Config: targeted, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("langsmith_deployment.test", plancheck.ResourceActionUpdate),
			}}, Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("langsmith_deployment.test", "agent.agent_id", "offline-agent"),
				resource.TestCheckResourceAttr("langsmith_deployment.test", "id", offlineDeploymentID),
				resource.TestCheckResourceAttr("langsmith_deployment.test", "name", "offline-agent"),
				func(*terraform.State) error {
					backend.mu.Lock()
					defer backend.mu.Unlock()
					if backend.createPosts != 1 || backend.revisionPosts != 0 || backend.patches != 0 || backend.deleted {
						return fmt.Errorf("migration changed the deployment: creates=%d revisions=%d patches=%d deleted=%t", backend.createPosts, backend.revisionPosts, backend.patches, backend.deleted)
					}
					return nil
				},
			)},
			{Config: targeted, PlanOnly: true},
			{Config: named, ConfigPlanChecks: replacement, Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("langsmith_deployment.test", "name", "generated-offline-agent-production"),
				resource.TestCheckNoResourceAttr("langsmith_deployment.test", "agent.agent_id"),
			)},
			{Config: named, PlanOnly: true},
		},
	})
}

func TestAccDeploymentOfflineAgentTargetCleanupRetry(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run offline Terraform acceptance")
	}
	backend := newDeploymentContractBackend(t)
	backend.deletePolls = 2
	server := httptest.NewServer(backend)
	defer server.Close()
	initial := strings.Replace(deploymentAcceptanceConfig(server.URL, "", "registry.example.com/agent:v1", "1", offlineSecretOne),
		`name   = "offline-agent"`, `agent = { agent_id = "helper", environment = "production" }`, 1)
	replacement := strings.Replace(initial, "resource_spec = {}", "listener_id = \"listener\"\nresource_spec = {}", 1)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(deploymentCleanupTestProvider{New("test")()}),
		},
		Steps: []resource.TestStep{
			{Config: initial},
			{Config: replacement, PreConfig: func() {
				backend.mu.Lock()
				defer backend.mu.Unlock()
				backend.cleanupBlocked = true
			}, ExpectError: regexp.MustCompile(`still being cleaned up; retry\s+terraform apply`)},
			{Config: replacement, PreConfig: func() {
				backend.mu.Lock()
				defer backend.mu.Unlock()
				if !backend.deleted || backend.created || backend.createPosts != 1 {
					t.Fatal("cleanup timeout must leave the old deployment deleted and the replacement uncreated")
				}
				backend.cleanupBlocked = false
			}, Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("langsmith_deployment.test", "agent.agent_id", "helper"),
				resource.TestCheckResourceAttr("langsmith_deployment.test", "name", "generated-helper-production"),
			)},
			{Config: replacement, PlanOnly: true},
		},
	})
	if backend.createPosts != 2 {
		t.Fatalf("created %d deployments, want the original and one replacement", backend.createPosts)
	}
}

type deploymentCleanupTestProvider struct {
	frameworkprovider.Provider
}

func (deploymentCleanupTestProvider) Resources(context.Context) []func() frameworkresource.Resource {
	return []func() frameworkresource.Resource{
		func() frameworkresource.Resource {
			return &DeploymentResource{pollInterval: time.Millisecond, waitTimeout: time.Second}
		},
	}
}
