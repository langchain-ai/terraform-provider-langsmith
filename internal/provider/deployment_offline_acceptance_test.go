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
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(New("test")()),
		},
		Steps: []resource.TestStep{
			{
				Config: deploymentAcceptanceConfig(server.URL, "Offline deployment", "registry.example.com/agent:v1", "1", offlineSecretOne),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("langsmith_deployment.test", "id", offlineDeploymentID),
					resource.TestCheckResourceAttr("langsmith_deployment.test", "display_name", "Offline deployment"),
					resource.TestCheckResourceAttr("langsmith_deployment.test", "latest_revision_id", offlineRevisionOne),
					resource.TestCheckResourceAttr("langsmith_deployment.test", "latest_revision_status", "DEPLOYED"),
					resource.TestCheckResourceAttr("data.langsmith_deployment_revision.test", "id", offlineRevisionOne),
					resource.TestCheckResourceAttr("data.langsmith_deployment_revision.test", "source_revision_config.image_uri", "registry.example.com/agent:v1"),
					resource.TestCheckResourceAttr("data.langsmith_deployment_revisions.test", "revisions.#", "1"),
					resource.TestCheckResourceAttr("data.langsmith_deployment_revisions.test", "revisions.0.id", offlineRevisionOne),
					deploymentStateExcludesSecrets,
				),
			},
			{
				Config:             deploymentAcceptanceConfig(server.URL, "Offline deployment", "registry.example.com/agent:v1", "1", offlineSecretOne),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
			{
				ResourceName:            "langsmith_deployment.test",
				ImportState:             true,
				ImportStateId:           offlineDeploymentID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"secrets_version"},
				Check:                   deploymentStateExcludesSecrets,
			},
			{
				Config: deploymentAcceptanceConfig(server.URL, "Offline deployment updated", "registry.example.com/agent:v2", "2", offlineSecretTwo),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("langsmith_deployment.test", "display_name", "Offline deployment updated"),
					resource.TestCheckResourceAttr("langsmith_deployment.test", "latest_revision_id", offlineRevisionTwo),
					resource.TestCheckResourceAttr("langsmith_deployment.test", "latest_revision_status", "DEPLOYED"),
					resource.TestCheckResourceAttr("data.langsmith_deployment_revision.test", "id", offlineRevisionTwo),
					resource.TestCheckResourceAttr("data.langsmith_deployment_revision.test", "source_revision_config.image_uri", "registry.example.com/agent:v2"),
					resource.TestCheckResourceAttr("data.langsmith_deployment_revisions.test", "revisions.#", "2"),
					resource.TestCheckResourceAttr("data.langsmith_deployment_revisions.test", "revisions.0.id", offlineRevisionTwo),
					deploymentStateExcludesSecrets,
				),
			},
		},
	})

	backend.assertCoverage()
}

func deploymentAcceptanceConfig(serverURL, displayName, image, secretsVersion, secret string) string {
	return fmt.Sprintf(`
provider "langsmith" {
  control_plane_url = %q
  api_key           = "offline-test-key"
  workspace_id      = "offline-workspace"
}

resource "langsmith_deployment" "test" {
  name         = "offline-agent"
  display_name = %q
  source       = "external_docker"

  source_config = {
    deployment_type = "prod"
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
	displayName    string
	image          string
	latestRevision string
	revisions      []string
	revisionGets   map[string]int
	created        bool
	deleted        bool
	createPosts    int
	revisionPosts  int
	patches        int
}

func newDeploymentContractBackend(t *testing.T) *deploymentContractBackend {
	return &deploymentContractBackend{t: t, revisionGets: map[string]int{}}
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
	if payload["name"] != "offline-agent" || payload["source"] != "external_docker" || deploymentNestedString(payload, "source_config", "deployment_type") != "prod" || deploymentNestedString(payload, "source_revision_config", "image_uri") != "registry.example.com/agent:v1" || !deploymentPayloadHasSecret(payload, offlineSecretOne) {
		b.reject(w, req, "invalid create payload")
		return
	}
	if _, ok := payload["display_name"]; ok {
		b.reject(w, req, "create payload contains display_name")
		return
	}
	b.created = true
	b.image = "registry.example.com/agent:v1"
	b.latestRevision = offlineRevisionOne
	b.revisions = []string{offlineRevisionOne}
	b.createPosts++
	w.WriteHeader(http.StatusCreated)
	b.writeDeployment(w)
}

func (b *deploymentContractBackend) patch(w http.ResponseWriter, req *http.Request, body []byte) {
	var payload map[string]any
	if !b.decode(w, req, body, &payload) {
		return
	}
	name, ok := payload["display_name"].(string)
	if !ok || len(payload) != 1 {
		b.reject(w, req, "PATCH must contain only display_name")
		return
	}
	b.displayName = name
	b.patches++
	b.writeDeployment(w)
}

func (b *deploymentContractBackend) createRevision(w http.ResponseWriter, req *http.Request, body []byte) {
	var payload map[string]any
	if !b.decode(w, req, body, &payload) {
		return
	}
	if deploymentNestedString(payload, "source_revision_config", "image_uri") != "registry.example.com/agent:v2" || !deploymentPayloadHasSecret(payload, offlineSecretTwo) {
		b.reject(w, req, "invalid nested revision payload")
		return
	}
	if _, ok := payload["display_name"]; ok {
		b.reject(w, req, "revision payload contains display_name")
		return
	}
	b.image = "registry.example.com/agent:v2"
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

func (b *deploymentContractBackend) writeDeployment(w http.ResponseWriter) {
	response := map[string]any{
		"id": offlineDeploymentID, "name": "offline-agent", "source": "external_docker", "display_name": b.displayName,
		"source_config": map[string]any{"deployment_type": "prod"}, "source_revision_config": map[string]any{"image_uri": b.image},
		"secret_references": []any{}, "shareable": false, "route_through_gateway": false, "tenant_id": "offline-workspace",
		"created_at": "2025-01-01T00:00:00Z", "updated_at": "2025-01-01T00:00:01Z", "status": "READY",
		"latest_revision_id": b.latestRevision, "active_revision_id": b.latestRevision, "tracer_session_id": "44444444-4444-4444-4444-444444444444", "url": "https://offline.example.com",
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
		"status_message": nil, "source": "external_docker", "source_revision_config": map[string]any{"image_uri": image, "tracked_packages": []string{"agent"}},
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
		resources = append(resources, map[string]any{"id": id, "created_at": "2025-01-01T00:00:00Z", "updated_at": "2025-01-01T00:00:01Z", "status": "DEPLOYED", "status_message": nil, "source": "external_docker", "source_revision_config": map[string]any{"image_uri": image, "tracked_packages": []string{"agent"}}})
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

func (b *deploymentContractBackend) reject(w http.ResponseWriter, req *http.Request, reason string) {
	b.t.Errorf("%s %s: %s", req.Method, req.URL.RequestURI(), reason)
	http.Error(w, reason, http.StatusBadRequest)
}

func (b *deploymentContractBackend) assertCoverage() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.createPosts != 1 || b.revisionPosts != 1 || b.patches != 2 || b.revisionGets[offlineRevisionOne] < 2 || b.revisionGets[offlineRevisionTwo] < 2 || !b.deleted || b.created {
		b.t.Fatalf("contract coverage: create=%d revision=%d patches=%d revision_gets=%v deleted=%t exists=%t", b.createPosts, b.revisionPosts, b.patches, b.revisionGets, b.deleted, b.created)
	}
}

func deploymentNestedString(payload map[string]any, object, key string) string {
	nested, _ := payload[object].(map[string]any)
	value, _ := nested[key].(string)
	return value
}

func deploymentPayloadHasSecret(payload map[string]any, expected string) bool {
	secrets, ok := payload["secrets"].([]any)
	if !ok || len(secrets) != 1 {
		return false
	}
	secret, ok := secrets[0].(map[string]any)
	return ok && secret["name"] == "API_TOKEN" && secret["value"] == expected
}
