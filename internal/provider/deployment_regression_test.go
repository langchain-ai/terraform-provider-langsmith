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
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestDeploymentImportFiltersRevisionInputsBySource(t *testing.T) {
	for _, tt := range []struct {
		source string
		want   map[string]any
	}{
		{"github", map[string]any{"repo_ref": "main", "langgraph_config_path": "langgraph.json"}},
		{"external_docker", map[string]any{"image_uri": "registry.example.com/agent:v1"}},
		{"internal_template", map[string]any{}},
	} {
		t.Run(tt.source, func(t *testing.T) {
			api := deploymentAPI{
				ID: testDeploymentID, Name: "agent", Source: tt.source,
				SourceConfig: map[string]any{"repo_branch": "main"},
				SourceRevisionConfig: map[string]any{
					"repo_ref": "resolved-commit", "langgraph_config_path": "langgraph.json",
					"image_uri": "registry.example.com/agent:v1", "source_tarball_path": "uploads/source.tar.gz",
				},
			}
			imported := deploymentModelFromAPI(api, deploymentResourceRevisionAPI{}, deploymentResourceModel{})
			if got := sourceRevisionPayload(imported.SourceRevisionConfig); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("imported revision inputs = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestDeploymentImportGithubFallsBackToRevisionRef(t *testing.T) {
	api := deploymentAPI{Name: "agent", Source: "github", SourceRevisionConfig: map[string]any{"repo_ref": "main", "langgraph_config_path": "langgraph.json"}}
	imported := deploymentModelFromAPI(api, deploymentResourceRevisionAPI{}, deploymentResourceModel{})
	if imported.SourceRevisionConfig.RepoRef.ValueString() != "main" {
		t.Fatalf("imported repo ref = %s", imported.SourceRevisionConfig.RepoRef)
	}
}

func TestDeploymentRejectedUpdatePreservesDesiredState(t *testing.T) {
	for _, method := range []string{http.MethodPatch, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if req.Method == method {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"detail":"write rejected"}`))
					return
				}
				if strings.Contains(req.URL.Path, "/revisions/") {
					_, _ = w.Write([]byte(`{"id":"` + testRevisionID + `","status":"DEPLOYED"}`))
					return
				}
				_, _ = w.Write([]byte(deploymentResponseJSON("READY")))
			}))
			defer server.Close()
			state, plan := testDeploymentModel(), testDeploymentModel()
			state.ID, plan.ID = types.StringValue(testDeploymentID), types.StringValue(testDeploymentID)
			state.SourceConfig.ResourceSpec, plan.SourceConfig.ResourceSpec = nil, nil
			plan.SourceRevisionConfig.ImageURI = types.StringValue("registry.example.com/orders:v2")
			plan.SecretsVersion = types.StringValue("2")
			if method == http.MethodPatch {
				plan.DisplayName = types.StringValue("New name")
			}
			result, err := testDeploymentResource(server).update(context.Background(), state, plan)
			if err == nil {
				t.Fatal("expected write rejection")
			}
			if !result.SourceRevisionConfig.ImageURI.Equal(state.SourceRevisionConfig.ImageURI) || !result.SecretsVersion.Equal(state.SecretsVersion) {
				t.Fatal("rejected desired inputs were recorded as applied")
			}
			if !revisionChanged(result, plan) {
				t.Fatal("retry would skip the rejected revision")
			}
		})
	}
}

func TestDeploymentRevisionPreservesUnmanagedResourceFields(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unmanaged", true: "managed"}[managed], func(t *testing.T) {
			var payload map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if req.Method == http.MethodPost {
					if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					_, _ = w.Write([]byte(`{"id":"` + testRevisionID + `","status":"CREATING"}`))
					return
				}
				if strings.Contains(req.URL.Path, "/revisions/") {
					_, _ = w.Write([]byte(`{"id":"` + testRevisionID + `","status":"DEPLOYED"}`))
					return
				}
				var response deploymentAPI
				if err := json.Unmarshal([]byte(deploymentResponseJSON("READY")), &response); err != nil {
					t.Error(err)
					return
				}
				response.SourceConfig["resource_spec"] = map[string]any{
					"cpu": 1, "memory_mb": 4096, "queue_cpu": 4,
					"sidecars":           []any{map[string]any{"name": "proxy", "image": "proxy:v1"}},
					"image_pull_secrets": []any{map[string]any{"name": "registry"}},
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			state, plan := testDeploymentModel(), testDeploymentModel()
			state.ID = types.StringValue(testDeploymentID)
			plan.SourceConfig.ResourceSpec = nil
			if managed {
				plan.SourceConfig.ResourceSpec = &deploymentResourceSpecModel{CPU: types.Float64Value(2)}
			}
			plan.SourceRevisionConfig.ImageURI = types.StringValue("registry.example.com/orders:v2")
			if _, err := testDeploymentResource(server).update(context.Background(), state, plan); err != nil {
				t.Fatal(err)
			}
			if !managed {
				if config, ok := payload["source_config"].(map[string]any); ok && config["resource_spec"] != nil {
					t.Fatal("unmanaged resources should be inherited by omitting resource_spec")
				}
				return
			}
			want := map[string]any{
				"cpu": float64(2), "memory_mb": float64(4096), "queue_cpu": float64(4),
				"sidecars":           []any{map[string]any{"name": "proxy", "image": "proxy:v1"}},
				"image_pull_secrets": []any{map[string]any{"name": "registry"}},
			}
			config, ok := payload["source_config"].(map[string]any)
			if !ok {
				t.Fatalf("missing revision source_config: %#v", payload)
			}
			got := config["resource_spec"]
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("replacement resource_spec = %#v, want %#v", got, want)
			}
		})
	}
}

func TestDeploymentEmptySecretsUpdateFailsBeforeWrites(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	state, plan := testDeploymentModel(), testDeploymentModel()
	state.ID, plan.ID = types.StringValue(testDeploymentID), types.StringValue(testDeploymentID)
	plan.Secrets = types.MapValueMust(types.StringType, map[string]attr.Value{})
	plan.SecretsVersion = types.StringValue("2")
	plan.DisplayName = types.StringValue("New name")
	result, err := testDeploymentResource(server).update(context.Background(), state, plan)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected an empty-secret API limitation, got %v", err)
	}
	if requests != 0 || !result.SecretsVersion.Equal(state.SecretsVersion) {
		t.Fatalf("unsupported clearing made %d requests or persisted the version", requests)
	}
}

func TestDeploymentUpdateKeepsRevisionWhenFinalReadsFail(t *testing.T) {
	const revisionID = "33333333-3333-3333-3333-333333333333"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"` + revisionID + `","status":"CREATING"}`))
		} else if strings.Contains(req.URL.Path, "/revisions/") {
			_, _ = w.Write([]byte(`{"id":"` + revisionID + `","status":"DEPLOYED"}`))
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	state, plan := testDeploymentModel(), testDeploymentModel()
	state.ID, plan.ID = types.StringValue(testDeploymentID), types.StringValue(testDeploymentID)
	state.LatestRevisionID = types.StringValue(testRevisionID)
	state.SourceConfig.ResourceSpec, plan.SourceConfig.ResourceSpec = nil, nil
	plan.SourceRevisionConfig.ImageURI = types.StringValue("registry.example.com/orders:v2")
	result, err := testDeploymentResource(server).update(context.Background(), state, plan)
	if err == nil {
		t.Fatal("expected final read failure")
	}
	if result.LatestRevisionID.ValueString() != revisionID || result.LatestRevisionStatus.ValueString() != "DEPLOYED" {
		t.Fatalf("lost known accepted revision: %s / %s", result.LatestRevisionID, result.LatestRevisionStatus)
	}
	if revisionChanged(result, plan) {
		t.Fatal("retry would duplicate the accepted revision")
	}
}

func TestDeploymentResourceSpecReadFailurePreventsWrites(t *testing.T) {
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			writes++
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	state, plan := testDeploymentModel(), testDeploymentModel()
	state.ID = types.StringValue(testDeploymentID)
	plan.DisplayName = types.StringValue("New name")
	plan.SourceConfig.ResourceSpec.CPU = types.Float64Value(2)
	result, err := testDeploymentResource(server).update(context.Background(), state, plan)
	if err == nil || writes != 0 || !result.SourceConfig.ResourceSpec.CPU.Equal(state.SourceConfig.ResourceSpec.CPU) {
		t.Fatalf("failed resource read must preserve state without writes: err=%v, writes=%d", err, writes)
	}
}

func TestDeploymentGithubRevisionFailurePreservesRefAndPush(t *testing.T) {
	for _, failure := range []string{"post", "wait", "read"} {
		t.Run(failure, func(t *testing.T) {
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case req.Method == http.MethodPost:
					posts++
					if failure == "post" {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					_, _ = w.Write([]byte(`{"id":"` + testRevisionID + `","status":"CREATING"}`))
				case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/revisions/"):
					status := "DEPLOYED"
					if failure == "wait" {
						status = "DEPLOY_FAILED"
					}
					_ = json.NewEncoder(w).Encode(deploymentResourceRevisionAPI{ID: testRevisionID, Status: status})
				default:
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			state := deploymentResourceModel{
				ID: types.StringValue(testDeploymentID), Source: types.StringValue("github"),
				SourceConfig:         &deploymentSourceConfigModel{BuildOnPush: types.BoolValue(true)},
				SourceRevisionConfig: &sourceRevisionConfigModel{RepoRef: types.StringValue("main")},
			}
			plan := state
			plan.SourceConfig = &deploymentSourceConfigModel{BuildOnPush: types.BoolValue(false)}
			plan.SourceRevisionConfig = &sourceRevisionConfigModel{RepoRef: types.StringValue("refs/tags/v1")}
			result, err := testDeploymentResource(server).update(context.Background(), state, plan)
			if err == nil || posts != 1 {
				t.Fatalf("expected %s failure after one revision POST, got %d POSTs and %v", failure, posts, err)
			}
			want := plan
			if failure == "post" {
				want = state
			}
			if !result.SourceConfig.BuildOnPush.Equal(want.SourceConfig.BuildOnPush) || !result.SourceRevisionConfig.RepoRef.Equal(want.SourceRevisionConfig.RepoRef) {
				t.Fatal("recovered ref and push flag do not match the accepted inputs")
			}
			if revisionChanged(result, plan) != (failure == "post") {
				t.Fatal("retry must repeat a rejected revision and preserve an accepted revision")
			}
		})
	}
}

func TestDeploymentCreatePreservesExplicitCloudSettings(t *testing.T) {
	model := deploymentResourceModel{
		Source: types.StringValue("github"),
		SourceConfig: &deploymentSourceConfigModel{
			DeploymentType: types.StringValue("dev_zero"),
			BuildOnPush:    types.BoolValue(true),
		},
	}
	config, ok := createPayload(model)["source_config"].(map[string]any)
	if !ok || config["deployment_type"] != "dev_zero" || config["build_on_push"] != true {
		t.Fatalf("creation defaults overwrote explicit settings: %#v", config)
	}
}

func TestDeploymentRefreshesManagedResourceFields(t *testing.T) {
	api := deploymentAPI{
		Name: "orders", Source: "external_docker",
		SourceConfig: map[string]any{"resource_spec": map[string]any{
			"cpu": float64(4), "cpu_limit": nil, "memory_mb": float64(8192), "min_scale": float64(3),
			"labels":      map[string]any{"team": "updated", "remote": "label"},
			"annotations": map[string]any{"remote": "annotation"}, "service_account_name": "new-account",
		}},
	}
	for _, tc := range []struct {
		name string
		spec *deploymentResourceSpecModel
		want map[string]any
	}{
		{"managed", &deploymentResourceSpecModel{
			CPU: types.Float64Value(2), CPULimit: types.Float64Value(2), MemoryMB: types.Int64Value(4096),
			Labels:             types.MapValueMust(types.StringType, map[string]attr.Value{"team": types.StringValue("agents")}),
			Annotations:        types.MapValueMust(types.StringType, map[string]attr.Value{}),
			ServiceAccountName: types.StringValue("old-account"),
		}, map[string]any{
			"cpu": float64(4), "memory_mb": int64(8192),
			"labels":      map[string]string{"team": "updated", "remote": "label"},
			"annotations": map[string]string{"remote": "annotation"}, "service_account_name": "new-account",
		}},
		{"empty", &deploymentResourceSpecModel{}, map[string]any{}},
		{"unmanaged", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := testDeploymentModel()
			previous.SourceConfig.ResourceSpec = tc.spec
			model := deploymentModelFromAPI(api, deploymentResourceRevisionAPI{}, previous)
			var got map[string]any
			if model.SourceConfig.ResourceSpec != nil {
				got = resourceSpecPayload(model.SourceConfig.ResourceSpec)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("refreshed resource fields = %#v, want %#v", got, tc.want)
			}
		})
	}
}
