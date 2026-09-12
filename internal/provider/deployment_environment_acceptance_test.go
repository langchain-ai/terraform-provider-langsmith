package provider

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/config"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccDeploymentOfflineSplitEnvironment(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the offline Terraform acceptance test")
	}
	backend := newDeploymentContractBackend(t)
	backend.revisionSecret = ""
	server := httptest.NewServer(backend)
	defer server.Close()
	hcl := func(environment, secrets string) string {
		return deploymentEnvironmentAcceptanceConfig(server.URL, environment, secrets)
	}
	public := `environment_variables = { LOG_LEVEL = "info" }`
	secret := `secrets = var.environment`
	check := func(revisions int, environment map[string]string) resource.TestCheckFunc {
		return resource.ComposeAggregateTestCheckFunc(
			backend.expectRevisionPosts(revisions), deploymentStateExcludesSecrets,
			func(*terraform.State) error {
				backend.mu.Lock()
				defer backend.mu.Unlock()
				actual := map[string]string{}
				for _, entry := range backend.secrets {
					item, ok := entry.(map[string]any)
					if !ok {
						return fmt.Errorf("API environment entry is not an object")
					}
					name, nameOK := item["name"].(string)
					value, valueOK := item["value"].(string)
					if !nameOK || !valueOK {
						return fmt.Errorf("API environment entry must have string name and value")
					}
					actual[name] = value
				}
				if !reflect.DeepEqual(actual, environment) {
					return fmt.Errorf("API environment does not match desired public and secret entries")
				}
				return nil
			},
		)
	}
	plans := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{deploymentPlanExcludesSecrets{}}}
	drift := func() {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		backend.secrets = []any{
			map[string]any{"name": "LOG_LEVEL", "value": "warn"},
			map[string]any{"name": "API_TOKEN", "value": offlineSecretOne},
			map[string]any{"name": "UNDECLARED_TOKEN", "value": offlineSecretOne},
		}
	}
	testCase := resource.TestCase{
		ProtoV6ProviderFactories: offlineDeploymentFactories(),
		Steps: []resource.TestStep{
			{Config: hcl(public, secret), ConfigPlanChecks: plans,
				Check: resource.ComposeAggregateTestCheckFunc(check(0, map[string]string{"LOG_LEVEL": "info", "API_TOKEN": offlineSecretOne}),
					resource.TestCheckResourceAttr("langsmith_deployment.test", "environment_variables.LOG_LEVEL", "info"))},
			{Config: hcl(public, secret), PlanOnly: true, ExpectNonEmptyPlan: false},
			{Config: hcl(public, secret), ConfigPlanChecks: plans,
				Check: check(1, map[string]string{"LOG_LEVEL": "info", "API_TOKEN": offlineSecretTwo})},
			{Config: hcl(`environment_variables = { LOG_LEVEL = "debug" }`, secret), ConfigPlanChecks: plans,
				Check: check(2, map[string]string{"LOG_LEVEL": "debug", "API_TOKEN": offlineSecretTwo})},
			{PreConfig: drift, Config: hcl(`environment_variables = { LOG_LEVEL = "debug" }`, secret),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					deploymentPlanExcludesSecrets{}, deploymentPublicEnvironmentPlan{before: "warn", after: "debug"},
				}},
				Check: check(3, map[string]string{"LOG_LEVEL": "debug", "API_TOKEN": offlineSecretTwo})},
			{Config: hcl(`environment_variables = { LOG_LEVEL = "debug" }`, `secrets = {}`), ConfigPlanChecks: plans,
				Check: check(4, map[string]string{"LOG_LEVEL": "debug"})},
			{Config: hcl(`environment_variables = { LOG_LEVEL = "debug" }`, ""),
				PlanOnly: true, ExpectNonEmptyPlan: false},
			{Config: hcl(`environment_variables = {}`, ""), ExpectError: regexp.MustCompile("does not clear secrets")},
			{Config: hcl("", ""), ConfigPlanChecks: plans,
				Check: check(4, map[string]string{"LOG_LEVEL": "debug"})},
		},
	}
	for i := range testCase.Steps {
		value := offlineSecretOne
		if i >= 2 {
			value = offlineSecretTwo
		}
		testCase.Steps[i].ConfigVariables = deploymentEnvironmentTestVariables(value)
	}
	resource.Test(t, testCase)
}

func TestAccDeploymentOfflineSplitEnvironmentImport(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the offline Terraform acceptance test")
	}
	backend := newDeploymentContractBackend(t)
	backend.created = true
	backend.image = "registry.example.com/agent:v1"
	backend.latestRevision = offlineRevisionOne
	backend.secrets = []any{
		map[string]any{"name": "LOG_LEVEL", "value": "info"},
		map[string]any{"name": "API_TOKEN", "value": offlineSecretOne},
	}
	server := httptest.NewServer(backend)
	defer server.Close()
	hcl := strings.Replace(deploymentEnvironmentAcceptanceConfig(server.URL,
		`environment_variables = { LOG_LEVEL = "info" }`, `secrets = var.environment`),
		"resource_spec = {}", "resource_spec = { min_scale = 1, max_scale = 1, cpu = 1, memory_mb = 2048 }", 1)
	variables := deploymentEnvironmentTestVariables(offlineSecretOne)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: offlineDeploymentFactories(),
		Steps: []resource.TestStep{
			{Config: hcl, ConfigVariables: variables, ResourceName: "langsmith_deployment.test", ImportState: true,
				ImportStateId: offlineDeploymentID, ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					for _, state := range states {
						for name, value := range state.Attributes {
							if strings.HasPrefix(name, "environment_variables.") || strings.Contains(value, offlineSecretOne) {
								return fmt.Errorf("import copied unclassified API environment into Terraform state")
							}
						}
					}
					return nil
				}},
			{Config: hcl, ConfigVariables: variables, PlanOnly: true, ExpectNonEmptyPlan: true},
			{Config: hcl, ConfigVariables: variables,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{deploymentPlanExcludesSecrets{}}},
				Check: resource.ComposeAggregateTestCheckFunc(backend.expectRevisionPosts(0), deploymentStateExcludesSecrets,
					resource.TestCheckResourceAttr("langsmith_deployment.test", "environment_variables.LOG_LEVEL", "info"))},
			{Config: hcl, ConfigVariables: variables, PlanOnly: true, ExpectNonEmptyPlan: false},
		},
	})
}

func TestAccDeploymentOfflineSplitEnvironmentUnknown(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the offline Terraform acceptance test")
	}
	backend := newDeploymentContractBackend(t)
	backend.revisionSecret = offlineSecretOne
	server := httptest.NewServer(backend)
	defer server.Close()
	hcl := func(level string) string {
		return deploymentEnvironmentAcceptanceConfig(server.URL, `environment_variables = terraform_data.environment.output`, `secrets = var.environment`) +
			fmt.Sprintf(`
resource "terraform_data" "environment" { input = { LOG_LEVEL = %q } }
`, level)
	}
	checks := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{deploymentPlanExcludesSecrets{}}}
	variables := deploymentEnvironmentTestVariables(offlineSecretOne)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: offlineDeploymentFactories(),
		Steps: []resource.TestStep{
			{Config: hcl("info"), ConfigVariables: variables, ConfigPlanChecks: checks,
				Check: resource.TestCheckResourceAttr("langsmith_deployment.test", "environment_variables.LOG_LEVEL", "info")},
			{Config: hcl("debug"), ConfigVariables: variables, ConfigPlanChecks: checks,
				Check: resource.ComposeAggregateTestCheckFunc(backend.expectRevisionPosts(1), deploymentStateExcludesSecrets,
					resource.TestCheckResourceAttr("langsmith_deployment.test", "environment_variables.LOG_LEVEL", "debug"))},
			{Config: hcl("debug"), ConfigVariables: variables, PlanOnly: true, ExpectNonEmptyPlan: false},
		},
	})
}

func TestAccDeploymentOfflineSplitEnvironmentIgnoreChanges(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the offline Terraform acceptance test")
	}
	backend := newDeploymentContractBackend(t)
	server := httptest.NewServer(backend)
	defer server.Close()
	hcl := deploymentEnvironmentAcceptanceConfig(server.URL,
		`environment_variables = { LOG_LEVEL = "info" }
  lifecycle { ignore_changes = [environment_variables] }`, `secrets = var.environment`)
	drift := func() {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		backend.secrets = []any{
			map[string]any{"name": "LOG_LEVEL", "value": "debug"},
			map[string]any{"name": "API_TOKEN", "value": offlineSecretOne},
		}
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: offlineDeploymentFactories(),
		Steps: []resource.TestStep{
			{Config: hcl, ConfigVariables: deploymentEnvironmentTestVariables(offlineSecretOne)},
			{PreConfig: drift, Config: hcl, ConfigVariables: deploymentEnvironmentTestVariables(offlineSecretOne),
				PlanOnly: true, ExpectNonEmptyPlan: false},
			{Config: hcl, ConfigVariables: deploymentEnvironmentTestVariables(offlineSecretTwo),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{deploymentPlanExcludesSecrets{}}},
				Check: resource.ComposeAggregateTestCheckFunc(backend.expectRevisionPosts(1), deploymentStateExcludesSecrets,
					resource.TestCheckResourceAttr("langsmith_deployment.test", "environment_variables.LOG_LEVEL", "debug"))},
			{Config: hcl, ConfigVariables: deploymentEnvironmentTestVariables(offlineSecretTwo), PlanOnly: true, ExpectNonEmptyPlan: false},
		},
	})
}

func deploymentEnvironmentAcceptanceConfig(serverURL, environment, secrets string) string {
	return fmt.Sprintf(`
provider "langsmith" {
  api_url = %q
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
  %s
  %s
}`, serverURL+"/api/v1", environment, secrets)
}

func deploymentEnvironmentTestVariables(secret string) config.Variables {
	return config.Variables{"environment": config.MapVariable(map[string]config.Variable{"API_TOKEN": config.StringVariable(secret)})}
}

type deploymentPublicEnvironmentPlan struct{ before, after string }

func (check deploymentPublicEnvironmentPlan) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	for _, change := range req.Plan.ResourceChanges {
		if change.Address != "langsmith_deployment.test" {
			continue
		}
		beforeState, beforeOK := change.Change.Before.(map[string]any)
		afterState, afterOK := change.Change.After.(map[string]any)
		if !beforeOK || !afterOK {
			resp.Error = fmt.Errorf("public environment plan must have before and after objects")
			return
		}
		before := beforeState["environment_variables"]
		after := afterState["environment_variables"]
		if !reflect.DeepEqual(before, map[string]any{"LOG_LEVEL": check.before}) ||
			!reflect.DeepEqual(after, map[string]any{"LOG_LEVEL": check.after}) {
			resp.Error = fmt.Errorf("public environment drift was not visible in the Terraform plan")
		}
		return
	}
	resp.Error = fmt.Errorf("deployment was missing from the Terraform plan")
}
