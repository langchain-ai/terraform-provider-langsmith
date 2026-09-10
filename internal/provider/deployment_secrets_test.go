package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestDeploymentSecretsPlan(t *testing.T) {
	ctx := context.Background()
	r := &DeploymentResource{}
	var schema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schema)
	for _, tc := range []struct {
		name    string
		secrets types.Map
		changed bool
		unknown bool
	}{
		{"changed", types.MapValueMust(types.StringType, map[string]attr.Value{"KEY": types.StringValue("new")}), true, false},
		{"omitted", types.MapNull(types.StringType), false, false},
		{"unknown map", types.MapUnknown(types.StringType), true, true},
		{"unknown value", types.MapValueMust(types.StringType, map[string]attr.Value{"KEY": types.StringUnknown()}), true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := testDeploymentModel()
			model.SourceConfig.ResourceSpec = nil
			model.Secrets = types.MapNull(types.StringType)
			model.SecretsVersion = types.StringNull()
			model.LatestRevisionID = types.StringValue(testRevisionID)
			state := tfsdk.State{Schema: schema.Schema}
			if diags := state.Set(ctx, model); diags.HasError() {
				t.Fatal(diags)
			}
			if diags := state.SetAttribute(ctx, path.Root("secrets_hash"), "old-hash"); diags.HasError() {
				t.Fatal(diags)
			}
			config := tfsdk.State{Schema: schema.Schema}
			model.Secrets = tc.secrets
			if diags := config.Set(ctx, model); diags.HasError() {
				t.Fatal(diags)
			}
			plan := tfsdk.Plan{Schema: schema.Schema, Raw: state.Raw}
			response := resource.ModifyPlanResponse{Plan: plan}
			r.ModifyPlan(ctx, resource.ModifyPlanRequest{
				State: state, Plan: plan,
				Config: tfsdk.Config{Schema: schema.Schema, Raw: config.Raw},
			}, &response)
			if response.Diagnostics.HasError() {
				t.Fatal(response.Diagnostics)
			}
			var hash, revision types.String
			if diags := response.Plan.GetAttribute(ctx, path.Root("secrets_hash"), &hash); diags.HasError() {
				t.Fatal(diags)
			}
			if diags := response.Plan.GetAttribute(ctx, path.Root("latest_revision_id"), &revision); diags.HasError() {
				t.Fatal(diags)
			}
			if hash.IsUnknown() != tc.unknown || (hash.ValueString() != "old-hash") != tc.changed {
				t.Fatal("planned digest did not reflect configured environment")
			}
			if revision.IsUnknown() != tc.changed {
				t.Fatal("revision metadata must become unknown when the environment changes")
			}
		})
	}
}

func TestDeploymentSecretsHash(t *testing.T) {
	values := types.MapValueMust(types.StringType, map[string]attr.Value{
		"Z": types.StringValue("line one\nline two\n"), "A": types.StringValue(""),
	})
	hash, err := deploymentSecretsHash(values)
	if err != nil || hash.IsUnknown() || len(hash.ValueString()) != 64 {
		t.Fatal("known environment did not produce a SHA-256 digest")
	}
	first, second := "", "line one\nline two\n"
	remote, err := deploymentAPISecretsHash([]deploymentSecretAPI{{Name: "A", Value: &first}, {Name: "Z", Value: &second}})
	if err != nil || !remote.Equal(hash) {
		t.Fatal("API ordering or empty strings changed the digest")
	}
	for _, values := range []map[string]string{
		{"Z": "line one\nline two", "A": ""},
		{"Z": "line one\nline two\n"},
		{"Z": "line one\nline two\n", "B": ""},
	} {
		if deploymentEnvironmentHash(values).Equal(hash) {
			t.Fatal("value, key deletion, or key rename failed to change the digest")
		}
	}
	if deploymentEnvironmentHash(map[string]string{"a": "bc"}).Equal(deploymentEnvironmentHash(map[string]string{"ab": "c"})) {
		t.Fatal("hash did not preserve key/value boundaries")
	}
	if _, err := deploymentSecretsHash(types.MapValueMust(types.StringType, map[string]attr.Value{"A": types.StringNull()})); err == nil {
		t.Fatal("null environment value was accepted")
	}
	if _, err := deploymentAPISecretsHash([]deploymentSecretAPI{{Name: "A"}}); err == nil {
		t.Fatal("redacted API value was accepted")
	}
	if _, err := deploymentAPISecretsHash([]deploymentSecretAPI{{Name: "A", Value: &first}, {Name: "A", Value: &second}}); err == nil {
		t.Fatal("duplicate API names were accepted")
	}
}

func TestDeploymentSecretsReadRefreshesHashWithoutValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": testDeploymentID, "name": "agent", "source": "github",
			"secrets": []map[string]string{{"name": "KEY", "value": "changed-remotely"}},
		})
	}))
	defer server.Close()
	previous := testDeploymentModel()
	previous.SecretsHash = deploymentEnvironmentHash(map[string]string{"KEY": "desired"})
	model, err := testDeploymentResource(server).read(context.Background(), testDeploymentID, previous)
	if err != nil {
		t.Fatal(err)
	}
	if !model.Secrets.IsNull() || model.SecretsHash.Equal(previous.SecretsHash) {
		t.Fatal("refresh did not detect drift without persisting values")
	}
	plan := previous
	plan.Secrets = types.MapNull(types.StringType)
	if !revisionChanged(model, plan) {
		t.Fatal("digest drift did not request a revision")
	}
}

func TestDeploymentSecretsRejectChangesAfterPlanning(t *testing.T) {
	plan := testDeploymentModel()
	plan.SecretsHash = deploymentEnvironmentHash(map[string]string{"KEY": "planned"})
	config := plan
	config.Secrets = types.MapValueMust(types.StringType, map[string]attr.Value{"KEY": types.StringValue("changed-after-plan")})
	if err := setDeploymentSecrets(&plan, config); err == nil {
		t.Fatal("changed ephemeral values were accepted against a saved plan")
	}
	plan.SecretsHash = types.StringUnknown()
	if err := setDeploymentSecrets(&plan, config); err != nil || plan.SecretsHash.IsUnknown() {
		t.Fatal("values unknown during planning did not resolve at apply")
	}
}
