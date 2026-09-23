package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// These tests run Terraform automatically against a local fake API. They never
// use production credentials or provision AWS infrastructure.
func TestAccDataPlaneOffline(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run offline Terraform acceptance tests")
	}
	for _, byovpc := range []bool{false, true} {
		t.Run(fmt.Sprintf("byovpc=%t", byovpc), func(t *testing.T) {
			backend := &dataPlaneContractBackend{t: t}
			server := httptest.NewServer(backend)
			t.Cleanup(server.Close)
			config := dataPlaneAcceptanceConfig(server.URL, byovpc, "")
			updated := dataPlaneAcceptanceConfig(server.URL, byovpc, `
  maintenance_window = "fri:02:00-fri:04:00"
  ttl = { enabled = false, short_days = 7, long_days = 30 }
  firewall = {
    allow_http = true
    allowed_domains = [".aws.langchain-byoc.com", "api.example.com"]
    allowed_cidrs = {}
  }
  fleet_oidc = {
    is_enabled = true
    provider = "custom"
    issuer_url = "https://login.example.com"
    audience = "agents"
    subject_claim = "oid"
    tenant_claim = "org"
    tenant_mappings = {}
    email_claim = "mail"
    groups_claim = "roles"
  }
`)
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: offlineDeploymentFactories(),
				Steps: []resource.TestStep{
					{Config: config, Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("langsmith_data_plane.test", "id", testDataPlaneID),
						resource.TestCheckResourceAttr("langsmith_data_plane.test", "cloud", "AWS"),
						resource.TestCheckResourceAttr("langsmith_data_plane.test", "status", "active"),
						resource.TestCheckResourceAttr("langsmith_data_plane.test", "api_url", "https://plane.example.com"),
						resource.TestCheckResourceAttr("langsmith_data_plane.test", "vpc_cidr", "10.20.0.0/16"),
						resource.TestCheckResourceAttr("langsmith_data_plane.test", "ttl.short_days", "14"),
						resource.TestCheckResourceAttr("langsmith_data_plane.test", "workspaces.#", "1"),
					)},
					{Config: config, PlanOnly: true, ExpectNonEmptyPlan: false},
					{ResourceName: "langsmith_data_plane.test", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
					{Config: updated, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("langsmith_data_plane.test", plancheck.ResourceActionUpdate)}}, Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("langsmith_data_plane.test", "ttl.enabled", "false"),
						resource.TestCheckResourceAttr("langsmith_data_plane.test", "ttl.short_days", "7"),
						resource.TestCheckResourceAttr("langsmith_data_plane.test", "ttl.long_days", "30"),
						resource.TestCheckResourceAttr("langsmith_data_plane.test", "firewall.allowed_cidrs.%", "0"),
						resource.TestCheckResourceAttr("langsmith_data_plane.test", "fleet_oidc.tenant_mappings.%", "0"),
						resource.TestCheckResourceAttr("langsmith_data_plane.test", "fleet_oidc.subject_claim", "oid"),
						resource.TestCheckResourceAttr("langsmith_data_plane.test", "fleet_oidc.groups_claim", "roles"),
						resource.TestCheckResourceAttr("langsmith_data_plane.test", "status", "active"),
					)},
					{Config: updated, PlanOnly: true, ExpectNonEmptyPlan: false},
					{Config: updated, PreConfig: func() {
						backend.mu.Lock()
						defer backend.mu.Unlock()
						ttl, ok := backend.current["ttl"].(map[string]any)
						if !ok {
							t.Fatal("missing retention settings")
						}
						ttl["short_days"] = float64(8)
					}, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("langsmith_data_plane.test", plancheck.ResourceActionUpdate)}},
						Check: resource.TestCheckResourceAttr("langsmith_data_plane.test", "ttl.short_days", "7")},
					// Removing optional/computed settings stops managing them without clearing defaults.
					{Config: config, PlanOnly: true, ExpectNonEmptyPlan: false},
					{Config: dataPlaneAcceptanceConfig(server.URL, byovpc, `additional_tags = { Team = "platform" }`), PlanOnly: true,
						ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPreRefresh: []plancheck.PlanCheck{plancheck.ExpectResourceAction("langsmith_data_plane.test", plancheck.ResourceActionDestroyBeforeCreate)}}, ExpectNonEmptyPlan: true},
				},
			})
			backend.mu.Lock()
			defer backend.mu.Unlock()
			if backend.creates != 1 || backend.deletes != 1 || backend.updates != 2 {
				t.Fatalf("creates=%d updates=%d deletes=%d", backend.creates, backend.updates, backend.deletes)
			}
		})
	}
}

func TestAccDataPlaneOfflineSettingsOnCreate(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run offline Terraform acceptance tests")
	}
	backend := &dataPlaneContractBackend{t: t}
	server := httptest.NewServer(backend)
	t.Cleanup(server.Close)
	config := dataPlaneAcceptanceConfig(server.URL, false, `ttl = { enabled = false }
  byoiam_enabled = true
  public_load_balancer = true
  eks_api_privatelink_disabled = true
  additional_tags = { Team = "platform", Empty = "" }
`)
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: offlineDeploymentFactories(), Steps: []resource.TestStep{
		{Config: config, Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr("langsmith_data_plane.test", "ttl.enabled", "false"),
			resource.TestCheckResourceAttr("langsmith_data_plane.test", "ttl.short_days", "14"),
			resource.TestCheckResourceAttr("langsmith_data_plane.test", "additional_tags.Team", "platform"),
			resource.TestCheckResourceAttr("langsmith_data_plane.test", "byoiam_enabled", "true"),
		)},
		{Config: config, PlanOnly: true, ExpectNonEmptyPlan: false},
		{ResourceName: "langsmith_data_plane.test", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
	}})
}

func dataPlaneAcceptanceConfig(endpoint string, byovpc bool, settings string) string {
	vpc := `vpc_cidr = "10.20.0.0/16"`
	if byovpc {
		vpc = `byovpc = {
    vpc_id = "vpc-12345678"
    private_app_subnet_ids = ["subnet-11111111", "subnet-22222222"]
    private_db_subnet_ids = ["subnet-33333333", "subnet-44444444"]
  }`
	}
	return fmt.Sprintf(`
provider "langsmith" {
  api_url = %q
  api_key = "offline-test-key"
}
resource "langsmith_data_plane" "test" {
  name = "test-plane"
  region = "us-east-1"
  role_arn = "arn:aws:iam::123456789012:role/test"
  %s
  %s
  timeouts = { create = "5s", update = "5s", delete = "5s" }
}
`, endpoint, vpc, settings)
}

type dataPlaneContractBackend struct {
	t                         *testing.T
	mu                        sync.Mutex
	current                   map[string]any
	creates, updates, deletes int
}

func (b *dataPlaneContractBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path != "/"+dataPlanesPath && r.URL.Path != "/"+dataPlanePath(testDataPlaneID) {
		b.t.Errorf("unexpected API path %s", r.URL.Path)
		w.WriteHeader(404)
		return
	}
	if r.Method != http.MethodPost && b.current == nil {
		w.WriteHeader(404)
		return
	}
	switch r.Method {
	case http.MethodPost:
		b.creates++
		var payload struct {
			Name          string         `json:"name"`
			Region        string         `json:"region"`
			RoleARN       string         `json:"role_arn"`
			VPCCIDR       string         `json:"vpc_cidr"`
			BYOVPC        map[string]any `json:"byovpc"`
			BYOIAM        bool           `json:"byoiam_enabled"`
			Public        bool           `json:"public_load_balancer"`
			NoPrivateLink bool           `json:"eks_api_privatelink_disabled"`
			Tags          []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"additional_tags"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			b.t.Error(err)
			w.WriteHeader(400)
			return
		}
		if err := json.Unmarshal([]byte(dataPlaneFixture), &b.current); err != nil {
			b.t.Error(err)
			return
		}
		p, ok := b.current["provisioning_settings"].(map[string]any)
		if !ok {
			b.t.Error("invalid fixture")
			return
		}
		b.current["name"], b.current["region"] = payload.Name, payload.Region
		p["role_arn"], p["is_byoiam_enabled"], p["is_public_load_balancer"], p["is_eks_api_privatelink_disabled"] = payload.RoleARN, payload.BYOIAM, payload.Public, payload.NoPrivateLink
		if payload.BYOVPC != nil {
			if payload.VPCCIDR != "" {
				b.t.Error("BYOVPC request included vpc_cidr")
			}
			p["byovpc"] = payload.BYOVPC
		} else {
			p["vpc_cidr"] = payload.VPCCIDR
		}
		tags := map[string]string{}
		for _, tag := range payload.Tags {
			tags[tag.Key] = tag.Value
		}
		p["additional_tags"] = tags
		b.current["status"] = "requested"
		w.WriteHeader(202)
	case http.MethodGet:
		b.current["status"] = "active"
	case http.MethodPatch:
		b.updates++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			b.t.Error(err)
			return
		}
		for key, value := range payload {
			switch key {
			case "maintenance_window":
				b.current[key] = value
			case "ttl", "firewall", "fleet_oidc":
				changes, ok := value.(map[string]any)
				if !ok {
					b.t.Errorf("invalid settings %v", value)
					return
				}
				current, ok := b.current[key].(map[string]any)
				if !ok {
					b.t.Errorf("invalid fixture %s", key)
					return
				}
				for field, v := range changes {
					current[field] = v
				}
			default:
				b.t.Errorf("unexpected PATCH field %s", key)
			}
		}
		b.current["status"] = "updating"
	case http.MethodDelete:
		b.deletes++
		b.current = nil
		w.WriteHeader(202)
		return
	default:
		b.t.Errorf("unexpected method %s", r.Method)
		w.WriteHeader(405)
		return
	}
	if err := json.NewEncoder(w).Encode(b.current); err != nil {
		b.t.Error(err)
	}
}
