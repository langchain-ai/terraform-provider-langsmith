package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	frameworkresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func tracingTestState(t *testing.T, model tracingKeyResourceModel) tfsdk.State {
	t.Helper()
	var schema frameworkresource.SchemaResponse
	NewTracingKeyResource().Schema(context.Background(), frameworkresource.SchemaRequest{}, &schema)
	state := tfsdk.State{Schema: schema.Schema}
	if diagnostics := state.Set(context.Background(), &model); diagnostics.HasError() {
		t.Fatal(diagnostics)
	}
	return state
}

func tracingTestResource(t *testing.T, handler http.HandlerFunc) *tracingKeyResource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("test-service-key"), option.WithMaxRetries(0))
	return &tracingKeyResource{client: client}
}

func TestTracingKeyLifecycle(t *testing.T) {
	ctx := context.Background()
	expiresAt := "2030-01-01T00:00:00.123456+00:00"
	result := tracingKeyAPIResponse{
		serviceKeyAPIResponse: serviceKeyAPIResponse{ID: "key-id", Description: "Tracing automation", ShortKey: "redacted", ExpiresAt: &expiresAt, WorkspaceNames: []string{"Production"}},
		WorkspaceIDs:          []string{"workspace-id"}, IsTracingKey: true,
	}
	exists := false
	supportChecks := 0
	r := tracingTestResource(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("X-Api-Key") != "test-service-key" {
			t.Error("caller credential was not forwarded")
		}
		if req.URL.Path == "/api/v1/info" {
			supportChecks++
			writeJSON(t, w, map[string]any{"instance_flags": map[string]bool{"tracing_keys_service_account_management_supported": true}})
			return
		}
		switch req.Method + " " + req.URL.Path {
		case "POST /api/v1/orgs/current/tracing-keys":
			var payload map[string]any
			decodeJSON(t, req, &payload)
			if len(payload) != 3 || payload["description"] != result.Description || payload["expires_at"] != "2030-01-01T00:00:00.123456Z" {
				t.Errorf("unexpected create payload: %v", payload)
			}
			workspaces, ok := payload["workspaces"].([]any)
			if !ok || len(workspaces) != 1 || workspaces[0] != "workspace-id" {
				t.Errorf("unexpected scope: %v", payload["workspaces"])
			}
			exists = true
			created := result
			created.Key = "one-time-secret"
			writeJSON(t, w, created)
		case "GET /api/v1/orgs/current/tracing-keys/key-id":
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(t, w, result)
		case "DELETE /api/v1/orgs/current/tracing-keys/key-id":
			exists = false
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	config := tracingKeyResourceModel{
		Description:    types.StringValue(result.Description),
		ExpiresAt:      types.StringValue("2030-01-01T00:00:00.123456Z"),
		Workspaces:     types.SetValueMust(types.StringType, []attr.Value{types.StringValue("workspace-id")}),
		WorkspaceNames: types.SetNull(types.StringType),
	}
	state := tracingTestState(t, config)
	created := frameworkresource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
	r.Create(ctx, frameworkresource.CreateRequest{Plan: tfsdk.Plan(state)}, &created)
	if created.Diagnostics.HasError() {
		t.Fatal(created.Diagnostics)
	}
	var model tracingKeyResourceModel
	if diagnostics := created.State.Get(ctx, &model); diagnostics.HasError() {
		t.Fatal(diagnostics)
	}
	if model.Key.ValueString() != "one-time-secret" || !model.CreatedBy.IsNull() {
		t.Fatal("one-time secret or machine attribution incorrect")
	}
	read := frameworkresource.ReadResponse{State: created.State}
	r.Read(ctx, frameworkresource.ReadRequest{State: created.State}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	if !read.State.Raw.Equal(created.State.Raw) {
		t.Fatal("no-change refresh changed state")
	}

	imported := tracingTestState(t, tracingKeyResourceModel{ID: model.ID, Workspaces: types.SetNull(types.StringType), WorkspaceNames: types.SetNull(types.StringType)})
	read = frameworkresource.ReadResponse{State: imported}
	r.Read(ctx, frameworkresource.ReadRequest{State: imported}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	if diagnostics := read.State.Get(ctx, &model); diagnostics.HasError() {
		t.Fatal(diagnostics)
	}
	if !model.Key.IsNull() || !model.Workspaces.Equal(config.Workspaces) {
		t.Fatal("import recovered a secret or lost workspace scope")
	}

	exists = false
	missing := frameworkresource.ReadResponse{State: created.State}
	r.Read(ctx, frameworkresource.ReadRequest{State: created.State}, &missing)
	if missing.Diagnostics.HasError() || !missing.State.Raw.IsNull() {
		t.Fatal("confirmed deletion did not remove state", missing.Diagnostics)
	}
	exists = true
	deleted := frameworkresource.DeleteResponse{State: created.State}
	r.Delete(ctx, frameworkresource.DeleteRequest{State: created.State}, &deleted)
	if deleted.Diagnostics.HasError() || exists {
		t.Fatal("delete failed", deleted.Diagnostics)
	}
	if supportChecks != 5 {
		t.Fatalf("support checks = %d, want 5", supportChecks)
	}
}

func TestTracingKeyCreateDoesNotRetry(t *testing.T) {
	issued := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.Method + " " + req.URL.Path {
		case "GET /api/v1/info":
			writeJSON(t, w, map[string]any{"instance_flags": map[string]bool{"tracing_keys_service_account_management_supported": true}})
		case "POST /api/v1/orgs/current/tracing-keys":
			issued++
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client := langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("test-service-key"))
	r := &tracingKeyResource{client: client}
	state := tracingTestState(t, tracingKeyResourceModel{
		Description:    types.StringValue("Tracing automation"),
		Workspaces:     types.SetValueMust(types.StringType, []attr.Value{types.StringValue("11111111-1111-1111-1111-111111111111")}),
		WorkspaceNames: types.SetNull(types.StringType),
	})
	created := frameworkresource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
	r.Create(context.Background(), frameworkresource.CreateRequest{Plan: tfsdk.Plan(state)}, &created)
	if !created.Diagnostics.HasError() {
		t.Fatal("failed create did not report an error")
	}
	if issued != 1 {
		t.Fatalf("issued %d keys after a failed response, want 1", issued)
	}
}

func TestTracingKeyUnsupportedServerRetainsState(t *testing.T) {
	for _, scenario := range []string{"absent", "false", "404", "500"} {
		t.Run(scenario, func(t *testing.T) {
			r := tracingTestResource(t, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/api/v1/info" {
					t.Error("unsupported server received lifecycle request")
					w.WriteHeader(500)
					return
				}
				switch scenario {
				case "absent":
					writeJSON(t, w, map[string]any{"instance_flags": map[string]bool{}})
				case "false":
					writeJSON(t, w, map[string]any{"instance_flags": map[string]bool{"tracing_keys_service_account_management_supported": false}})
				case "404":
					w.WriteHeader(http.StatusNotFound)
				case "500":
					w.WriteHeader(http.StatusInternalServerError)
				}
			})
			ctx := context.Background()
			state := tracingTestState(t, tracingKeyResourceModel{ID: types.StringValue("key-id"), Workspaces: types.SetNull(types.StringType), WorkspaceNames: types.SetNull(types.StringType)})
			read := frameworkresource.ReadResponse{State: state}
			r.Read(ctx, frameworkresource.ReadRequest{State: state}, &read)
			if !read.Diagnostics.HasError() || !read.State.Raw.Equal(state.Raw) {
				t.Fatal("unsupported refresh lost state")
			}
			deleted := frameworkresource.DeleteResponse{State: state}
			r.Delete(ctx, frameworkresource.DeleteRequest{State: state}, &deleted)
			if !deleted.Diagnostics.HasError() {
				t.Fatal("unsupported delete succeeded")
			}
			created := frameworkresource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
			r.Create(ctx, frameworkresource.CreateRequest{Plan: tfsdk.Plan(state)}, &created)
			if !created.Diagnostics.HasError() {
				t.Fatal("unsupported create succeeded")
			}
		})
	}
}

func TestTracingKeyReadErrorsRetainState(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			r := tracingTestResource(t, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/api/v1/info" {
					writeJSON(t, w, map[string]any{"instance_flags": map[string]bool{"tracing_keys_service_account_management_supported": true}})
				} else {
					w.WriteHeader(status)
				}
			})
			state := tracingTestState(t, tracingKeyResourceModel{ID: types.StringValue("key-id"), Key: types.StringValue("one-time-secret"), Workspaces: types.SetNull(types.StringType), WorkspaceNames: types.SetNull(types.StringType)})
			read := frameworkresource.ReadResponse{State: state}
			r.Read(context.Background(), frameworkresource.ReadRequest{State: state}, &read)
			if !read.Diagnostics.HasError() || !read.State.Raw.Equal(state.Raw) {
				t.Fatal("failed refresh lost state or secret")
			}
		})
	}
}

func TestTracingKeyRejectsUnexpectedReadResponse(t *testing.T) {
	for _, scenario := range []string{"ordinary key", "different key"} {
		t.Run(scenario, func(t *testing.T) {
			r := tracingTestResource(t, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/api/v1/info" {
					writeJSON(t, w, map[string]any{"instance_flags": map[string]bool{"tracing_keys_service_account_management_supported": true}})
					return
				}
				result := tracingKeyAPIResponse{serviceKeyAPIResponse: serviceKeyAPIResponse{ID: "key-id"}}
				if scenario == "different key" {
					result.ID = "other-id"
					result.IsTracingKey = true
				}
				writeJSON(t, w, result)
			})
			state := tracingTestState(t, tracingKeyResourceModel{ID: types.StringValue("key-id"), Key: types.StringValue("one-time-secret"), Workspaces: types.SetNull(types.StringType), WorkspaceNames: types.SetNull(types.StringType)})
			read := frameworkresource.ReadResponse{State: state}
			r.Read(context.Background(), frameworkresource.ReadRequest{State: state}, &read)
			if !read.Diagnostics.HasError() || !read.State.Raw.Equal(state.Raw) {
				t.Fatal("unexpected response changed state")
			}
		})
	}
}
