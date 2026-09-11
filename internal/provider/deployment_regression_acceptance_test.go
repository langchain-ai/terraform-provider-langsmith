package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccDeploymentOfflineGithubImportHasEmptyPlan(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprintf("managed_secrets=%t", managed), func(t *testing.T) {
			testDeploymentGithubImport(t, managed)
		})
	}
}

func testDeploymentGithubImport(t *testing.T, managed bool) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the offline Terraform acceptance test")
	}
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.Method == http.MethodDelete {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if deleted {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if req.Method != http.MethodGet {
			t.Errorf("import and plan made an unexpected %s request", req.Method)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if strings.Contains(req.URL.Path, "/revisions/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": offlineRevisionOne, "status": "DEPLOYED"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": offlineDeploymentID, "name": "github-agent", "source": "github",
			"source_config": map[string]any{
				"integration_id": "github-integration", "repo_url": "https://github.com/example/agent",
				"repo_branch": "main", "deployment_type": "dev_zero", "build_on_push": true,
				"custom_url": "https://agent.example.com",
			},
			"source_revision_config": map[string]any{
				"repo_ref": "resolved-commit", "langgraph_config_path": "langgraph.json",
				"image_uri": "registry.example.com/agent:built-image",
			},
			"secrets": []any{map[string]any{"name": "API_TOKEN", "value": offlineSecretOne}},
			"status":  "READY", "latest_revision_id": offlineRevisionOne, "active_revision_id": offlineRevisionOne,
		})
	}))
	defer server.Close()
	config := fmt.Sprintf(`
provider "langsmith" {
  control_plane_url = %q
  api_key = "offline-test-key"
}
resource "langsmith_deployment" "test" {
  name = "github-agent"
  source = "github"
  source_config = {
    integration_id = "github-integration"
    repo_url = "https://github.com/example/agent"
    deployment_type = "dev_zero"
    build_on_push = true
  }
  source_revision_config = {
    repo_ref = "main"
    langgraph_config_path = "langgraph.json"
  }
}`, server.URL)
	if managed {
		config = strings.Replace(config, `source = "github"`, `source = "github"
  secrets = { API_TOKEN = "`+offlineSecretOne+`" }`, 1)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: offlineDeploymentFactories(),
		Steps: []resource.TestStep{
			{Config: config, ResourceName: "langsmith_deployment.test", ImportState: true, ImportStateId: offlineDeploymentID, ImportStatePersist: true},
			{Config: config, PlanOnly: true, ExpectNonEmptyPlan: false, Check: deploymentStateExcludesSecrets},
		},
	})
}

func TestAccDeploymentOfflineRejectedRevisionRetry(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the offline Terraform acceptance test")
	}
	backend := newDeploymentContractBackend(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost && req.URL.Path == "/api-host/v2/deployments/"+offlineDeploymentID+"/revisions" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"detail":"revision rejected"}`))
			return
		}
		backend.ServeHTTP(w, req)
	}))
	defer server.Close()
	config1 := deploymentAcceptanceConfig(server.URL, "", "registry.example.com/agent:v1", "1", offlineSecretOne)
	config2 := deploymentAcceptanceConfig(server.URL, "", "registry.example.com/agent:v2", "2", offlineSecretTwo)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: offlineDeploymentFactories(),
		Steps: []resource.TestStep{
			{Config: config1},
			{Config: config2, ExpectError: regexp.MustCompile("Unable to Update LangSmith Deployment")},
			{Config: config2, PlanOnly: true, ExpectNonEmptyPlan: true},
		},
	})
}

func TestAccDeploymentOfflineResourceSpecDrift(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the offline Terraform acceptance test")
	}
	backend := newDeploymentContractBackend(t)
	backend.revisionSecret = offlineSecretOne
	server := httptest.NewServer(backend)
	defer server.Close()
	unmanaged := deploymentAcceptanceConfig(server.URL, "", "registry.example.com/agent:v1", "1", offlineSecretOne)
	managed := strings.Replace(unmanaged, "resource_spec = {}", `resource_spec = { cpu = 2, labels = { team = "agents" } }`, 1)
	drift := func() {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		backend.resourceSpec["cpu"] = float64(4)
		backend.resourceSpec["labels"] = map[string]any{"team": "changed"}
		backend.resourceSpec["memory_mb"] = float64(4096)
		backend.resourceSpec["queue_cpu"] = float64(4)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: offlineDeploymentFactories(),
		Steps: []resource.TestStep{
			{Config: managed, Check: resource.TestCheckNoResourceAttr("langsmith_deployment.test", "source_config.resource_spec.memory_mb")},
			{PreConfig: drift, Config: managed, PlanOnly: true, ExpectNonEmptyPlan: true},
			{Config: managed, Check: resource.ComposeAggregateTestCheckFunc(
				backend.expectRevisionPosts(1),
				resource.TestCheckResourceAttr("langsmith_deployment.test", "source_config.resource_spec.cpu", "2"),
				resource.TestCheckResourceAttr("langsmith_deployment.test", "source_config.resource_spec.labels.team", "agents"),
				resource.TestCheckNoResourceAttr("langsmith_deployment.test", "source_config.resource_spec.memory_mb"),
				func(*terraform.State) error {
					backend.mu.Lock()
					defer backend.mu.Unlock()
					if backend.resourceSpec["cpu"] != float64(2) || backend.resourceSpec["memory_mb"] != float64(4096) || backend.resourceSpec["queue_cpu"] != float64(4) {
						return fmt.Errorf("drift correction did not restore CPU while preserving unmanaged fields: %#v", backend.resourceSpec)
					}
					return nil
				},
			)},
			{Config: managed, PlanOnly: true, ExpectNonEmptyPlan: false},
			{Config: unmanaged, Check: backend.expectRevisionPosts(2)},
			{PreConfig: drift, Config: unmanaged, PlanOnly: true, ExpectNonEmptyPlan: false},
		},
	})
}
