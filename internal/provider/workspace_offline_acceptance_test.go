package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/langchain-ai/langsmith-go"
)

func TestAccWorkspaceDeletionPolicyOffline(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the offline Terraform acceptance test")
	}

	backend := newWorkspaceContractBackend(t)
	server := httptest.NewServer(backend)
	t.Cleanup(server.Close)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(New("test")()),
		},
		CheckDestroy: func(*terraform.State) error {
			if got := backend.deleteCount(); got != 0 {
				return fmt.Errorf("DELETE requests = %d, want 0 with deletion_policy ABANDON", got)
			}
			if !backend.exists() {
				return fmt.Errorf("workspace was removed from the backend, want it left in place")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config:      workspaceAcceptanceConfig(server.URL, "Offline Workspace", `"KEEP"`),
				ExpectError: regexp.MustCompile(`deletion_policy value must be one of`),
			},
			{
				Config: workspaceAcceptanceConfig(server.URL, "Offline Workspace", ""),
				Check:  resource.TestCheckResourceAttr("langsmith_workspace.test", "deletion_policy", "DELETE"),
			},
			{
				ResourceName:      "langsmith_workspace.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				PreConfig: backend.resetWrites,
				Config:    workspaceAcceptanceConfig(server.URL, "Offline Workspace", `"PREVENT"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("langsmith_workspace.test", "deletion_policy", "PREVENT"),
					backend.expectNoWrites(),
				),
			},
			{
				Config:      workspaceAcceptanceConfig(server.URL, "Offline Workspace", `"PREVENT"`),
				Destroy:     true,
				ExpectError: regexp.MustCompile(`deletion_policy is PREVENT`),
			},
			{
				Config: workspaceAcceptanceConfig(server.URL, "Offline Workspace Renamed", `"PREVENT"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("langsmith_workspace.test", "display_name", "Offline Workspace Renamed"),
					resource.TestCheckResourceAttr("langsmith_workspace.test", "deletion_policy", "PREVENT"),
				),
			},
			{
				PreConfig: backend.resetWrites,
				Config:    workspaceAcceptanceConfig(server.URL, "Offline Workspace Renamed", `"ABANDON"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("langsmith_workspace.test", "deletion_policy", "ABANDON"),
					backend.expectNoWrites(),
				),
			},
			{
				Config:             workspaceAcceptanceConfig(server.URL, "Offline Workspace Renamed", `"ABANDON"`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

func workspaceAcceptanceConfig(apiURL, displayName, deletionPolicy string) string {
	policy := ""
	if deletionPolicy != "" {
		policy = "\n  deletion_policy = " + deletionPolicy
	}
	return fmt.Sprintf(`
provider "langsmith" {
  api_url      = %q
  api_key      = "offline-test-key"
  workspace_id = "offline-workspace"
}

resource "langsmith_workspace" "test" {
  display_name = %q%s
}
`, apiURL, displayName, policy)
}

type workspaceContractBackend struct {
	t         *testing.T
	mu        sync.Mutex
	workspace *langsmith.WorkspaceListResponse
	writes    []string
	deletes   int
}

func newWorkspaceContractBackend(t *testing.T) *workspaceContractBackend {
	t.Helper()
	return &workspaceContractBackend{t: t}
}

func (b *workspaceContractBackend) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()

	const collection = "/api/v1/workspaces"
	switch {
	case req.Method == http.MethodGet && req.URL.Path == collection:
		workspaces := []langsmith.WorkspaceListResponse{}
		if b.workspace != nil {
			workspaces = append(workspaces, *b.workspace)
		}
		writeJSON(b.t, w, workspaces)
	case req.Method == http.MethodPost && req.URL.Path == collection:
		b.writes = append(b.writes, req.Method+" "+req.URL.Path)
		var payload struct {
			DisplayName string `json:"display_name"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b.workspace = &langsmith.WorkspaceListResponse{
			ID:          "workspace-1",
			CreatedAt:   time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC),
			DisplayName: payload.DisplayName,
		}
		writeJSON(b.t, w, b.workspace)
	case strings.HasPrefix(req.URL.Path, collection+"/"):
		b.writes = append(b.writes, req.Method+" "+req.URL.Path)
		if b.workspace == nil || strings.TrimPrefix(req.URL.Path, collection+"/") != b.workspace.ID {
			http.Error(w, `{"detail":"workspace not found"}`, http.StatusNotFound)
			return
		}
		switch req.Method {
		case http.MethodPatch:
			var payload struct {
				DisplayName string `json:"display_name"`
			}
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			b.workspace.DisplayName = payload.DisplayName
			writeJSON(b.t, w, b.workspace)
		case http.MethodDelete:
			b.deletes++
			b.workspace = nil
			writeJSON(b.t, w, map[string]string{"id": "workspace-1"})
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	default:
		b.t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
		http.Error(w, "unexpected request", http.StatusNotFound)
	}
}

func (b *workspaceContractBackend) resetWrites() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writes = nil
}

func (b *workspaceContractBackend) expectNoWrites() resource.TestCheckFunc {
	return func(*terraform.State) error {
		b.mu.Lock()
		defer b.mu.Unlock()
		if len(b.writes) != 0 {
			return fmt.Errorf("write requests = %v, want none for a deletion_policy change", b.writes)
		}
		return nil
	}
}

func (b *workspaceContractBackend) deleteCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.deletes
}

func (b *workspaceContractBackend) exists() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.workspace != nil
}
