package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestDeploymentTargetValidation(t *testing.T) {
	ctx := context.Background()
	r := &DeploymentResource{}
	var schema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schema)
	for _, tc := range []struct {
		name                     string
		named, targeted, unknown bool
		wantError                bool
	}{
		{"name", true, false, false, false},
		{"agent", false, true, false, false},
		{"unknown agent", false, true, true, false},
		{"both", true, true, false, true},
		{"neither", false, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := testDeploymentModel()
			model.SourceConfig.ResourceSpec = nil
			if !tc.named {
				model.Name = types.StringNull()
			}
			if tc.targeted {
				model.Agent = deploymentAgentObject(deploymentAgentAPI{"helper", "staging"})
			}
			if tc.unknown {
				model.Agent = types.ObjectUnknown(deploymentAgentTypes)
			}
			state := tfsdk.State{Schema: schema.Schema}
			if diagnostics := state.Set(ctx, model); diagnostics.HasError() {
				t.Fatal(diagnostics)
			}
			var response resource.ValidateConfigResponse
			for _, validator := range r.ConfigValidators(ctx) {
				validator.ValidateResource(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: schema.Schema, Raw: state.Raw}}, &response)
			}
			if response.Diagnostics.HasError() != tc.wantError {
				t.Fatal(response.Diagnostics)
			}
		})
	}
}

func TestDeploymentTargetAdoptionVerification(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		wantError  bool
	}{
		{"mismatch", `{"resources":[]}`, http.StatusOK, false},
		{"forbidden", `{}`, http.StatusForbidden, true},
		{"invalid response", `{}`, http.StatusOK, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet {
					writes.Add(1)
				}
				query := req.URL.Query()
				if req.URL.Path != "/v2/deployments" || query.Get("deployment_ids") != testDeploymentID || query.Get("agent_id") != "orders" || query.Get("agent_environment") != "production" || req.Header.Get("X-Tenant-Id") != "original-workspace" {
					t.Errorf("unexpected verification request: %s %s", req.Method, req.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			state := testDeploymentModel()
			state.SourceConfig.ResourceSpec = nil
			state.ID = types.StringValue(testDeploymentID)
			state.WorkspaceID = types.StringValue("original-workspace")
			plan := state
			plan.Agent = deploymentAgentObject(deploymentAgentAPI{"orders", "production"})
			plan.DisplayName = types.StringValue("Updated")
			plan.SourceRevisionConfig = &sourceRevisionConfigModel{ImageURI: types.StringValue("registry.example.com/orders:v2")}
			r := testDeploymentResource(server)
			ctx := context.Background()
			var schema resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &schema)
			prior := tfsdk.State{Schema: schema.Schema}
			planned := tfsdk.Plan{Schema: schema.Schema}
			if diagnostics := prior.Set(ctx, state); diagnostics.HasError() {
				t.Fatal(diagnostics)
			}
			if diagnostics := planned.Set(ctx, plan); diagnostics.HasError() {
				t.Fatal(diagnostics)
			}
			var response resource.ModifyPlanResponse
			r.modifyAgentTargetPlan(ctx, resource.ModifyPlanRequest{State: prior, Plan: planned}, &response)
			wantReplacement := 0
			if !tc.wantError {
				wantReplacement = 1
			}
			if response.Diagnostics.HasError() != tc.wantError || len(response.RequiresReplace) != wantReplacement {
				t.Fatalf("unexpected target plan: diagnostics=%v replacement=%v", response.Diagnostics, response.RequiresReplace)
			}
			got, err := r.update(ctx, state, plan)
			if err == nil || writes.Load() != 0 || !got.Agent.IsNull() || !got.ID.Equal(state.ID) || !got.DisplayName.Equal(state.DisplayName) {
				t.Fatalf("failed verification must preserve the deployment: err=%v writes=%d", err, writes.Load())
			}
		})
	}
}

func TestDeploymentTargetWaitsForEnvironmentCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, agentID, environment, workspace string
		remaining                             int
	}{
		{"both deletion phases", "helper", "staging", "offline-workspace", 0},
		{"other agent", "other-agent", "staging", "offline-workspace", 2},
		{"other environment", "helper", "production", "offline-workspace", 2},
		{"other workspace", "helper", "staging", "other-workspace", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := newDeploymentContractBackend(t)
			backend.deleteRemaining = 2
			backend.deletingAgent = &deploymentAgentAPI{"helper", "staging"}
			backend.deletingWorkspace = "offline-workspace"
			server := httptest.NewServer(backend)
			defer server.Close()
			r := testDeploymentResource(server)
			r.client = langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("offline-test-key"))
			if err := r.waitForAgentEnvironment(context.Background(), deploymentAgentAPI{tc.agentID, tc.environment}, types.StringValue(tc.workspace)); err != nil {
				t.Fatal(err)
			}
			if backend.deleteRemaining != tc.remaining {
				t.Fatalf("remaining cleanup phases = %d, want %d", backend.deleteRemaining, tc.remaining)
			}
		})
	}
}

func TestDeploymentTargetDeleteLeavesCleanupInBackground(t *testing.T) {
	for _, targeted := range []bool{false, true} {
		t.Run(fmt.Sprintf("targeted=%t", targeted), func(t *testing.T) {
			backend := newDeploymentContractBackend(t)
			backend.deletePolls = 2
			backend.created = true
			backend.agent = &deploymentAgentAPI{"helper", "staging"}
			server := httptest.NewServer(backend)
			defer server.Close()
			r := testDeploymentResource(server)
			r.client = langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("offline-test-key"))
			model := testDeploymentModel()
			model.SourceConfig.ResourceSpec = nil
			model.ID = types.StringValue(offlineDeploymentID)
			model.WorkspaceID = types.StringValue("offline-workspace")
			if targeted {
				model.Agent = deploymentAgentObject(*backend.agent)
			}
			var schema resource.SchemaResponse
			r.Schema(context.Background(), resource.SchemaRequest{}, &schema)
			state := tfsdk.State{Schema: schema.Schema}
			if diagnostics := state.Set(context.Background(), model); diagnostics.HasError() {
				t.Fatal(diagnostics)
			}
			var response resource.DeleteResponse
			r.Delete(context.Background(), resource.DeleteRequest{State: state}, &response)
			if response.Diagnostics.HasError() || !backend.deleted || backend.deleteRemaining != 2 {
				t.Fatalf("delete did not leave cleanup in the background: %v, remaining=%d", response.Diagnostics, backend.deleteRemaining)
			}
		})
	}
}
