package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/config"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

func TestAccDeploymentOfflineAutomaticSecrets(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the offline Terraform acceptance test")
	}
	backend := newDeploymentContractBackend(t)
	server := httptest.NewServer(backend)
	defer server.Close()
	hcl := fmt.Sprintf(`
provider "langsmith" {
  control_plane_url = %q
  api_key = "offline-test-key"
  workspace_id = "offline-workspace"
}
variable "environment" {
  type = map(string)
  sensitive = true
  ephemeral = true
}
resource "langsmith_deployment" "test" {
  name = "offline-agent"
  source = "external_docker"
  source_config = { resource_spec = {} }
  source_revision_config = { image_uri = "registry.example.com/agent:v1" }
  secrets = var.environment
}`, server.URL+"/api-host")
	variables := func(value string) config.Variables {
		return config.Variables{"environment": config.MapVariable(map[string]config.Variable{"API_TOKEN": config.StringVariable(value)})}
	}
	drift := func() {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		backend.secrets = []any{map[string]any{"name": "API_TOKEN", "value": offlineSecretOne}}
	}
	checks := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{deploymentPlanExcludesSecrets{}}}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: offlineDeploymentFactories(),
		Steps: []resource.TestStep{
			{Config: hcl, ConfigVariables: variables(offlineSecretOne), ConfigPlanChecks: checks, Check: deploymentStateExcludesSecrets},
			{Config: hcl, ConfigVariables: variables(offlineSecretTwo), ConfigPlanChecks: checks,
				Check: resource.ComposeAggregateTestCheckFunc(backend.expectRevisionPosts(1), deploymentStateExcludesSecrets)},
			{Config: hcl, ConfigVariables: variables(offlineSecretTwo), PlanOnly: true, ExpectNonEmptyPlan: false},
			{PreConfig: drift, Config: hcl, ConfigVariables: variables(offlineSecretTwo), ConfigPlanChecks: checks,
				Check: resource.ComposeAggregateTestCheckFunc(backend.expectRevisionPosts(2), deploymentStateExcludesSecrets)},
			{PreConfig: drift, Config: strings.Replace(hcl, "  secrets = var.environment", "", 1), ConfigVariables: variables(offlineSecretTwo),
				PlanOnly: true, ExpectNonEmptyPlan: false},
		},
	})
}

type deploymentPlanExcludesSecrets struct{}

func (deploymentPlanExcludesSecrets) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	encoded, err := json.Marshal(req.Plan)
	if err != nil {
		resp.Error = err
		return
	}
	for _, secret := range []string{offlineSecretOne, offlineSecretTwo} {
		if strings.Contains(string(encoded), secret) {
			resp.Error = fmt.Errorf("ephemeral environment value found in Terraform plan JSON")
			return
		}
	}
}
