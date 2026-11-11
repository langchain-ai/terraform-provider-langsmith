// Live agent/deployment lifecycle tests, skipped by default.
// Requires a test workspace and a connected GitHub repository configured with
// TEST_LANGSMITH_GITHUB_INTEGRATION_ID and TEST_LANGSMITH_GITHUB_REPO_URL.
//
//	LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 \
//	  go test ./internal/provider -run '^TestAccDeploymentAgentTargetLive$' -count=1 -v

package provider

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	frameworkprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	frameworkresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/config"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestAccDeploymentAgentTargetLive(t *testing.T) {
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 TF_ACC=1 to run the live deployment test")
	}
	for _, name := range []string{"LANGSMITH_ENDPOINT", "LANGSMITH_API_KEY", "LANGSMITH_WORKSPACE_ID", "TEST_LANGSMITH_GITHUB_INTEGRATION_ID", "TEST_LANGSMITH_GITHUB_REPO_URL"} {
		if os.Getenv(name) == "" {
			t.Fatalf("%s must be set", name)
		}
	}
	t.Setenv(resource.EnvTfAccProviderNamespace, "langchain-ai")
	endpoint, workspaceID := os.Getenv("LANGSMITH_ENDPOINT"), os.Getenv("LANGSMITH_WORKSPACE_ID")
	client := langsmith.NewClient(option.WithBaseURL(resolveAPIURL(endpoint)), option.WithTenantID(workspaceID), option.WithAPIKey(os.Getenv("LANGSMITH_API_KEY")))
	agentID := fmt.Sprintf("tf-acc-agent-%d", time.Now().UnixNano())
	t.Logf("Test agent: %s", agentID)
	hcl := `
terraform {
  required_providers {
    langsmith = {
      source = "langchain-ai/langsmith"
    }
  }
}

variable "api_url" {
  type = string
}

variable "workspace_id" {
  type = string
}

variable "agent_id" {
  type = string
}

variable "github_integration_id" {
  type = string
}

variable "github_repo_url" {
  type = string
}

variable "github_repo_ref" {
  type = string
}

variable "langgraph_config_path" {
  type = string
}

variable "production_by_name" {
  type    = bool
  default = true
}

variable "staging_log_level" {
  type    = string
  default = "info"
}

provider "langsmith" {
  api_url      = var.api_url
  workspace_id = var.workspace_id
}

resource "langsmith_agent" "test" {
  agent_id    = var.agent_id
  source_type = "LSD"
}

data "langsmith_agent" "test" {
  agent_id = langsmith_agent.test.id
}
`
	deployment := func(environment string) string {
		return fmt.Sprintf(`
resource "langsmith_deployment" %q {
  name = %q == "production" && var.production_by_name ? langsmith_agent.test.id : null
  agent = %q == "production" && var.production_by_name ? null : {
    agent_id    = langsmith_agent.test.id
    environment = %q
  }
  source = "github"

  source_config = {
    integration_id  = var.github_integration_id
    repo_url        = var.github_repo_url
    deployment_type = "dev"
    build_on_push   = false
  }

  source_revision_config = {
    repo_ref              = var.github_repo_ref
    langgraph_config_path = var.langgraph_config_path
  }

  environment_variables = {
    LOG_LEVEL = %q == "staging" ? var.staging_log_level : "info"
  }
}
`, environment, environment, environment, environment, environment)
	}
	stagingConfig := hcl + deployment("staging")
	bothConfig := stagingConfig + deployment("production")
	productionConfig := hcl + deployment("production")
	variables := config.Variables{
		"api_url":               config.StringVariable(endpoint),
		"workspace_id":          config.StringVariable(workspaceID),
		"agent_id":              config.StringVariable(agentID),
		"github_integration_id": config.StringVariable(os.Getenv("TEST_LANGSMITH_GITHUB_INTEGRATION_ID")),
		"github_repo_url":       config.StringVariable(os.Getenv("TEST_LANGSMITH_GITHUB_REPO_URL")),
		"github_repo_ref":       config.StringVariable(firstNonEmpty(os.Getenv("TEST_LANGSMITH_GITHUB_REPO_REF"), "main")),
		"langgraph_config_path": config.StringVariable(firstNonEmpty(os.Getenv("TEST_LANGSMITH_LANGGRAPH_CONFIG_PATH"), "langgraph.json")),
	}
	deploymentIDs, revisionIDs, tracingProjects := map[string]string{}, map[string]string{}, map[string]string{}
	checkAgent := resource.ComposeAggregateTestCheckFunc(
		resource.TestCheckResourceAttr("langsmith_agent.test", "environments.%", "4"),
		resource.TestCheckResourceAttrPair("langsmith_agent.test", "environments.STAGING.tracer_session_id", "data.langsmith_agent.test", "environments.STAGING.tracer_session_id"),
		func(state *terraform.State) error {
			entry, ok := state.RootModule().Resources["langsmith_agent.test"]
			if !ok || entry.Primary == nil {
				return fmt.Errorf("agent is missing from Terraform state")
			}
			attributes := entry.Primary.Attributes
			for _, environment := range []string{"LOCAL", "DEVELOPMENT", "STAGING", "PRODUCTION"} {
				id := attributes["environments."+environment+".tracer_session_id"]
				if id == "" || tracingProjects[environment] != "" && tracingProjects[environment] != id {
					return fmt.Errorf("%s tracing project was missing or replaced", environment)
				}
				tracingProjects[environment] = id
			}
			return nil
		},
	)
	checkDeployment := func(environment, logLevel string, revisionChanged bool) resource.TestCheckFunc {
		return func(state *terraform.State) error {
			address := "langsmith_deployment." + environment
			entry, ok := state.RootModule().Resources[address]
			if !ok || entry.Primary == nil {
				return fmt.Errorf("%s is missing", address)
			}
			id, attributes := entry.Primary.ID, entry.Primary.Attributes
			if previous := deploymentIDs[environment]; previous != "" && previous != id {
				return fmt.Errorf("%s deployment was replaced", environment)
			}
			deploymentIDs[environment] = id
			var result struct {
				Resources []deploymentAPI `json:"resources"`
			}
			query := url.Values{"deployment_ids": {id}, "agent_id": {agentID}, "agent_environment": {environment}}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			if err := client.Get(ctx, deploymentsPath+"?"+query.Encode(), nil, &result); err != nil {
				return err
			}
			if len(result.Resources) != 1 || result.Resources[0].ID != id {
				return fmt.Errorf("deployment %s does not belong to agent %s %s", id, agentID, environment)
			}
			deployment := result.Resources[0]
			if err := client.Get(ctx, deploymentPath(id), nil, &deployment); err != nil {
				return err
			}
			revisionID := attributes["latest_revision_id"]
			if deployment.TenantID != workspaceID || attributes["workspace_id"] != workspaceID || deployment.LatestRevisionID == nil || *deployment.LatestRevisionID != revisionID || deployment.ActiveRevisionID == nil || *deployment.ActiveRevisionID != revisionID {
				return fmt.Errorf("%s workspace or active revision does not match Terraform", environment)
			}
			if !slices.ContainsFunc(deployment.Secrets, func(value deploymentSecretAPI) bool {
				return value.Name == "LOG_LEVEL" && value.Value != nil && *value.Value == logLevel
			}) {
				return fmt.Errorf("%s LOG_LEVEL does not match the configured value", environment)
			}
			var revision deploymentResourceRevisionAPI
			if err := client.Get(ctx, deploymentRevisionPath(id, revisionID), nil, &revision); err != nil {
				return err
			}
			if revision.Status != "DEPLOYED" || attributes["latest_revision_status"] != "DEPLOYED" || attributes["source_config.deployment_type"] != "dev" || attributes["environment_variables.LOG_LEVEL"] != logLevel {
				return fmt.Errorf("%s deployment did not reach the configured state", environment)
			}
			if previous := revisionIDs[environment]; previous != "" && (previous != revisionID) != revisionChanged {
				return fmt.Errorf("%s revision change: expected %t", environment, revisionChanged)
			}
			revisionIDs[environment] = revisionID
			return nil
		}
	}
	staging, production := "langsmith_deployment.staging", "langsmith_deployment.production"
	steps := []resource.TestStep{
		{Config: stagingConfig, PreConfig: func() { t.Log("Create staging") }, Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(staging, "agent.agent_id", agentID),
			resource.TestCheckResourceAttr(staging, "agent.environment", "staging"),
			checkDeployment("staging", "info", false),
		)},
		{Config: stagingConfig, PreConfig: func() { t.Log("Update staging without replacing the deployment") },
			ConfigVariables:  config.Variables{"staging_log_level": config.StringVariable("debug")},
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(staging, plancheck.ResourceActionUpdate)}},
			Check:            checkDeployment("staging", "debug", true)},
		{Config: bothConfig, PreConfig: func() { t.Log("Create name-based production without changing staging") },
			ConfigVariables:  config.Variables{"staging_log_level": config.StringVariable("debug")},
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(staging, plancheck.ResourceActionNoop), plancheck.ExpectResourceAction(production, plancheck.ResourceActionCreate)}},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(production, "name", agentID),
				checkDeployment("staging", "debug", false), checkDeployment("production", "info", false),
			)},
		{Config: bothConfig, PreConfig: func() { t.Log("Adopt the matching production target without replacing the deployment") },
			ConfigVariables:  config.Variables{"staging_log_level": config.StringVariable("debug"), "production_by_name": config.BoolVariable(false)},
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(production, plancheck.ResourceActionUpdate)}},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(production, "agent.agent_id", agentID),
				resource.TestCheckResourceAttr(production, "agent.environment", "production"),
				checkDeployment("staging", "debug", false), checkDeployment("production", "info", false),
			)},
		{Config: productionConfig, PreConfig: func() { t.Log("Delete staging without changing production") },
			ConfigVariables:  config.Variables{"staging_log_level": config.StringVariable("debug"), "production_by_name": config.BoolVariable(false)},
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(staging, plancheck.ResourceActionDestroy), plancheck.ExpectResourceAction(production, plancheck.ResourceActionNoop)}},
			Check: resource.ComposeAggregateTestCheckFunc(checkDeployment("production", "info", false), func(*terraform.State) error {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				var result deploymentAPI
				if err := client.Get(ctx, deploymentPath(deploymentIDs["staging"]), nil, &result); !isLangSmithNotFound(err) {
					return fmt.Errorf("staging deployment was not confirmed deleted: %v", err)
				}
				return nil
			})},
	}
	var lifecycle []resource.TestStep
	for _, step := range steps {
		inputs := maps.Clone(variables)
		maps.Copy(inputs, step.ConfigVariables)
		step.ConfigVariables = inputs
		step.Check = resource.ComposeAggregateTestCheckFunc(checkAgent, step.Check)
		lifecycle = append(lifecycle, step, resource.TestStep{Config: step.Config, ConfigVariables: inputs, PlanOnly: true, ExpectNonEmptyPlan: false})
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(deploymentLiveTestProvider{Provider: New("test")(), waitTimeout: 5 * time.Minute}),
		},
		CheckDestroy: func(state *terraform.State) error {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			ids := map[string]bool{}
			for _, id := range deploymentIDs {
				ids[id] = true
			}
			for _, entry := range state.RootModule().Resources {
				if entry.Type == "langsmith_deployment" && entry.Primary != nil {
					ids[entry.Primary.ID] = true
				}
			}
			if _, err := client.Agents.Get(ctx, agentID, langsmith.AgentGetParams{}); !isAgentNotFound(err) {
				return fmt.Errorf("agent %s was not confirmed deleted: %v", agentID, err)
			}
			for id := range ids {
				var result deploymentAPI
				if err := client.Get(ctx, deploymentPath(id), nil, &result); !isLangSmithNotFound(err) {
					return fmt.Errorf("deployment %s was not confirmed deleted: %v", id, err)
				}
			}
			return nil
		},
		Steps: lifecycle,
	})
}

type deploymentLiveTestProvider struct {
	frameworkprovider.Provider
	waitTimeout time.Duration
}

func (p deploymentLiveTestProvider) Resources(context.Context) []func() frameworkresource.Resource {
	return []func() frameworkresource.Resource{
		NewAgentResource,
		func() frameworkresource.Resource { return &DeploymentResource{waitTimeout: p.waitTimeout} },
	}
}
