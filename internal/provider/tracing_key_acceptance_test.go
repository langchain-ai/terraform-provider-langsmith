package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

// TF_ACC=1 runs real Terraform against an HTTP contract fixture. Set
// LANGSMITH_PROVIDER_ACC=1 and LANGSMITH_WORKSPACE_ID to use a configured backend.
func TestAccTracingKey(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run Terraform acceptance")
	}
	ctx := context.Background()
	workspaceID := "11111111-1111-1111-1111-111111111111"
	providerConfig := ""
	var client *langsmith.Client
	if os.Getenv("LANGSMITH_PROVIDER_ACC") == "1" {
		workspaceID = os.Getenv("LANGSMITH_WORKSPACE_ID")
		if workspaceID == "" {
			t.Fatal("LANGSMITH_WORKSPACE_ID is required for live tracing acceptance")
		}
		client = langsmith.NewClient(option.WithTenantID(workspaceID))
		var caller struct {
			LSUserID     string `json:"ls_user_id"`
			APIKeyID     string `json:"api_key_id"`
			IsTracingKey bool   `json:"is_tracing_key"`
		}
		if err := client.Get(ctx, "auth", nil, &caller); err != nil {
			t.Fatal("unable to verify live acceptance caller")
		}
		if caller.LSUserID != "" || caller.APIKeyID == "" || caller.IsTracingKey {
			t.Fatal("live tracing acceptance requires a service key")
		}
	} else {
		var mu sync.Mutex
		keys := map[string]tracingKeyAPIResponse{}
		nextID := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			if req.URL.Path == "/api/v1/info" {
				writeJSON(t, w, map[string]any{"instance_flags": map[string]bool{"tracing_keys_service_account_management_supported": true}})
				return
			}
			if req.Method == http.MethodPost && req.URL.Path == "/api/v1/orgs/current/tracing-keys" {
				var payload struct {
					Description string   `json:"description"`
					Workspaces  []string `json:"workspaces"`
					ExpiresAt   *string  `json:"expires_at"`
				}
				decodeJSON(t, req, &payload)
				for i, workspace := range payload.Workspaces {
					id, err := uuid.Parse(workspace)
					if err != nil {
						t.Error("invalid workspace UUID reached the API")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					payload.Workspaces[i] = id.String()
				}
				if payload.ExpiresAt != nil {
					expiry, err := time.Parse(time.RFC3339Nano, *payload.ExpiresAt)
					if err != nil {
						t.Error("invalid expiry reached the API")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					// Match the API's microsecond timestamp precision.
					normalized := expiry.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
					payload.ExpiresAt = &normalized
				}
				nextID++
				id := fmt.Sprintf("22222222-2222-2222-2222-%012d", nextID)
				result := tracingKeyAPIResponse{
					serviceKeyAPIResponse: serviceKeyAPIResponse{ID: id, Description: payload.Description, ExpiresAt: payload.ExpiresAt, ShortKey: "redacted", WorkspaceNames: []string{"Production"}},
					WorkspaceIDs:          payload.Workspaces, IsTracingKey: true,
				}
				keys[id] = result
				result.Key = "one-time-secret-" + id
				writeJSON(t, w, result)
				return
			}
			id := strings.TrimPrefix(req.URL.Path, "/api/v1/orgs/current/tracing-keys/")
			switch req.Method {
			case http.MethodGet:
				if key, ok := keys[id]; ok {
					writeJSON(t, w, key)
					return
				}
			case http.MethodDelete:
				delete(keys, id)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(server.Close)
		providerConfig = fmt.Sprintf("provider \"langsmith\" {\n api_url = %q\n api_key = \"test-service-key\"\n}\n", server.URL)
		client = langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("test-service-key"))
	}
	expiresAt := time.Now().UTC().Add(time.Hour).Format("2006-01-02T15:04:05") + ".123456Z"
	config := func(description string) string {
		return providerConfig + fmt.Sprintf(`
resource "langsmith_tracing_key" "test" {
  description = %q
  workspaces = [%q]
  expires_at = %q
}
`, description, workspaceID, expiresAt)
	}
	address := "langsmith_tracing_key.test"
	currentID := ""
	currentSecret := ""
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(New("test")()),
		},
		CheckDestroy: func(state *terraform.State) error {
			for _, rs := range state.RootModule().Resources {
				var result tracingKeyAPIResponse
				if err := client.Get(ctx, tracingKeyPath(rs.Primary.ID), nil, &result); !isLangSmithNotFound(err) {
					return fmt.Errorf("tracing key still exists or lookup failed")
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config:      strings.Replace(config("Terraform tracing acceptance"), workspaceID, "11111111-AAAA-4111-8111-111111111111", 1),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile("lowercase canonical UUID"),
			},
			{
				Config:      strings.Replace(config("Terraform tracing acceptance"), ".123456Z", ".123456789Z", 1),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`at most six fractional\s+digits`),
			},
			{Config: config("Terraform tracing acceptance"), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet(address, "key"),
				resource.TestCheckResourceAttr(address, "workspaces.0", workspaceID),
				resource.TestCheckResourceAttr(address, "expires_at", expiresAt),
				resource.TestCheckResourceAttrWith(address, "id", func(value string) error { currentID = value; return nil }),
				resource.TestCheckResourceAttrWith(address, "key", func(secret string) error {
					currentSecret = secret
					if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
						return nil
					}
					tracing := langsmith.NewClient(option.WithAPIKey(secret), option.WithTenantID(workspaceID))
					runID, err := uuid.NewV7()
					if err != nil {
						return err
					}
					startTime := time.Now().UTC()
					dottedOrder := fmt.Sprintf("%s%06dZ%s", startTime.Format("20060102T150405"), startTime.Nanosecond()/1000, runID)
					var project struct {
						ID string `json:"id"`
					}
					if err := tracing.Post(ctx, "api/v1/sessions", map[string]string{"name": "terraform-tracing-" + runID.String()}, &project); err != nil {
						return fmt.Errorf("tracing project creation failed")
					}
					if err := tracing.Post(ctx, "api/v1/runs", map[string]any{
						"id": runID, "trace_id": runID, "dotted_order": dottedOrder, "name": "Terraform tracing acceptance", "run_type": "chain", "session_id": project.ID,
						"start_time": startTime.Format(time.RFC3339Nano), "inputs": map[string]string{"test": "tracing"},
					}, nil); err != nil {
						return fmt.Errorf("issued tracing key could not ingest a trace")
					}
					var apiErr *langsmith.Error
					if err := tracing.Get(ctx, "api/v1/runs/"+runID.String(), nil, nil); !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden {
						return fmt.Errorf("issued tracing key did not reject telemetry reads")
					}
					return nil
				}),
			)},
			{Config: config("Terraform tracing acceptance"), PlanOnly: true, ExpectNonEmptyPlan: false},
			{RefreshState: true, Check: resource.TestCheckResourceAttrWith(address, "key", func(secret string) error {
				if secret == "" || secret != currentSecret {
					return fmt.Errorf("refresh must preserve the one-time secret")
				}
				return nil
			})},
			{Config: config("Terraform tracing acceptance"), PlanOnly: true, ExpectNonEmptyPlan: false},
			{ResourceName: address, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"key"}, ImportStateCheck: func(states []*terraform.InstanceState) error {
				if len(states) != 1 || states[0].Attributes["key"] != "" {
					return fmt.Errorf("import must not recover the secret")
				}
				return nil
			}},
			{PreConfig: func() {
				if err := client.Delete(ctx, tracingKeyPath(currentID), nil, nil); err != nil {
					t.Fatal("external deletion failed")
				}
			}, Config: config("Terraform tracing acceptance"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(address, plancheck.ResourceActionCreate)}}, Check: resource.TestCheckResourceAttrWith(address, "key", func(secret string) error {
				currentSecret = secret
				return nil
			})},
			{Config: config("Terraform tracing replacement"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(address, plancheck.ResourceActionReplace)}}, Check: resource.TestCheckResourceAttrWith(address, "key", func(secret string) error {
				if secret == "" || secret == currentSecret {
					return fmt.Errorf("replacement must store the newly issued secret")
				}
				return nil
			})},
		},
	})
}
