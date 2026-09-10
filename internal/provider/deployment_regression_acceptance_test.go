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
