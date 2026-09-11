package provider

// The fake API reproduces server defaults, normalization, and validation.

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

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const (
	offlineDeploymentID = "11111111-1111-1111-1111-111111111111"
	offlineRevisionOne  = "22222222-2222-2222-2222-222222222222"
	offlineRevisionTwo  = "33333333-3333-3333-3333-333333333333"
	offlineSecretOne    = "offline-secret-one"
	offlineSecretTwo    = "offline-secret-two"
)

func TestAccDeploymentOffline(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the offline Terraform acceptance test")
	}

	backend := newDeploymentContractBackend(t)
	server := httptest.NewServer(backend)
	t.Cleanup(server.Close)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: offlineDeploymentFactories(),
		Steps: []resource.TestStep{
			{
				Config: deploymentAcceptanceConfig(server.URL, `display_name = "Offline deployment"`, "registry.example.com/agent:v1", "1", offlineSecretOne),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("langsmith_deployment.test", "id", offlineDeploymentID),
					resource.TestCheckResourceAttr("langsmith_deployment.test", "display_name", "Offline deployment"),
					resource.TestCheckResourceAttr("langsmith_deployment.test", "latest_revision_id", offlineRevisionOne),
					resource.TestCheckResourceAttr("langsmith_deployment.test", "latest_revision_status", "DEPLOYED"),
					// Adopted from the service rather than diffed against null.
					resource.TestCheckResourceAttr("langsmith_deployment.test", "source_config.build_on_push", "false"),
					// The API requires a resource_spec object for external images,
					// but unconfigured defaults remain outside Terraform's state.
					resource.TestCheckNoResourceAttr("langsmith_deployment.test", "source_config.resource_spec.cpu"),
					// Terraform keeps owning what the service does not report.
					resource.TestCheckResourceAttr("langsmith_deployment.test", "source_revision_config.image_uri", "registry.example.com/agent:v1"),
					resource.TestCheckResourceAttr("data.langsmith_deployment_revision.test", "id", offlineRevisionOne),
					resource.TestCheckResourceAttr("data.langsmith_deployment_revision.test", "source_revision_config.image_uri", "registry.example.com/agent:v1"),
					resource.TestCheckResourceAttr("data.langsmith_deployment_revisions.test", "revisions.#", "1"),
					resource.TestCheckResourceAttr("data.langsmith_deployment_revisions.test", "revisions.0.id", offlineRevisionOne),
					deploymentStateExcludesSecrets,
				),
			},
			{
				Config:             deploymentAcceptanceConfig(server.URL, `display_name = "Offline deployment"`, "registry.example.com/agent:v1", "1", offlineSecretOne),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
			{
				ResourceName:      "langsmith_deployment.test",
				ImportState:       true,
				ImportStateId:     offlineDeploymentID,
				ImportStateVerify: true,
				// secrets_version is write-only bookkeeping the service never
				// stores, and an import cannot tell a desired resource_spec from
				// the defaults the service materialized around it.
				ImportStateVerifyIgnore: []string{"secrets_version", "source_config.resource_spec"},
				Check:                   deploymentStateExcludesSecrets,
			},
			{
				// A new image and a new secrets version: exactly one revision.
				Config: deploymentAcceptanceConfig(server.URL, `display_name = "Offline deployment"`, "registry.example.com/agent:v2", "2", offlineSecretTwo),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("langsmith_deployment.test", "latest_revision_id", offlineRevisionTwo),
					resource.TestCheckResourceAttr("langsmith_deployment.test", "latest_revision_status", "DEPLOYED"),
					resource.TestCheckResourceAttr("langsmith_deployment.test", "source_revision_config.image_uri", "registry.example.com/agent:v2"),
					resource.TestCheckResourceAttr("data.langsmith_deployment_revisions.test", "revisions.#", "2"),
					resource.TestCheckResourceAttr("data.langsmith_deployment_revisions.test", "revisions.0.id", offlineRevisionTwo),
					backend.expectRevisionPosts(1),
					deploymentStateExcludesSecrets,
				),
			},
			{
				// Renaming touches no revision input, so the service should see a
				// PATCH and nothing else. Creating a revision here would rebuild
				// and redeploy the agent for a cosmetic change.
				Config: deploymentAcceptanceConfig(server.URL, `display_name = "Offline deployment renamed"`, "registry.example.com/agent:v2", "2", offlineSecretTwo),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("langsmith_deployment.test", "display_name", "Offline deployment renamed"),
					resource.TestCheckResourceAttr("langsmith_deployment.test", "latest_revision_id", offlineRevisionTwo),
					backend.expectRevisionPosts(1),
					deploymentStateExcludesSecrets,
				),
			},
		},
	})
}

// Omitted optional arguments must remain stable across API defaults and updates.
func TestAccDeploymentOfflineWithoutDisplayName(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the offline Terraform acceptance test")
	}

	backend := newDeploymentContractBackend(t)
	server := httptest.NewServer(backend)
	t.Cleanup(server.Close)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: offlineDeploymentFactories(),
		Steps: []resource.TestStep{
			{
				Config: deploymentAcceptanceConfig(server.URL, "", "registry.example.com/agent:v1", "1", offlineSecretOne),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("langsmith_deployment.test", "id", offlineDeploymentID),
					resource.TestCheckNoResourceAttr("langsmith_deployment.test", "display_name"),
					resource.TestCheckResourceAttr("langsmith_deployment.test", "source_config.deployment_type", "prod"),
					resource.TestCheckResourceAttr("langsmith_deployment.test", "source_config.build_on_push", "false"),
				),
			},
			{
				Config:             deploymentAcceptanceConfig(server.URL, "", "registry.example.com/agent:v1", "1", offlineSecretOne),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
			{
				Config: deploymentAcceptanceConfig(server.URL, "", "registry.example.com/agent:v2", "2", offlineSecretTwo),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("langsmith_deployment.test", "latest_revision_id", offlineRevisionTwo),
					resource.TestCheckNoResourceAttr("langsmith_deployment.test", "display_name"),
					backend.expectRevisionPosts(1),
				),
			},
		},
	})
}

func offlineDeploymentFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"langsmith": providerserver.NewProtocol6WithError(New("test")()),
	}
}

func deploymentAcceptanceConfig(serverURL, displayName, image, secretsVersion, secret string) string {
	return fmt.Sprintf(`
provider "langsmith" {
  control_plane_url = %q
  api_key           = "offline-test-key"
  workspace_id      = "offline-workspace"
}

resource "langsmith_deployment" "test" {
  name   = "offline-agent"
  source = "external_docker"
  %s

  source_config = {
    resource_spec = {}
  }

  source_revision_config = {
    image_uri = %q
  }

  secrets = {
    API_TOKEN = %q
  }
  secrets_version = %q
}

data "langsmith_deployment_revision" "test" {
  deployment_id = langsmith_deployment.test.id
  revision_id   = langsmith_deployment.test.latest_revision_id
}

data "langsmith_deployment_revisions" "test" {
  deployment_id = langsmith_deployment.test.id
  limit         = 10
  offset        = 0
  status        = "DEPLOYED"
}
`, serverURL+"/api-host", displayName, image, secret, secretsVersion)
}

func deploymentStateExcludesSecrets(state *terraform.State) error {
	for address, instance := range state.RootModule().Resources {
		for key, value := range instance.Primary.Attributes {
			if strings.Contains(value, offlineSecretOne) || strings.Contains(value, offlineSecretTwo) {
				return fmt.Errorf("secret value found in Terraform state at %s.%s", address, key)
			}
		}
	}
	return nil
}

type deploymentContractBackend struct {
	t              *testing.T
	mu             sync.Mutex
	displayName    *string
	image          string
	latestRevision string
	revisions      []string
	revisionGets   map[string]int
	created        bool
	deleted        bool
	createPosts    int
	revisionPosts  int
	secrets        []any
	revisionSecret string
	resourceSpec   map[string]any
}

func newDeploymentContractBackend(t *testing.T) *deploymentContractBackend {
	return &deploymentContractBackend{
		t: t, revisionGets: map[string]int{}, revisionSecret: offlineSecretTwo,
		resourceSpec: map[string]any{
			"min_scale": 1, "max_scale": 1, "cpu": 1, "cpu_limit": nil,
			"memory_mb": 2048, "memory_limit_mb": nil,
			"labels": nil, "annotations": nil, "service_account_name": nil,
		},
	}
}

func (b *deploymentContractBackend) expectRevisionPosts(want int) resource.TestCheckFunc {
	return func(*terraform.State) error {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.revisionPosts != want {
			return fmt.Errorf("revision creations = %d, want %d", b.revisionPosts, want)
		}
		return nil
	}
}

func (b *deploymentContractBackend) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if req.Header.Get("X-Api-Key") != "offline-test-key" {
		b.reject(w, req, "missing API key")
		return
	}
	if req.Header.Get("X-Tenant-Id") != "offline-workspace" {
		b.reject(w, req, "missing tenant ID")
		return
	}
	if strings.Contains(req.URL.Path, "/v1/") || !strings.HasPrefix(req.URL.Path, "/api-host/v2/") {
		b.reject(w, req, "non-v2 control-plane path")
		return
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		b.reject(w, req, err.Error())
		return
	}
	containsSecret := strings.Contains(string(body), offlineSecretOne) || strings.Contains(string(body), offlineSecretTwo)
	if containsSecret && req.Method != http.MethodPost {
		b.reject(w, req, "secret sent outside POST body")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(req.URL.Path, "/api-host/")
	switch {
	case path == "v2/deployments" && req.Method == http.MethodPost:
		b.create(w, req, body)
	case path == "v2/deployments/"+offlineDeploymentID && req.Method == http.MethodPatch:
		b.patch(w, req, body)
	case path == "v2/deployments/"+offlineDeploymentID && req.Method == http.MethodGet:
		b.readDeployment(w, req)
	case path == "v2/deployments/"+offlineDeploymentID+"/revisions" && req.Method == http.MethodPost:
		b.createRevision(w, req, body)
	case path == "v2/deployments/"+offlineDeploymentID+"/revisions" && req.Method == http.MethodGet:
		b.listRevisions(w, req)
	case strings.HasPrefix(path, "v2/deployments/"+offlineDeploymentID+"/revisions/") && req.Method == http.MethodGet:
		b.readRevision(w, req, strings.TrimPrefix(path, "v2/deployments/"+offlineDeploymentID+"/revisions/"))
	case path == "v2/deployments/"+offlineDeploymentID && req.Method == http.MethodDelete:
		b.deleted = true
		b.created = false
		w.WriteHeader(http.StatusNoContent)
	default:
		b.reject(w, req, "unexpected route")
	}
}

func (b *deploymentContractBackend) create(w http.ResponseWriter, req *http.Request, body []byte) {
	var payload map[string]any
	if !b.decode(w, req, body, &payload) {
		return
	}
	if payload["name"] != "offline-agent" || payload["source"] != "external_docker" || deploymentNestedString(payload, "source_revision_config", "image_uri") != "registry.example.com/agent:v1" || !deploymentPayloadHasSecret(payload, offlineSecretOne) {
		b.reject(w, req, "invalid create payload")
		return
	}
	sourceConfig, ok := payload["source_config"].(map[string]any)
	if !ok || sourceConfig["deployment_type"] != nil || sourceConfig["resource_spec"] == nil {
		b.reject(w, req, "external_docker requires resource_spec and rejects deployment_type on create")
		return
	}
	if spec, ok := sourceConfig["resource_spec"].(map[string]any); ok {
		for key, value := range spec {
			b.resourceSpec[key] = value
		}
	}
	// DeploymentCreateRequest has no display_name field, which is why the
	// provider needs a follow-up PATCH to set one.
	if _, ok := payload["display_name"]; ok {
		b.reject(w, req, "create payload contains display_name")
		return
	}
	b.created = true
	b.deleted = false
	b.displayName = nil
	b.image = "registry.example.com/agent:v1"
	b.latestRevision = offlineRevisionOne
	b.revisions = []string{offlineRevisionOne}
	b.createPosts++
	b.secrets, _ = payload["secrets"].([]any)
	w.WriteHeader(http.StatusCreated)
	b.writeDeployment(w)
}

func (b *deploymentContractBackend) patch(w http.ResponseWriter, req *http.Request, body []byte) {
	var payload map[string]any
	if !b.decode(w, req, body, &payload) {
		return
	}
	for key := range payload {
		if key != "display_name" && key != "source_config" {
			b.reject(w, req, "PATCH carries unsupported field "+key)
			return
		}
	}
	if raw, ok := payload["display_name"]; ok {
		name, isString := raw.(string)
		if !isString || name == "" {
			// DeploymentPatchRequest.display_name has min_length=1, so an empty
			// string is a 422 rather than a way to clear the name.
			b.unprocessable(w, "display_name must be at least 1 character")
			return
		}
		b.displayName = &name
	}
	if raw, ok := payload["source_config"]; ok {
		nested, isObject := raw.(map[string]any)
		if !isObject {
			b.reject(w, req, "PATCH source_config is not an object")
			return
		}
		for key := range nested {
			if key != "build_on_push" && key != "custom_url" {
				b.reject(w, req, "PATCH source_config carries unsupported field "+key)
				return
			}
		}
	}
	b.writeDeployment(w)
}

func (b *deploymentContractBackend) createRevision(w http.ResponseWriter, req *http.Request, body []byte) {
	var payload map[string]any
	if !b.decode(w, req, body, &payload) {
		return
	}
	image := deploymentNestedString(payload, "source_revision_config", "image_uri")
	if (image != "registry.example.com/agent:v1" && image != "registry.example.com/agent:v2") || (b.revisionSecret != "" && !deploymentPayloadHasSecret(payload, b.revisionSecret)) {
		b.reject(w, req, "invalid nested revision payload")
		return
	}
	if _, ok := payload["display_name"]; ok {
		b.reject(w, req, "revision payload contains display_name")
		return
	}
	b.image = image
	if sourceConfig, ok := payload["source_config"].(map[string]any); ok {
		if spec, ok := sourceConfig["resource_spec"].(map[string]any); ok {
			b.resourceSpec = spec
		}
	}
	b.secrets, _ = payload["secrets"].([]any)
	b.latestRevision = offlineRevisionTwo
	b.revisions = append([]string{offlineRevisionTwo}, b.revisions...)
	b.revisionPosts++
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`{"id":"` + offlineRevisionTwo + `","status":"CREATING"}`))
}

func (b *deploymentContractBackend) readDeployment(w http.ResponseWriter, req *http.Request) {
	if !b.created {
		http.NotFound(w, req)
		return
	}
	b.writeDeployment(w)
}

// Mirror API nulls, defaults, and the resource spec for external images.
func (b *deploymentContractBackend) writeDeployment(w http.ResponseWriter) {
	response := map[string]any{
		"id": offlineDeploymentID, "name": "offline-agent", "source": "external_docker", "display_name": b.displayName,
		"source_config": map[string]any{
			"integration_id": nil, "repo_url": nil, "deployment_type": "prod", "build_on_push": false,
			"custom_url": nil, "listener_id": nil, "listener_config": nil,
			"install_command": nil, "build_command": nil, "template_id": nil,
			"resource_spec": b.resourceSpec,
		},
		"source_revision_config": map[string]any{
			"repo_ref": nil, "langgraph_config_path": nil, "image_uri": b.image,
			"source_tarball_path": nil, "repo_commit_sha": nil,
		},
		"secret_references": []any{}, "tenant_id": "offline-workspace",
		"created_at": "2025-01-01T00:00:00Z", "updated_at": "2025-01-01T00:00:01Z", "status": "READY",
		"latest_revision_id": b.latestRevision, "active_revision_id": b.latestRevision,
		"image_version": nil, "is_managed_deep_agent": false,
		"secrets": b.secrets,
	}
	_ = json.NewEncoder(w).Encode(response)
}

func (b *deploymentContractBackend) readRevision(w http.ResponseWriter, req *http.Request, id string) {
	if id != offlineRevisionOne && id != offlineRevisionTwo {
		http.NotFound(w, req)
		return
	}
	b.revisionGets[id]++
	status := "DEPLOYED"
	if b.revisionGets[id] == 1 {
		status = "CREATING"
	}
	image := "registry.example.com/agent:v1"
	if id == offlineRevisionTwo {
		image = "registry.example.com/agent:v2"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": id, "created_at": "2025-01-01T00:00:00Z", "updated_at": "2025-01-01T00:00:01Z", "status": status,
		"source": "external_docker", "source_revision_config": map[string]any{"image_uri": image, "tracked_packages": []string{"agent"}},
	})
}

func (b *deploymentContractBackend) listRevisions(w http.ResponseWriter, req *http.Request) {
	if req.URL.Query().Get("limit") != "10" || req.URL.Query().Get("offset") != "0" || req.URL.Query().Get("status") != "DEPLOYED" {
		b.reject(w, req, "invalid revision list query")
		return
	}
	resources := make([]map[string]any, 0, len(b.revisions))
	for _, id := range b.revisions {
		image := "registry.example.com/agent:v1"
		if id == offlineRevisionTwo {
			image = "registry.example.com/agent:v2"
		}
		resources = append(resources, map[string]any{"id": id, "created_at": "2025-01-01T00:00:00Z", "updated_at": "2025-01-01T00:00:01Z", "status": "DEPLOYED", "source": "external_docker", "source_revision_config": map[string]any{"image_uri": image, "tracked_packages": []string{"agent"}}})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"resources": resources, "offset": len(resources)})
}

func (b *deploymentContractBackend) decode(w http.ResponseWriter, req *http.Request, body []byte, value any) bool {
	if err := json.Unmarshal(body, value); err != nil {
		b.reject(w, req, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// Unexpected invalid requests fail the test; unprocessable models expected API errors.
func (b *deploymentContractBackend) reject(w http.ResponseWriter, req *http.Request, reason string) {
	b.t.Errorf("%s %s: %s", req.Method, req.URL.RequestURI(), reason)
	http.Error(w, reason, http.StatusBadRequest)
}

func (b *deploymentContractBackend) unprocessable(w http.ResponseWriter, detail string) {
	w.WriteHeader(http.StatusUnprocessableEntity)
	_, _ = w.Write([]byte(`{"detail":"` + detail + `"}`))
}

func deploymentNestedString(payload map[string]any, object, key string) string {
	nested, _ := payload[object].(map[string]any)
	value, _ := nested[key].(string)
	return value
}

func deploymentPayloadHasSecret(payload map[string]any, expected string) bool {
	secrets, ok := payload["secrets"].([]any)
	if !ok {
		return false
	}
	for _, entry := range secrets {
		secret, ok := entry.(map[string]any)
		if ok && secret["name"] == "API_TOKEN" && secret["value"] == expected {
			return true
		}
	}
	return false
}
