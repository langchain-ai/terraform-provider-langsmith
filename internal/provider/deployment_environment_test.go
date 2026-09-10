package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
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
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": testDeploymentID, "name": "orders", "source": "external_docker",
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
