package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	frameworkprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

const workspaceTestDataPlaneID = "00000000-0000-0000-0000-000000000001"

func TestWorkspaceCreateDataPlaneResolutionFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		url    string
	}{
		{name: "not found", status: http.StatusNotFound},
		{name: "forbidden", status: http.StatusForbidden},
		{name: "missing URL"},
		{name: "relative URL", url: "/data-plane"},
		{name: "HTTP URL", url: "http://data-plane.example.com"},
		{name: "userinfo", url: "https://user@data-plane.example.com"},
		{name: "query", url: "https://data-plane.example.com?key=value"},
		{name: "fragment", url: "https://data-plane.example.com#fragment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newWorkspaceResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet || req.URL.Path != "/api/v1/orgs/current/data-planes/"+workspaceTestDataPlaneID {
					t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				if tc.status != 0 {
					http.Error(w, "lookup failed", tc.status)
					return
				}
				writeJSON(t, w, map[string]string{"api_url": tc.url})
			}), option.WithMaxRetries(0))
			_, err := r.createWorkspace(context.Background(), workspaceResourceModel{
				DisplayName: types.StringValue("Workspace"),
				DataPlaneID: types.StringValue(workspaceTestDataPlaneID),
			})
			if err == nil {
				t.Fatal("expected resolution failure before workspace creation")
			}
		})
	}
}

// Keep the provider's normal configuration path while trusting only the local
// test server's certificate for requests to the mock data plane.
type workspaceTestProvider struct {
	frameworkprovider.Provider
	httpClient *http.Client
}

func (p *workspaceTestProvider) Configure(ctx context.Context, req frameworkprovider.ConfigureRequest, resp *frameworkprovider.ConfigureResponse) {
	p.Provider.Configure(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		return
	}
	configured, ok := resp.ResourceData.(*langsmith.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Test Client", fmt.Sprintf("Expected *langsmith.Client, got %T", resp.ResourceData))
		return
	}
	client := langsmith.NewClient(append(configured.Options, option.WithHTTPClient(p.httpClient))...)
	resp.ResourceData = client
	resp.DataSourceData = client
}

func TestAccWorkspaceOfflineDataPlane(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the offline Terraform acceptance test")
	}
	for _, useDataPlane := range []bool{false, true} {
		t.Run(fmt.Sprintf("data_plane=%t", useDataPlane), func(t *testing.T) {
			var mu sync.Mutex
			var workspace map[string]any
			var creates, updates, deletes int
			var dataPlaneURL string
			mutation := func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get("X-Api-Key") != "offline-test-key" {
					t.Error("missing caller API key")
				}
				if req.Method != http.MethodPost && req.Header.Get("X-Tenant-Id") != "workspace-id" {
					t.Error("mutation did not select the managed workspace")
				}
				switch req.Method + " " + req.URL.Path {
				case "POST /api/v1/workspaces":
					creates++
					var payload map[string]any
					if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					if _, ok := payload["data_plane_id"]; ok {
						t.Error("data_plane_id must select routing, not be sent in the body")
					}
					workspace = map[string]any{"id": "workspace-id", "display_name": payload["display_name"], "tenant_handle": "workspace", "organization_id": "org-id", "created_at": "2026-05-21T01:02:03Z", "is_deleted": false, "is_personal": false}
					if useDataPlane {
						workspace["data_plane_url"] = dataPlaneURL + "/api/v1"
					}
					writeJSON(t, w, workspace)
				case "PATCH /api/v1/workspaces/workspace-id":
					updates++
					var payload map[string]string
					if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					workspace["display_name"] = payload["display_name"]
					writeJSON(t, w, map[string]string{"message": "Workspace updated"})
				case "DELETE /api/v1/workspaces/workspace-id":
					deletes++
					workspace = nil
					writeJSON(t, w, map[string]string{"message": "Workspace deleted"})
				default:
					t.Errorf("unexpected mutation: %s %s", req.Method, req.URL.Path)
					http.Error(w, "unexpected request", http.StatusBadRequest)
				}
			}
			dp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if !useDataPlane || req.Method == http.MethodGet {
					t.Errorf("unexpected data plane request: %s %s", req.Method, req.URL.Path)
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				mutation(w, req)
			}))
			t.Cleanup(dp.Close)
			dataPlaneURL = dp.URL
			cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if req.Method == http.MethodGet {
					if req.Header.Get("X-Tenant-Id") != "provider-workspace" {
						t.Error("shared provider tenant was changed")
					}
					switch req.URL.Path {
					case "/api/v1/orgs/current/data-planes/" + workspaceTestDataPlaneID, "/api/v1/orgs/current/data-planes/00000000-0000-0000-0000-000000000002":
						if !useDataPlane {
							t.Error("default creation should not resolve a data plane")
						}
						writeJSON(t, w, map[string]string{"api_url": dp.URL + "/api/v1/"})
					case "/api/v1/workspaces":
						items := []map[string]any{}
						if workspace != nil {
							items = append(items, workspace)
						}
						writeJSON(t, w, items)
					default:
						t.Errorf("unexpected control plane read: %s", req.URL.Path)
						http.Error(w, "unexpected request", http.StatusBadRequest)
					}
					return
				}
				if useDataPlane {
					t.Error("BYOC mutation incorrectly sent to control plane")
					http.Error(w, "use data plane", http.StatusForbidden)
					return
				}
				mutation(w, req)
			}))
			t.Cleanup(cp.Close)
			hcl := func(name, selector string) string {
				return fmt.Sprintf(`provider "langsmith" {
  api_url = %q
  api_key = "offline-test-key"
  workspace_id = "provider-workspace"
}
resource "langsmith_workspace" "test" {
  display_name = %q
  %s
}`, cp.URL, name, selector)
			}
			selector, importID := "", "workspace-id"
			if useDataPlane {
				selector = fmt.Sprintf("data_plane_id = %q", workspaceTestDataPlaneID)
				importID += "/" + workspaceTestDataPlaneID
			}
			steps := []resource.TestStep{
				{Config: hcl("Created", selector), Check: resource.TestCheckResourceAttr("langsmith_workspace.test", "display_name", "Created")},
				{ResourceName: "langsmith_workspace.test", ImportState: true, ImportStateId: importID, ImportStateVerify: true},
				{Config: hcl("Renamed", selector), Check: resource.TestCheckResourceAttr("langsmith_workspace.test", "display_name", "Renamed")},
				{Config: hcl("Renamed", selector), PlanOnly: true, ExpectNonEmptyPlan: false},
			}
			if useDataPlane {
				steps = append(steps,
					resource.TestStep{ResourceName: "langsmith_workspace.test", ImportState: true, ImportStateId: "workspace-id", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"data_plane_id"}},
					resource.TestStep{Config: hcl("Renamed", `data_plane_id = "00000000-0000-0000-0000-000000000002"`),
						ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("langsmith_workspace.test", plancheck.ResourceActionDestroyBeforeCreate)}}},
				)
			}
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
					"langsmith": providerserver.NewProtocol6WithError(&workspaceTestProvider{Provider: New("test")(), httpClient: dp.Client()}),
				},
				Steps: steps,
			})
			mu.Lock()
			defer mu.Unlock()
			wantCreates := 1
			if useDataPlane {
				wantCreates++ // Changing the selector must replace the workspace.
			}
			if creates != wantCreates || updates != 1 || deletes != wantCreates {
				t.Errorf("mutations: creates=%d updates=%d deletes=%d; want %d, 1, %d", creates, updates, deletes, wantCreates, wantCreates)
			}
		})
	}
}

func TestWorkspaceMutationRejectsInvalidDataPlaneURL(t *testing.T) {
	r := newWorkspaceResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		t.Error("invalid data plane URL must not fall back to provider endpoint")
		http.Error(w, "unexpected request", http.StatusBadRequest)
	}))
	invalidURL := types.StringValue("http://data-plane.example.com")
	_, err := r.updateWorkspace(context.Background(), "workspace-id", workspaceResourceModel{DataPlaneURL: invalidURL})
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("update error = %v", err)
	}
	if err := r.deleteWorkspace(context.Background(), "workspace-id", invalidURL); err == nil {
		t.Error("expected delete to reject invalid URL")
	}
}
