package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestDeploymentEnvironmentHashCombinesMaps(t *testing.T) {
	public := types.MapValueMust(types.StringType, map[string]attr.Value{"LOG_LEVEL": types.StringValue("info")})
	secret := types.MapValueMust(types.StringType, map[string]attr.Value{"API_TOKEN": types.StringValue(offlineSecretOne)})
	union := types.MapValueMust(types.StringType, map[string]attr.Value{
		"LOG_LEVEL": types.StringValue("info"), "API_TOKEN": types.StringValue(offlineSecretOne),
	})
	combined, err := deploymentSecretsHash(public, secret)
	if err != nil {
		t.Fatal(err)
	}
	beforeSplit, err := deploymentSecretsHash(union)
	if err != nil || !combined.Equal(beforeSplit) {
		t.Fatal("splitting public entries out of secrets must preserve the environment digest")
	}
	for _, tc := range []struct {
		name    string
		public  types.Map
		secret  types.Map
		unknown bool
		invalid bool
	}{
		{"overlap", public, public, false, true},
		{"overlap with unknown value", public, types.MapValueMust(types.StringType, map[string]attr.Value{"LOG_LEVEL": types.StringUnknown()}), false, true},
		{"null public entry", types.MapValueMust(types.StringType, map[string]attr.Value{"LOG_LEVEL": types.StringNull()}), secret, false, true},
		{"empty public name", types.MapValueMust(types.StringType, map[string]attr.Value{"": types.StringValue("info")}), secret, false, true},
		{"unknown public map", types.MapUnknown(types.StringType), secret, true, false},
		{"unknown secret map", public, types.MapUnknown(types.StringType), true, false},
		{"unknown public value", types.MapValueMust(types.StringType, map[string]attr.Value{"LOG_LEVEL": types.StringUnknown()}), secret, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hash, err := deploymentSecretsHash(tc.public, tc.secret)
			if (err != nil) != tc.invalid || (!tc.invalid && hash.IsUnknown() != tc.unknown) {
				t.Fatal("combined environment hash did not preserve validation or unknown values")
			}
		})
	}
}

func TestDeploymentEnvironmentPayload(t *testing.T) {
	model := testDeploymentModel()
	model.EnvironmentVariables = types.MapValueMust(types.StringType, map[string]attr.Value{"LOG_LEVEL": types.StringValue("info")})
	want := []map[string]string{{"name": "API_KEY", "value": "secret"}, {"name": "LOG_LEVEL", "value": "info"}}
	for _, payload := range []map[string]any{createPayload(model), revisionPayload(model)} {
		if !reflect.DeepEqual(payload["secrets"], want) {
			t.Fatal("deployment payload did not send the complete environment in API order")
		}
	}
	model.Secrets = types.MapNull(types.StringType)
	if got := revisionPayload(model)["secrets"]; !reflect.DeepEqual(got, want[1:]) {
		t.Fatal("public map alone must manage the complete environment")
	}
	model.EnvironmentVariables = types.MapNull(types.StringType)
	if _, ok := revisionPayload(model)["secrets"]; ok {
		t.Fatal("omitting both maps must preserve the previous revision environment")
	}
}

func TestDeploymentEnvironmentReadOnlyRefreshesDeclaredPublicKeys(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.URL.Path, "/revisions/") {
			_ = json.NewEncoder(w).Encode(deploymentResourceRevisionAPI{ID: testRevisionID, Status: "DEPLOYED"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": testDeploymentID, "name": "orders", "source": "external_docker",
			"latest_revision_id": testRevisionID,
			"secrets": []map[string]string{
				{"name": "LOG_LEVEL", "value": "debug"},
				{"name": "API_TOKEN", "value": offlineSecretOne},
			},
		})
	}))
	defer server.Close()
	previous := testDeploymentModel()
	previous.EnvironmentVariables = types.MapValueMust(types.StringType, map[string]attr.Value{
		"LOG_LEVEL": types.StringValue("info"), "REMOVED": types.StringValue("previous"),
	})
	for _, imported := range []bool{false, true} {
		t.Run(map[bool]string{false: "refresh", true: "import"}[imported], func(t *testing.T) {
			before := previous
			if imported {
				before = deploymentResourceModel{EnvironmentVariables: types.MapNull(types.StringType)}
			}
			model, err := testDeploymentResource(server).read(context.Background(), testDeploymentID, before)
			if err != nil {
				t.Fatal(err)
			}
			if !model.Secrets.IsNull() || model.SecretsHash.IsNull() {
				t.Fatal("refresh must record the environment digest without secret values")
			}
			if imported {
				if !model.EnvironmentVariables.IsNull() {
					t.Fatal("import must not classify API environment values as public")
				}
			} else if !reflect.DeepEqual(stringMap(model.EnvironmentVariables), map[string]string{"LOG_LEVEL": "debug"}) {
				t.Fatal("refresh must expose changed and missing declared public keys only")
			}
		})
	}
}

func TestDeploymentEnvironmentMaskedRefreshKeepsPreviousValues(t *testing.T) {
	for _, tc := range []struct {
		name        string
		environment []map[string]string
		masked      bool
		public      map[string]string
	}{
		{"masked values", []map[string]string{
			{"name": "LOG_LEVEL", "value": "***"},
			{"name": "MODE", "value": "fast"},
			{"name": "API_TOKEN", "value": "***"},
		}, true, map[string]string{"LOG_LEVEL": "info", "MODE": "fast"}},
		{"values that only look masked", []map[string]string{
			{"name": "LOG_LEVEL", "value": "****"},
			{"name": "MODE", "value": " *** "},
			{"name": "API_TOKEN", "value": offlineSecretOne},
			{"name": "OPENAI_API_KEY", "value": "*******"},
		}, false, map[string]string{"LOG_LEVEL": "****", "MODE": " *** "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(req.URL.Path, "/revisions/") {
					_ = json.NewEncoder(w).Encode(deploymentResourceRevisionAPI{ID: testRevisionID, Status: "DEPLOYED"})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": testDeploymentID, "name": "orders", "source": "external_docker",
					"latest_revision_id": testRevisionID,
					"secrets":            tc.environment,
				})
			}))
			defer server.Close()
			previous := testDeploymentModel()
			previous.SecretsHash = types.StringValue("previous-hash")
			previous.EnvironmentVariables = types.MapValueMust(types.StringType, map[string]attr.Value{
				"LOG_LEVEL": types.StringValue("info"), "MODE": types.StringValue("slow"),
			})
			model, err := testDeploymentResource(server).read(context.Background(), testDeploymentID, previous)
			if err != nil {
				t.Fatal(err)
			}
			if kept := model.SecretsHash.ValueString() == "previous-hash"; kept != tc.masked {
				t.Fatalf("previous digest kept = %t, want %t: only a masked environment keeps it", kept, tc.masked)
			}
			if !reflect.DeepEqual(stringMap(model.EnvironmentVariables), tc.public) {
				t.Fatalf("public values = %v, want %v: masked values keep their previous values; others refresh",
					stringMap(model.EnvironmentVariables), tc.public)
			}
		})
	}
}

// A masked read keeps the previous digest, so drift in a declared public value
// must itself plan and create the revision that restores it.
func TestDeploymentEnvironmentMaskedDriftCreatesRevision(t *testing.T) {
	configured := []map[string]string{
		{"name": "DISTRIBUTED_RUNTIME_ENABLED", "value": "true"},
		{"name": "LOG_LEVEL", "value": "***"},
		{"name": "API_KEY", "value": "***"},
	}
	for _, tc := range []struct {
		name    string
		drifted []map[string]string // the API environment after a change outside Terraform
	}{
		{"readable value changed", []map[string]string{
			{"name": "DISTRIBUTED_RUNTIME_ENABLED", "value": "false"},
			{"name": "LOG_LEVEL", "value": "***"},
			{"name": "API_KEY", "value": "***"},
		}},
		{"public key deleted", []map[string]string{
			{"name": "DISTRIBUTED_RUNTIME_ENABLED", "value": "true"},
			{"name": "API_KEY", "value": "***"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			environment := tc.drifted
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(req.URL.Path, "/revisions/") {
					_ = json.NewEncoder(w).Encode(deploymentResourceRevisionAPI{ID: testRevisionID, Status: "DEPLOYED"})
					return
				}
				var body map[string]any
				_ = json.Unmarshal([]byte(deploymentResponseJSON("READY")), &body)
				body["secrets"] = environment
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			r := testDeploymentResource(server)
			config := testDeploymentModel()
			config.SourceConfig.ResourceSpec.Annotations = types.MapNull(types.StringType)
			config.EnvironmentVariables = types.MapValueMust(types.StringType, map[string]attr.Value{
				"DISTRIBUTED_RUNTIME_ENABLED": types.StringValue("true"), "LOG_LEVEL": types.StringValue("info"),
			})
			hash, err := deploymentSecretsHash(config.EnvironmentVariables, config.Secrets)
			if err != nil {
				t.Fatal(err)
			}
			config.SecretsHash = hash

			state, err := r.read(ctx, testDeploymentID, config)
			if err != nil {
				t.Fatal(err)
			}
			if !state.SecretsHash.Equal(hash) {
				t.Fatal("a masked environment must keep the previous digest")
			}

			response := modifyDeploymentPlan(t, r, state, config)
			if response.Diagnostics.HasError() {
				t.Fatal(response.Diagnostics)
			}
			var revision types.String
			if diags := response.Plan.GetAttribute(ctx, path.Root("latest_revision_id"), &revision); diags.HasError() {
				t.Fatal(diags)
			}
			if !revision.IsUnknown() {
				t.Fatal("the plan must show that a revision restores the drifted value")
			}
			if !revisionChanged(state, config) {
				t.Fatal("apply must create the revision that sends the configured environment")
			}

			environment = configured // the revision sent the configured environment
			applied, err := r.read(ctx, testDeploymentID, config)
			if err != nil {
				t.Fatal(err)
			}
			if !applied.EnvironmentVariables.Equal(config.EnvironmentVariables) {
				t.Fatal("state after apply must match the plan")
			}
		})
	}
}

// The API keeps the stored value when a write sends the mask, so state would
// record "***" while the deployment keeps another value.
func TestDeploymentEnvironmentRejectsMaskedValue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("planning must not call the API: %s %s", req.Method, req.URL.Path)
	}))
	defer server.Close()
	r := testDeploymentResource(server)
	state := testDeploymentModel()
	state.SourceConfig.ResourceSpec.Annotations = types.MapNull(types.StringType)
	state.SecretsHash, _ = deploymentSecretsHash(state.EnvironmentVariables, state.Secrets)
	for _, tc := range []struct {
		name        string
		environment map[string]string
		secrets     map[string]string
		rejected    bool
	}{
		{"masked public value", map[string]string{"LOG_LEVEL": "***"}, map[string]string{"API_KEY": "secret"}, true},
		{"masked secret value", map[string]string{"LOG_LEVEL": "info"}, map[string]string{"API_KEY": "***"}, true},
		// The API trims a written value before comparing it with the mask.
		{"padded masked value", map[string]string{"LOG_LEVEL": " ***\n"}, map[string]string{"API_KEY": "secret"}, true},
		{"value that only looks masked", map[string]string{"LOG_LEVEL": "****"}, map[string]string{"API_KEY": "secret"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := state
			config.EnvironmentVariables = stringMapValue(tc.environment)
			config.Secrets = stringMapValue(tc.secrets)
			response := modifyDeploymentPlan(t, r, state, config)
			if response.Diagnostics.HasError() != tc.rejected {
				t.Fatalf("rejected = %t, want %t: %v", response.Diagnostics.HasError(), tc.rejected, response.Diagnostics)
			}
			if tc.rejected && !strings.Contains(response.Diagnostics[0].Detail(), "reserved") {
				t.Fatalf("the error must say the mask is reserved: %s", response.Diagnostics[0].Detail())
			}
		})
	}
}

func stringMapValue(values map[string]string) types.Map {
	elements := make(map[string]attr.Value, len(values))
	for name, value := range values {
		elements[name] = types.StringValue(value)
	}
	return types.MapValueMust(types.StringType, elements)
}

// modifyDeploymentPlan runs ModifyPlan as Terraform does for an update from
// state to config: write-only secrets are null in state and plan.
func modifyDeploymentPlan(t *testing.T, r *DeploymentResource, state, config deploymentResourceModel) resource.ModifyPlanResponse {
	t.Helper()
	ctx := context.Background()
	var schema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schema)
	prior := tfsdk.State{Schema: schema.Schema}
	priorModel := state
	priorModel.Secrets = types.MapNull(types.StringType)
	if diags := prior.Set(ctx, priorModel); diags.HasError() {
		t.Fatal(diags)
	}
	configured := tfsdk.State{Schema: schema.Schema}
	if diags := configured.Set(ctx, config); diags.HasError() {
		t.Fatal(diags)
	}
	proposed := tfsdk.State{Schema: schema.Schema}
	proposedModel := config
	proposedModel.Secrets = types.MapNull(types.StringType)
	if diags := proposed.Set(ctx, proposedModel); diags.HasError() {
		t.Fatal(diags)
	}
	plan := tfsdk.Plan{Schema: schema.Schema, Raw: proposed.Raw}
	response := resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{
		State: prior, Plan: plan, Config: tfsdk.Config{Schema: schema.Schema, Raw: configured.Raw},
	}, &response)
	return response
}

func TestDeploymentEnvironmentSavedPlanBinding(t *testing.T) {
	plan := testDeploymentModel()
	plan.EnvironmentVariables = types.MapValueMust(types.StringType, map[string]attr.Value{"LOG_LEVEL": types.StringValue("info")})
	plan.SecretsHash, _ = deploymentSecretsHash(plan.EnvironmentVariables, plan.Secrets)
	config := plan
	plan.EnvironmentVariables = types.MapValueMust(types.StringType, map[string]attr.Value{"LOG_LEVEL": types.StringValue("debug")})
	if err := setDeploymentSecrets(&plan, config); err == nil {
		t.Fatal("combined digest did not bind public values to the saved plan")
	}
	plan.SecretsHash = types.StringUnknown()
	if err := setDeploymentSecrets(&plan, config); err != nil || plan.SecretsHash.IsUnknown() {
		t.Fatal("unknown combined digest did not resolve at apply")
	}
	plan.EnvironmentVariables = types.MapUnknown(types.StringType)
	if err := setDeploymentSecrets(&plan, config); err == nil {
		t.Fatal("unknown public environment was accepted at apply")
	}
}
