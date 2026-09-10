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

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccDeploymentOfflineCloudSources(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the offline Terraform acceptance test")
	}
	for _, source := range []string{"github", "internal_docker", "internal_source", "internal_template"} {
		t.Run(source, func(t *testing.T) {
			backend := &cloudDeploymentBackend{}
			server := httptest.NewServer(backend)
			defer server.Close()
			config := func(displayName, ref, pushConfig string) string {
				sourceConfig, revisionConfig := "", ""
				switch source {
				case "github":
					sourceConfig = `integration_id = "github-integration"
    repo_url = "https://github.com/example/agent"`
					revisionConfig = fmt.Sprintf("repo_ref = %q\n    langgraph_config_path = \"langgraph.json\"", ref)
				case "internal_source":
					revisionConfig = `langgraph_config_path = "langgraph.json"`
				case "internal_template":
					sourceConfig = `template_id = "template"`
				}
				return fmt.Sprintf(`
provider "langsmith" {
  control_plane_url = %q
  api_key = "offline-test-key"
}
resource "langsmith_deployment" "test" {
  name = "cloud-agent"
  source = %q
  display_name = %q
  source_config = {
    %s
    %s
  }
  source_revision_config = {
    %s
  }
  environment_variables = { LOG_LEVEL = "info" }
  secrets = { API_TOKEN = "offline-secret-one" }
}`, server.URL, source, displayName, sourceConfig, pushConfig, revisionConfig)
			}
			check := func(name string, revisions int) resource.TestCheckFunc {
				return resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("langsmith_deployment.test", "display_name", name),
					resource.TestCheckResourceAttr("langsmith_deployment.test", "source_config.deployment_type", "prod"),
					resource.TestCheckResourceAttr("langsmith_deployment.test", "environment_variables.LOG_LEVEL", "info"),
					deploymentStateExcludesSecrets,
					func(*terraform.State) error {
						backend.mu.Lock()
						defer backend.mu.Unlock()
						if backend.creates != 1 || backend.revisionPosts != revisions {
							return fmt.Errorf("got %d creates and %d revision POSTs, want 1 and %d", backend.creates, backend.revisionPosts, revisions)
						}
						return nil
					},
				)
			}
			initial := config("Cloud agent", "refs/tags/v1", "")
			initialCheck := resource.ComposeAggregateTestCheckFunc(check("Cloud agent", 0),
				resource.TestCheckResourceAttr("langsmith_deployment.test", "source_config.build_on_push", "false"),
			)
			if source == "internal_docker" || source == "internal_source" {
				initialCheck = resource.ComposeAggregateTestCheckFunc(initialCheck,
					resource.TestCheckNoResourceAttr("langsmith_deployment.test", "latest_revision_id"),
					resource.TestCheckNoResourceAttr("langsmith_deployment.test", "latest_revision_status"),
				)
			}
			steps := []resource.TestStep{
				{Config: initial, Check: initialCheck},
				{Config: initial, PlanOnly: true, ExpectNonEmptyPlan: false},
				{Config: config("Renamed", "refs/tags/v1", ""), Check: check("Renamed", 0)},
			}
			if source == "github" {
				for _, transition := range []struct {
					ref       string
					push      bool
					revisions int
				}{
					{"main", true, 1},
					{"main", false, 1},
					{"main", true, 1},
					{"refs/tags/v2", false, 2},
				} {
					steps = append(steps, resource.TestStep{
						Config: config("Renamed", transition.ref, fmt.Sprintf("build_on_push = %t", transition.push)),
						Check: resource.ComposeAggregateTestCheckFunc(check("Renamed", transition.revisions),
							resource.TestCheckResourceAttr("langsmith_deployment.test", "source_config.build_on_push", fmt.Sprint(transition.push)),
							resource.TestCheckResourceAttr("langsmith_deployment.test", "source_revision_config.repo_ref", transition.ref),
						),
					})
				}
			}
			resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: offlineDeploymentFactories(), Steps: steps})
		})
	}
}

// Cloud creates require explicit tier/push settings. Docker/source creates have
// no revision, and GitHub updates validate the ref and push flag together.
type cloudDeploymentBackend struct {
	mu            sync.Mutex
	deployment    *deploymentAPI
	creates       int
	revisionPosts int
}

func (b *cloudDeploymentBackend) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if req.Method == http.MethodPost && req.URL.Path == "/v2/deployments" {
		var deployment deploymentAPI
		if err := json.NewDecoder(req.Body).Decode(&deployment); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if deployment.SourceConfig["deployment_type"] == nil || (deployment.Source == "github" && deployment.SourceConfig["build_on_push"] == nil) {
			http.Error(w, "Cloud deployment_type and GitHub build_on_push must be supplied", http.StatusBadRequest)
			return
		}
		if deployment.Source != "github" && deployment.SourceConfig["build_on_push"] != nil {
			http.Error(w, "build_on_push is only valid for GitHub", http.StatusBadRequest)
			return
		}
		if deployment.Source == "github" && deployment.SourceConfig["build_on_push"] == true && strings.HasPrefix(fmt.Sprint(deployment.SourceRevisionConfig["repo_ref"]), "refs/tags/") {
			http.Error(w, "build_on_push cannot be enabled for a tag", http.StatusBadRequest)
			return
		}
		deployment.ID, deployment.Status = offlineDeploymentID, "READY"
		deployment.TenantID = "offline-workspace"
		deployment.CreatedAt, deployment.UpdatedAt = "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z"
		if deployment.SourceConfig["build_on_push"] == nil {
			deployment.SourceConfig["build_on_push"] = false
		}
		if deployment.Source == "github" || deployment.Source == "internal_template" {
			revision := offlineRevisionOne
			deployment.LatestRevisionID, deployment.ActiveRevisionID = &revision, &revision
		}
		b.deployment = &deployment
		b.creates++
		w.WriteHeader(http.StatusCreated)
	} else {
		deploymentPath := "/v2/deployments/" + offlineDeploymentID
		validRoute := req.URL.Path == deploymentPath ||
			(req.Method == http.MethodPost && req.URL.Path == deploymentPath+"/revisions") ||
			(req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, deploymentPath+"/revisions/"))
		if !validRoute {
			http.NotFound(w, req)
			return
		}
		if b.deployment == nil {
			http.NotFound(w, req)
			return
		}
		switch {
		case req.Method == http.MethodDelete:
			b.deployment = nil
			w.WriteHeader(http.StatusNoContent)
			return
		case req.Method == http.MethodPatch || req.Method == http.MethodPost:
			var payload deploymentAPI
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			ref := b.deployment.SourceRevisionConfig["repo_ref"]
			if v := payload.SourceRevisionConfig["repo_ref"]; v != nil {
				ref = v
			}
			push := b.deployment.SourceConfig["build_on_push"]
			if v := payload.SourceConfig["build_on_push"]; v != nil {
				push = v
			}
			if b.deployment.Source == "github" && push == true && strings.HasPrefix(fmt.Sprint(ref), "refs/tags/") {
				http.Error(w, "build_on_push cannot be enabled for a tag", http.StatusBadRequest)
				return
			}
			if payload.DisplayName != nil {
				b.deployment.DisplayName = payload.DisplayName
			}
			for key, value := range payload.SourceConfig {
				b.deployment.SourceConfig[key] = value
			}
			for key, value := range payload.SourceRevisionConfig {
				b.deployment.SourceRevisionConfig[key] = value
			}
			if req.Method == http.MethodPost {
				b.revisionPosts++
				revision := fmt.Sprintf("00000000-0000-0000-0000-%012d", b.revisionPosts)
				b.deployment.LatestRevisionID, b.deployment.ActiveRevisionID = &revision, &revision
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(deploymentResourceRevisionAPI{ID: revision, Status: "DEPLOYED"})
				return
			}
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/revisions/"):
			if b.deployment.LatestRevisionID == nil {
				http.NotFound(w, req)
				return
			}
			_ = json.NewEncoder(w).Encode(deploymentResourceRevisionAPI{ID: *b.deployment.LatestRevisionID, Status: "DEPLOYED"})
			return
		case req.Method != http.MethodGet:
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
	}
	response := *b.deployment
	if response.LatestRevisionID == nil {
		response.Secrets = []deploymentSecretAPI{}
		response.SourceRevisionConfig = nil
	}
	_ = json.NewEncoder(w).Encode(response)
}
