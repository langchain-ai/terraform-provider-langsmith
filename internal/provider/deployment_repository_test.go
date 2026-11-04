package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func githubDeploymentModel(repoURL, ref, configPath string) deploymentResourceModel {
	return deploymentResourceModel{
		ID: types.StringValue(testDeploymentID), Name: types.StringValue("agent"), Source: types.StringValue("github"),
		DisplayName: types.StringNull(),
		SourceConfig: &deploymentSourceConfigModel{
			IntegrationID: types.StringValue("integration"), RepoURL: types.StringValue(repoURL),
			DeploymentType: types.StringValue("prod"), BuildOnPush: types.BoolValue(true),
		},
		SourceRevisionConfig: &sourceRevisionConfigModel{RepoRef: types.StringValue(ref), LanggraphConfigPath: types.StringValue(configPath)},
		EnvironmentVariables: types.MapNull(types.StringType),
		Secrets:              types.MapNull(types.StringType),
	}
}

type recordedRequest struct {
	method string
	path   string
	body   map[string]any
}

// githubDeploymentServer answers like the v2 API for a GitHub deployment and records
// every write the provider sends.
func githubDeploymentServer(t *testing.T) (*httptest.Server, func() []recordedRequest) {
	t.Helper()
	var mu sync.Mutex
	var writes []recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.Method != http.MethodGet {
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			mu.Lock()
			writes = append(writes, recordedRequest{req.Method, req.URL.Path, body})
			mu.Unlock()
		}
		if strings.Contains(req.URL.Path, "/revisions") {
			_, _ = w.Write([]byte(`{"id":"` + testRevisionID + `","status":"DEPLOYED"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"` + testDeploymentID + `","name":"agent","source":"github","source_config":{"integration_id":"integration","repo_url":"https://github.com/example/monorepo","deployment_type":"prod","build_on_push":true},"source_revision_config":{"repo_ref":"agent-prod","langgraph_config_path":"agent/langgraph.json"},"status":"READY","latest_revision_id":"` + testRevisionID + `","active_revision_id":"` + testRevisionID + `"}`))
	}))
	return server, func() []recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedRequest(nil), writes...)
	}
}

func TestDeploymentRepositoryChangeIsAnUpdateNotAReplacement(t *testing.T) {
	for _, name := range []string{"repo_url", "integration_id"} {
		attribute, ok := sourceConfigSchema()[name].(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s is not a string attribute", name)
		}
		for _, modifier := range attribute.PlanModifiers {
			if strings.Contains(modifier.Description(context.Background()), "destroy and recreate") {
				t.Fatalf("%s still replaces the deployment", name)
			}
		}
	}
}

func TestDeploymentRepositoryChangeCarriesTheRevisionOnOnePatch(t *testing.T) {
	server, writes := githubDeploymentServer(t)
	defer server.Close()
	state := githubDeploymentModel("https://github.com/example/agent", "prod", "langgraph.json")
	plan := githubDeploymentModel("https://github.com/example/monorepo", "agent-prod", "agent/langgraph.json")

	if _, err := testDeploymentResource(server).update(context.Background(), state, plan); err != nil {
		t.Fatal(err)
	}
	got := writes()
	if len(got) != 1 || got[0].method != http.MethodPatch || got[0].path != "/v2/deployments/"+testDeploymentID {
		t.Fatalf("writes = %#v, want one PATCH of the deployment", got)
	}
	sourceConfig, _ := got[0].body["source_config"].(map[string]any)
	if sourceConfig["repo_url"] != "https://github.com/example/monorepo" || sourceConfig["build_on_push"] != true {
		t.Fatalf("source_config = %#v", sourceConfig)
	}
	// The configured integration goes with the move, so the API checks it reaches the new repository.
	if sourceConfig["integration_id"] != "integration" {
		t.Fatalf("configured integration_id was not sent with the move: %#v", sourceConfig)
	}
	revision, _ := got[0].body["source_revision_config"].(map[string]any)
	if revision["repo_ref"] != "agent-prod" || revision["langgraph_config_path"] != "agent/langgraph.json" {
		t.Fatalf("source_revision_config = %#v", revision)
	}
}

func TestDeploymentRepositoryOnlyChangeCreatesNoRevision(t *testing.T) {
	server, writes := githubDeploymentServer(t)
	defer server.Close()
	state := githubDeploymentModel("https://github.com/example/agent", "agent-prod", "agent/langgraph.json")
	plan := githubDeploymentModel("https://github.com/example/monorepo", "agent-prod", "agent/langgraph.json")
	plan.SourceConfig.IntegrationID = types.StringValue("other-integration")

	if _, err := testDeploymentResource(server).update(context.Background(), state, plan); err != nil {
		t.Fatal(err)
	}
	got := writes()
	if len(got) != 1 || got[0].method != http.MethodPatch {
		t.Fatalf("writes = %#v, want one PATCH", got)
	}
	want := map[string]any{"repo_url": "https://github.com/example/monorepo", "integration_id": "other-integration"}
	sourceConfig, _ := got[0].body["source_config"].(map[string]any)
	if len(sourceConfig) != len(want) || sourceConfig["repo_url"] != want["repo_url"] || sourceConfig["integration_id"] != want["integration_id"] {
		t.Fatalf("source_config = %#v, want %#v", sourceConfig, want)
	}
	if _, ok := got[0].body["source_revision_config"]; ok {
		t.Fatalf("a repository-only change sent revision inputs: %#v", got[0].body)
	}
}
