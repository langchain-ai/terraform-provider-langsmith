package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

const (
	testDeploymentID = "11111111-1111-1111-1111-111111111111"
	testRevisionID   = "22222222-2222-2222-2222-222222222222"
)

func TestDeploymentSchemaSecretsAreWriteOnlySensitive(t *testing.T) {
	var response resource.SchemaResponse
	(&DeploymentResource{}).Schema(context.Background(), resource.SchemaRequest{}, &response)
	attribute, ok := response.Schema.Attributes["secrets"].(schema.MapAttribute)
	if !ok {
		t.Fatalf("secrets schema type = %T", response.Schema.Attributes["secrets"])
	}
	if !attribute.WriteOnly || !attribute.Sensitive || !attribute.Optional {
		t.Fatalf("secrets schema = %#v", attribute)
	}
	if _, ok := response.Schema.Attributes["secrets_version"].(schema.StringAttribute); !ok {
		t.Fatal("secrets_version is missing")
	}
}

func TestDeploymentCreateReadPathsAndPayload(t *testing.T) {
	requests := make(chan recordedDeploymentRequest, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- recordDeploymentRequest(t, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api-host/v2/deployments":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(deploymentResponseJSON("CREATING")))
		case r.Method == http.MethodPatch && r.URL.Path == "/api-host/v2/deployments/"+testDeploymentID:
			_, _ = w.Write([]byte(deploymentResponseJSON("CREATING")))
		case r.Method == http.MethodGet && r.URL.Path == "/api-host/v2/deployments/"+testDeploymentID+"/revisions/"+testRevisionID:
			_, _ = w.Write([]byte(`{"id":"` + testRevisionID + `","status":"DEPLOYED"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api-host/v2/deployments/"+testDeploymentID:
			_, _ = w.Write([]byte(deploymentResponseJSON("READY")))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	r := testDeploymentResourceAt(server, "/api-host")
	plan := testDeploymentModel()
	created, err := r.create(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID.ValueString() != testDeploymentID || created.LatestRevisionStatus.ValueString() != "DEPLOYED" {
		t.Fatalf("created = %#v", created)
	}
	create := <-requests
	if create.Method != http.MethodPost || create.Path != "/api-host/v2/deployments" {
		t.Fatalf("create request = %#v", create)
	}
	want := map[string]any{
		"name": "orders", "source": "external_docker",
		"source_config":          map[string]any{"custom_url": "orders.example.com", "listener_id": "listener", "listener_config": map[string]any{"k8s_namespace": "agents"}, "resource_spec": map[string]any{"min_scale": float64(1), "max_scale": float64(3), "cpu": 0.5, "memory_mb": float64(1024), "labels": map[string]any{"team": "agents"}}},
		"source_revision_config": map[string]any{"image_uri": "registry.example.com/orders:v1"},
		"secrets":                []any{map[string]any{"name": "API_KEY", "value": "secret"}},
		"secret_references":      []any{map[string]any{"name": "DATABASE_URL", "secret_name": "orders", "secret_key": "url"}},
	}
	if !reflect.DeepEqual(create.Body, want) {
		t.Fatalf("create payload\n got: %#v\nwant: %#v", create.Body, want)
	}
	patch := <-requests
	if patch.Method != http.MethodPatch || !reflect.DeepEqual(patch.Body, map[string]any{"display_name": "Orders"}) {
		t.Fatalf("display name patch = %#v", patch)
	}
	wait := <-requests
	if wait.Path != "/api-host/v2/deployments/"+testDeploymentID+"/revisions/"+testRevisionID {
		t.Fatalf("revision path = %q", wait.Path)
	}
	createRead := <-requests
	if createRead.Method != http.MethodGet || createRead.Path != "/api-host/v2/deployments/"+testDeploymentID {
		t.Fatalf("create read = %#v", createRead)
	}
	<-requests

	read, err := r.read(context.Background(), testDeploymentID, created)
	if err != nil {
		t.Fatal(err)
	}
	if read.DisplayName.ValueString() != "Orders" || read.SourceRevisionConfig.ImageURI.ValueString() != "registry.example.com/orders:v1" {
		t.Fatalf("read did not preserve desired inputs: %#v", read)
	}
	get := <-requests
	if get.Path != "/api-host/v2/deployments/"+testDeploymentID {
		t.Fatalf("read path = %q", get.Path)
	}
}

func TestDeploymentUpdateSeparatesPatchAndRevision(t *testing.T) {
	var requests []recordedDeploymentRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, recordDeploymentRequest(t, r))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(deploymentResponseJSON("READY")))
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"33333333-3333-3333-3333-333333333333","status":"CREATING"}`))
		case strings.Contains(r.URL.Path, "33333333-3333-3333-3333-333333333333"):
			_, _ = w.Write([]byte(`{"id":"33333333-3333-3333-3333-333333333333","status":"DEPLOYED"}`))
		case r.URL.Path == "/v2/deployments/"+testDeploymentID:
			_, _ = w.Write([]byte(deploymentResponseJSON("READY")))
		default:
			_, _ = w.Write([]byte(`{"id":"` + testRevisionID + `","status":"DEPLOYED"}`))
		}
	}))
	defer server.Close()

	r := testDeploymentResource(server)
	state := testDeploymentModel()
	state.ID = types.StringValue(testDeploymentID)
	plan := testDeploymentModel()
	plan.DisplayName = types.StringValue("Orders API")
	plan.SourceConfig.BuildOnPush = types.BoolValue(true)
	plan.SourceConfig.CustomURL = types.StringValue("api.orders.example.com")
	plan.SourceRevisionConfig.ImageURI = types.StringValue("registry.example.com/orders:v2")
	plan.SecretsVersion = types.StringValue("2")
	plan.Secrets = types.MapValueMust(types.StringType, map[string]attr.Value{"API_KEY": types.StringValue("new-secret")})
	if _, err := r.update(context.Background(), state, plan); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 6 {
		t.Fatalf("requests = %#v", requests)
	}
	if requests[0].Method != http.MethodGet || requests[0].Path != "/v2/deployments/"+testDeploymentID {
		t.Fatalf("resource specification read = %#v", requests[0])
	}
	requests = requests[1:]
	if requests[0].Method != http.MethodPatch || requests[0].Path != "/v2/deployments/"+testDeploymentID {
		t.Fatalf("patch = %#v", requests[0])
	}
	wantPatch := map[string]any{"display_name": "Orders API", "source_config": map[string]any{"build_on_push": true, "custom_url": "api.orders.example.com"}}
	if !reflect.DeepEqual(requests[0].Body, wantPatch) {
		t.Fatalf("patch payload = %#v", requests[0].Body)
	}
	if requests[1].Method != http.MethodPost || requests[1].Path != "/v2/deployments/"+testDeploymentID+"/revisions" {
		t.Fatalf("revision = %#v", requests[1])
	}
	if _, ok := requests[1].Body["display_name"]; ok {
		t.Fatal("revision payload contains display_name")
	}
	if got := requests[1].Body["source_revision_config"]; !reflect.DeepEqual(got, map[string]any{"image_uri": "registry.example.com/orders:v2"}) {
		t.Fatalf("revision source config = %#v", got)
	}
	if got := requests[1].Body["secret_references"]; !reflect.DeepEqual(got, []any{map[string]any{"name": "DATABASE_URL", "secret_name": "orders", "secret_key": "url"}}) {
		t.Fatalf("revision secret references = %#v", got)
	}
	if sourceConfig, ok := requests[1].Body["source_config"].(map[string]any); ok {
		if _, ok := sourceConfig["custom_url"]; ok {
			t.Fatal("revision payload contains custom_url")
		}
	}
	secrets, ok := requests[1].Body["secrets"].([]any)
	if !ok || len(secrets) != 1 {
		t.Fatalf("revision secrets = %#v", requests[1].Body["secrets"])
	}
	secret, ok := secrets[0].(map[string]any)
	if !ok || secret["value"] != "new-secret" {
		t.Fatalf("revision secret = %#v", secrets[0])
	}
}

func TestDeploymentReadRefreshesDesiredFieldsWithoutSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/revisions/") {
			_, _ = w.Write([]byte(`{"id":"` + testRevisionID + `","status":"DEPLOYED"}`))
			return
		}
		_, _ = w.Write([]byte(deploymentResponseJSON("READY")))
	}))
	defer server.Close()

	previous := testDeploymentModel()
	previous.ID = types.StringValue(testDeploymentID)
	previous.SourceConfig.CustomURL = types.StringValue("stale.example.com")
	previous.SourceConfig.InstallCommand = types.StringValue("uv sync")
	previous.SourceConfig.BuildCommand = types.StringValue("uv build")
	previous.SourceRevisionConfig.ImageURI = types.StringValue("desired-image")
	previous.SecretReferences = nil
	read, err := testDeploymentResource(server).read(context.Background(), testDeploymentID, previous)
	if err != nil {
		t.Fatal(err)
	}
	if read.SourceConfig.CustomURL.ValueString() != "orders.example.com" {
		t.Fatalf("server-owned fields not refreshed: %#v", read)
	}
	// The response describes the latest revision, not the request that produced
	// it, so adopting it would overwrite the configured value and fail the apply
	// as inconsistent. Same for the commands, which the service never echoes.
	if read.SourceRevisionConfig.ImageURI.ValueString() != "desired-image" {
		t.Fatalf("desired revision config not preserved: %#v", read.SourceRevisionConfig)
	}
	if read.SourceConfig.InstallCommand.ValueString() != "uv sync" || read.SourceConfig.BuildCommand.ValueString() != "uv build" {
		t.Fatalf("omitted revision commands not preserved: %#v", read.SourceConfig)
	}
	if read.SecretReferences != nil {
		t.Fatalf("undeclared secret references adopted: %#v", read.SecretReferences)
	}
	if !read.Secrets.IsNull() {
		t.Fatalf("response persisted secrets: %#v", read.Secrets)
	}
}

func TestDeploymentRevisionPayloadDoesNotSendUnavailableSecrets(t *testing.T) {
	model := testDeploymentModel()
	model.Secrets = types.MapNull(types.StringType)
	payload := revisionPayload(model)
	if _, ok := payload["secrets"]; ok {
		t.Fatalf("payload contains unavailable secrets: %#v", payload)
	}
}

func TestDeploymentRevisionPayloadOmitsEmptySourceConfig(t *testing.T) {
	model := testDeploymentModel()
	model.SourceConfig = &deploymentSourceConfigModel{}
	payload := revisionPayload(model)
	if _, ok := payload["source_config"]; ok {
		t.Fatalf("payload contains empty source_config: %#v", payload)
	}
}

func TestDeploymentSourceConfigPayloadHandlesNil(t *testing.T) {
	payload := sourceConfigPayload(nil)
	if len(payload) != 0 {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestDeploymentRevisionFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + testRevisionID + `","status":"DEPLOY_FAILED"}`))
	}))
	defer server.Close()
	_, err := testDeploymentResource(server).waitForRevision(context.Background(), testDeploymentID, testRevisionID)
	if err == nil || !strings.Contains(err.Error(), "DEPLOY_FAILED") {
		t.Fatalf("error = %v", err)
	}
}

// Superseded and transient revisions must not cause Terraform to taint a deployment.
func TestDeploymentWaitTreatsSupersededAndTransientStatusesAsNonFailures(t *testing.T) {
	t.Run("skipped is not a failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"` + testRevisionID + `","status":"SKIPPED"}`))
		}))
		defer server.Close()
		revision, err := testDeploymentResource(server).waitForRevision(context.Background(), testDeploymentID, testRevisionID)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if revision.Status != "SKIPPED" {
			t.Fatalf("status = %q", revision.Status)
		}
	})

	for _, status := range []string{"INTERRUPTED", "UNKNOWN"} {
		t.Run(status+" keeps polling", func(t *testing.T) {
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				if calls == 1 {
					_, _ = w.Write([]byte(`{"id":"` + testRevisionID + `","status":"` + status + `"}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"` + testRevisionID + `","status":"DEPLOYED"}`))
			}))
			defer server.Close()
			revision, err := testDeploymentResource(server).waitForRevision(context.Background(), testDeploymentID, testRevisionID)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if revision.Status != "DEPLOYED" || calls < 2 {
				t.Fatalf("status = %q after %d calls", revision.Status, calls)
			}
		})
	}
}

func TestDeploymentCreatePreservesInterimStateAfterPatchFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(deploymentResponseJSON("CREATING")))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"patch failed"}`))
	}))
	defer server.Close()
	model, err := testDeploymentResource(server).create(context.Background(), testDeploymentModel())
	if err == nil {
		t.Fatal("expected patch error")
	}
	if model.ID.ValueString() != testDeploymentID || model.LatestRevisionID.ValueString() != testRevisionID || model.Status.ValueString() != "CREATING" {
		t.Fatalf("interim model = %#v", model)
	}
	if !model.Secrets.IsNull() {
		t.Fatalf("interim model persisted secrets: %#v", model.Secrets)
	}
}

func TestDeploymentUpdatePreservesAppliedRevisionAfterWaitFailure(t *testing.T) {
	const newRevisionID = "33333333-3333-3333-3333-333333333333"
	var posts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			posts++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"` + newRevisionID + `","status":"CREATING"}`))
			return
		}
		if strings.Contains(r.URL.Path, "/revisions/") {
			_, _ = w.Write([]byte(`{"id":"` + newRevisionID + `","status":"DEPLOY_FAILED"}`))
			return
		}
		_, _ = w.Write([]byte(deploymentResponseJSON("READY")))
	}))
	defer server.Close()
	state := testDeploymentModel()
	state.ID = types.StringValue(testDeploymentID)
	state.ActiveRevisionID = types.StringValue(testRevisionID)
	plan := testDeploymentModel()
	plan.ID = types.StringValue(testDeploymentID)
	plan.SourceRevisionConfig.ImageURI = types.StringValue("registry.example.com/orders:v2")
	plan.SecretsVersion = types.StringValue("2")
	plan.Secrets = types.MapValueMust(types.StringType, map[string]attr.Value{"API_KEY": types.StringValue("new-secret")})
	partial, err := testDeploymentResource(server).update(context.Background(), state, plan)
	if err == nil || !strings.Contains(err.Error(), "DEPLOY_FAILED") {
		t.Fatalf("error = %v", err)
	}
	if partial.ID.ValueString() != testDeploymentID || partial.SourceRevisionConfig.ImageURI.ValueString() != plan.SourceRevisionConfig.ImageURI.ValueString() || partial.SecretsVersion.ValueString() != "2" {
		t.Fatalf("partial desired state = %#v", partial)
	}
	if partial.LatestRevisionID.ValueString() != newRevisionID || partial.LatestRevisionStatus.ValueString() != "DEPLOY_FAILED" {
		t.Fatalf("partial revision state = %#v", partial)
	}
	if !partial.Secrets.IsNull() {
		t.Fatalf("partial state persisted secrets: %#v", partial.Secrets)
	}
	// Unknown values in applied state are rejected by Terraform as a provider
	// bug, so the recovery path has to resolve every computed attribute.
	for name, value := range map[string]attr.Value{
		"id": partial.ID, "tenant_id": partial.TenantID, "created_at": partial.CreatedAt,
		"updated_at": partial.UpdatedAt, "status": partial.Status,
		"active_revision_id": partial.ActiveRevisionID, "display_name": partial.DisplayName,
	} {
		if value.IsUnknown() {
			t.Fatalf("%s is unknown in recovered state", name)
		}
	}
	if revisionChanged(partial, plan) {
		t.Fatal("retry would create a duplicate revision")
	}
	if posts != 1 {
		t.Fatalf("revision posts = %d", posts)
	}
}

func TestDeploymentDeletePollsUntilNotFound(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch len(requests) {
		case 1:
			w.WriteHeader(http.StatusNoContent)
		case 2:
			_, _ = w.Write([]byte(deploymentResponseJSON("DELETING")))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"not found"}`))
		}
	}))
	defer server.Close()
	if err := testDeploymentResource(server).delete(context.Background(), testDeploymentID); err != nil {
		t.Fatal(err)
	}
	want := []string{
		http.MethodDelete + " /v2/deployments/" + testDeploymentID,
		http.MethodGet + " /v2/deployments/" + testDeploymentID,
		http.MethodGet + " /v2/deployments/" + testDeploymentID,
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestDeploymentDeleteNotFoundIsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"not found"}`))
	}))
	defer server.Close()
	if err := testDeploymentResource(server).delete(context.Background(), testDeploymentID); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentDeleteTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(deploymentResponseJSON("DELETING")))
	}))
	defer server.Close()
	r := testDeploymentResource(server)
	r.waitTimeout = 5 * time.Millisecond
	err := r.delete(context.Background(), testDeploymentID)
	if err == nil || !strings.Contains(err.Error(), "waiting for deployment "+testDeploymentID+" deletion") || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("error = %v", err)
	}
}

func TestDeploymentImportUsesUUID(t *testing.T) {
	r := &DeploymentResource{}
	var schemaResponse resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResponse)
	raw := tftypes.NewValue(schemaResponse.Schema.Type().TerraformType(context.Background()), nil)
	response := resource.ImportStateResponse{State: tfsdk.State{Raw: raw, Schema: schemaResponse.Schema}}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: testDeploymentID}, &response)
	if response.Diagnostics.HasError() {
		t.Fatalf("import diagnostics = %v", response.Diagnostics)
	}
	var state deploymentResourceModel
	if diagnostics := response.State.Get(context.Background(), &state); diagnostics.HasError() {
		t.Fatalf("state diagnostics = %v", diagnostics)
	}
	if state.ID.ValueString() != testDeploymentID {
		t.Fatalf("imported ID = %q", state.ID.ValueString())
	}
}

type recordedDeploymentRequest struct {
	Method, Path string
	Body         map[string]any
}

func recordDeploymentRequest(t *testing.T, r *http.Request) recordedDeploymentRequest {
	t.Helper()
	result := recordedDeploymentRequest{Method: r.Method, Path: r.URL.Path}
	if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodDelete {
		if err := json.NewDecoder(r.Body).Decode(&result.Body); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func testDeploymentResource(server *httptest.Server) *DeploymentResource {
	return testDeploymentResourceAt(server, "")
}

func testDeploymentResourceAt(server *httptest.Server, basePath string) *DeploymentResource {
	return &DeploymentResource{client: langsmith.NewClient(option.WithBaseURL(server.URL+basePath), option.WithAPIKey("test-key")), pollInterval: time.Millisecond, waitTimeout: time.Second}
}

func testDeploymentModel() deploymentResourceModel {
	return deploymentResourceModel{
		Name: types.StringValue("orders"), Source: types.StringValue("external_docker"), DisplayName: types.StringValue("Orders"),
		SourceConfig:         &deploymentSourceConfigModel{CustomURL: types.StringValue("orders.example.com"), ListenerID: types.StringValue("listener"), ListenerConfig: &deploymentListenerConfigModel{K8sNamespace: types.StringValue("agents")}, ResourceSpec: &deploymentResourceSpecModel{MinScale: types.Int64Value(1), MaxScale: types.Int64Value(3), CPU: types.Float64Value(0.5), MemoryMB: types.Int64Value(1024), Labels: types.MapValueMust(types.StringType, map[string]attr.Value{"team": types.StringValue("agents")})}},
		SourceRevisionConfig: &sourceRevisionConfigModel{ImageURI: types.StringValue("registry.example.com/orders:v1")},
		EnvironmentVariables: types.MapNull(types.StringType),
		Secrets:              types.MapValueMust(types.StringType, map[string]attr.Value{"API_KEY": types.StringValue("secret")}), SecretsVersion: types.StringValue("1"),
		SecretReferences: []deploymentSecretReferenceModel{{Name: types.StringValue("DATABASE_URL"), SecretName: types.StringValue("orders"), SecretKey: types.StringValue("url")}},
	}
}

func deploymentResponseJSON(status string) string {
	return `{"id":"` + testDeploymentID + `","name":"orders","source":"external_docker","display_name":"Orders","source_config":{"custom_url":"orders.example.com","listener_id":"listener","listener_config":{"k8s_namespace":"agents"},"resource_spec":{"min_scale":1,"max_scale":3,"cpu":0.5,"memory_mb":1024,"labels":{"team":"agents"}}},"source_revision_config":{"image_uri":"registry.example.com/orders:v1"},"secret_references":[{"name":"DATABASE_URL","secret_name":"orders","secret_key":"url"}],"tenant_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:01Z","status":"` + status + `","latest_revision_id":"` + testRevisionID + `","active_revision_id":"` + testRevisionID + `"}`
}

// A failed final read must preserve the created ID to prevent duplicate deployments.
func TestDeploymentCreateKeepsIDWhenFinalReadFails(t *testing.T) {
	var gets int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(deploymentResponseJSON("CREATING")))
		case strings.Contains(r.URL.Path, "/revisions/"):
			_, _ = w.Write([]byte(`{"id":"` + testRevisionID + `","status":"DEPLOYED"}`))
		default:
			gets++
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"detail":"upstream unavailable"}`))
		}
	}))
	defer server.Close()

	plan := testDeploymentModel()
	plan.DisplayName = types.StringNull()
	model, err := testDeploymentResource(server).create(context.Background(), plan)
	if err == nil {
		t.Fatal("expected the read failure to surface")
	}
	if model.ID.ValueString() != testDeploymentID {
		t.Fatalf("created deployment ID lost: %#v", model.ID)
	}
	if model.LatestRevisionStatus.ValueString() != "DEPLOYED" {
		t.Fatalf("revision status = %#v", model.LatestRevisionStatus)
	}
	if gets == 0 {
		t.Fatal("expected a deployment read attempt")
	}
}

// A missing revision must not remove its deployment from state.
func TestDeploymentReadKeepsDeploymentWhenRevisionMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/revisions/") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"not found"}`))
			return
		}
		_, _ = w.Write([]byte(deploymentResponseJSON("READY")))
	}))
	defer server.Close()

	previous := testDeploymentModel()
	previous.ID = types.StringValue(testDeploymentID)
	model, err := testDeploymentResource(server).read(context.Background(), testDeploymentID, previous)
	if err != nil {
		t.Fatalf("a missing revision must not fail the read: %v", err)
	}
	if model.ID.ValueString() != testDeploymentID {
		t.Fatalf("deployment ID = %#v", model.ID)
	}
	if !model.LatestRevisionStatus.IsNull() {
		t.Fatalf("revision status = %#v, want null", model.LatestRevisionStatus)
	}
}
