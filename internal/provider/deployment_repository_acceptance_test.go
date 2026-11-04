package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// repositoryMoveBackend is a GitHub deployment as the v2 API stores it. A PATCH that
// changes repo_url without integration_id resolves the integration from the new
// repository, the way the API picks the workspace integration that reaches it.
type repositoryMoveBackend struct {
	t             *testing.T
	mu            sync.Mutex
	integration   string
	repo          string
	ref           string
	configPath    string
	revision      int
	revisionPosts int
	lastPatch     map[string]any
	created       bool
}

func (b *repositoryMoveBackend) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(req.Body)
	var payload map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			b.t.Errorf("%s %s: %v", req.Method, req.URL.Path, err)
		}
	}
	path := strings.TrimPrefix(req.URL.Path, "/")
	deployment := "v2/deployments/" + offlineDeploymentID
	switch {
	case path == "v2/deployments" && req.Method == http.MethodPost:
		sourceConfig, _ := payload["source_config"].(map[string]any)
		revision, _ := payload["source_revision_config"].(map[string]any)
		b.integration, _ = sourceConfig["integration_id"].(string)
		b.repo, _ = sourceConfig["repo_url"].(string)
		b.ref, _ = revision["repo_ref"].(string)
		b.configPath, _ = revision["langgraph_config_path"].(string)
		b.revision, b.created = 1, true
		w.WriteHeader(http.StatusCreated)
		b.write(w)
	case path == deployment && req.Method == http.MethodPatch:
		b.lastPatch = payload
		sourceConfig, _ := payload["source_config"].(map[string]any)
		if repo, ok := sourceConfig["repo_url"].(string); ok {
			b.repo = repo
			b.integration = "integration-for-" + repo[strings.LastIndex(repo, "/")+1:]
		}
		if integration, ok := sourceConfig["integration_id"].(string); ok {
			b.integration = integration
		}
		if revision, ok := payload["source_revision_config"].(map[string]any); ok {
			b.ref, _ = revision["repo_ref"].(string)
			b.configPath, _ = revision["langgraph_config_path"].(string)
			b.revision++
		}
		b.write(w)
	case path == deployment && req.Method == http.MethodGet:
		if !b.created {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"not found"}`))
			return
		}
		b.write(w)
	case path == deployment+"/revisions" && req.Method == http.MethodPost:
		b.revisionPosts++
		b.revision++
		_, _ = fmt.Fprintf(w, `{"id":"%s","status":"CREATING"}`, b.revisionID())
	case strings.HasPrefix(path, deployment+"/revisions/") && req.Method == http.MethodGet:
		_, _ = fmt.Fprintf(w, `{"id":"%s","status":"DEPLOYED"}`, strings.TrimPrefix(path, deployment+"/revisions/"))
	case path == deployment && req.Method == http.MethodDelete:
		b.created = false
		w.WriteHeader(http.StatusNoContent)
	default:
		b.t.Errorf("unexpected %s %s", req.Method, req.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (b *repositoryMoveBackend) revisionID() string {
	return fmt.Sprintf("33333333-3333-3333-3333-%012d", b.revision)
}

func (b *repositoryMoveBackend) write(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": offlineDeploymentID, "name": "moved-agent", "source": "github", "status": "READY",
		"tenant_id":     "offline-workspace",
		"source_config": map[string]any{"integration_id": b.integration, "repo_url": b.repo, "deployment_type": "prod", "build_on_push": true},
		"source_revision_config": map[string]any{
			"repo_ref": b.ref, "langgraph_config_path": b.configPath,
		},
		"latest_revision_id": b.revisionID(), "active_revision_id": b.revisionID(),
	})
}

func (b *repositoryMoveBackend) check(f func(*repositoryMoveBackend) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		b.mu.Lock()
		defer b.mu.Unlock()
		return f(b)
	}
}

func repositoryMoveConfig(serverURL, integration, repo, ref, configPath string) string {
	integrationLine := ""
	if integration != "" {
		integrationLine = fmt.Sprintf("integration_id = %q", integration)
	}
	return fmt.Sprintf(`
provider "langsmith" {
  api_url      = %q
  api_key      = "offline-test-key"
  workspace_id = "offline-workspace"
}

resource "langsmith_deployment" "test" {
  name   = "moved-agent"
  source = "github"

  source_config = {
    %s
    repo_url        = %q
    deployment_type = "prod"
    build_on_push   = true
  }

  source_revision_config = {
    repo_ref              = %q
    langgraph_config_path = %q
  }
}
`, serverURL, integrationLine, repo, ref, configPath)
}

// repositoryMoveDeferredConfig names the repository through another resource, so
// repo_url is unknown until apply.
func repositoryMoveDeferredConfig(serverURL, repo string) string {
	return strings.Replace(
		repositoryMoveConfig(serverURL, "", "REPO", "agent-prod", "agent/langgraph.json"),
		`repo_url        = "REPO"`,
		"repo_url        = terraform_data.repo.output",
		1,
	) + fmt.Sprintf(`
resource "terraform_data" "repo" {
  input = %q
}
`, repo)
}

func TestAccDeploymentRepositoryMove(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the offline Terraform acceptance test")
	}
	backend := &repositoryMoveBackend{t: t}
	server := httptest.NewServer(backend)
	defer server.Close()
	address := "langsmith_deployment.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: offlineDeploymentFactories(),
		Steps: []resource.TestStep{
			{
				Config: repositoryMoveConfig(server.URL, "integration-a", "https://github.com/org-a/agent", "prod", "langgraph.json"),
				Check:  resource.TestCheckResourceAttr(address, "source_config.integration_id", "integration-a"),
			},
			{
				// The integration is no longer configured, and the new repository is in another
				// installation: the API picks its integration, and the apply adopts it.
				Config: repositoryMoveConfig(server.URL, "", "https://github.com/org-b/monorepo", "agent-prod", "agent/langgraph.json"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "id", offlineDeploymentID),
					resource.TestCheckResourceAttr(address, "source_config.repo_url", "https://github.com/org-b/monorepo"),
					resource.TestCheckResourceAttr(address, "source_config.integration_id", "integration-for-monorepo"),
					resource.TestCheckResourceAttr(address, "source_revision_config.repo_ref", "agent-prod"),
					backend.check(func(b *repositoryMoveBackend) error {
						sourceConfig, _ := b.lastPatch["source_config"].(map[string]any)
						if _, sent := sourceConfig["integration_id"]; sent {
							return fmt.Errorf("an unconfigured integration was sent: %v", sourceConfig)
						}
						if b.revisionPosts != 0 {
							return fmt.Errorf("revision POSTs = %d; the revision belongs on the PATCH", b.revisionPosts)
						}
						return nil
					}),
				),
			},
			{
				// A configured integration goes with the move, so the API checks it can reach
				// the new repository instead of choosing another one.
				Config: repositoryMoveConfig(server.URL, "integration-c", "https://github.com/org-c/agent", "agent-prod", "agent/langgraph.json"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "id", offlineDeploymentID),
					resource.TestCheckResourceAttr(address, "source_config.integration_id", "integration-c"),
					backend.check(func(b *repositoryMoveBackend) error {
						sourceConfig, _ := b.lastPatch["source_config"].(map[string]any)
						if sourceConfig["integration_id"] != "integration-c" {
							return fmt.Errorf("configured integration not sent: %v", sourceConfig)
						}
						return nil
					}),
				),
			},
			{
				// The repository is only known at apply time; an omitted integration must not
				// carry over from state, since the new repository may be in another installation.
				Config: repositoryMoveDeferredConfig(server.URL, "https://github.com/org-d/service"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "source_config.repo_url", "https://github.com/org-d/service"),
					resource.TestCheckResourceAttr(address, "source_config.integration_id", "integration-for-service"),
					backend.check(func(b *repositoryMoveBackend) error {
						sourceConfig, _ := b.lastPatch["source_config"].(map[string]any)
						if _, sent := sourceConfig["integration_id"]; sent {
							return fmt.Errorf("the state's integration was sent with a deferred move: %v", sourceConfig)
						}
						return nil
					}),
				),
			},
		},
	})
}
