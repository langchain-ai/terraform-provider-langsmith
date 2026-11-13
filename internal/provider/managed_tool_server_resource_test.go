package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/langchain-ai/langsmith-go/option"
)

func managedToolServerTestState(t *testing.T, r *managedToolServerResource, model managedToolServerModel) tfsdk.State {
	t.Helper()
	var response resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &response)
	state := tfsdk.State{Schema: response.Schema}
	if diags := state.Set(context.Background(), model); diags.HasError() {
		t.Fatalf("set state: %v", diags)
	}
	return state
}

func managedToolServerTestModel() managedToolServerModel {
	return managedToolServerModel{
		ID: types.StringValue("server-id"), Name: types.StringValue("Example-MCP"),
		UpstreamURL:    types.StringValue("https://mcp.example.com/mcp"),
		ConnectionType: types.StringValue("none"), SecretHeaders: types.SetNull(types.StringType), OAuthSetup: types.ObjectNull(managedToolServerOAuthSetupTypes),
	}
}

func TestManagedToolServerLifecycle(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider workspace", true: "resource workspace"}[override], func(t *testing.T) {
			ctx := context.Background()
			model := managedToolServerTestModel()
			wantWorkspace := "provider-workspace"
			importID := "server-id"
			if override {
				model.WorkspaceID = types.StringValue("resource-workspace")
				wantWorkspace = "resource-workspace"
				importID = "resource-workspace/server-id"
			}
			server := managedToolServerAPI{ID: "server-id", Name: "Example-MCP", UpstreamURL: "https://mcp.example.com/mcp", Owner: "workspace", Scope: "workspace", URL: "/v1/managed-tools/servers/server-id/mcp", Enabled: true, Connection: managedToolServerConnectionAPI{Type: "none"}}
			postCount, getCount, deleteCount := 0, 0, 0
			r := &managedToolServerResource{client: newTagTestClientWithOptions(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if got := req.Header.Get("X-Tenant-Id"); got != wantWorkspace {
					t.Errorf("workspace = %q, want %q", got, wantWorkspace)
				}
				switch req.Method {
				case http.MethodPost:
					postCount++
					if req.URL.Path != "/v1/managed-tools/servers" {
						t.Errorf("create path = %q", req.URL.Path)
					}
					var payload map[string]json.RawMessage
					decodeJSON(t, req, &payload)
					if len(payload) != 4 || string(payload["scope"]) != `"workspace"` || string(payload["upstream_url"]) != `"https://mcp.example.com/mcp"` || string(payload["connection"]) != `{"type":"none"}` {
						t.Errorf("create payload = %s", payload)
					}
					writeJSON(t, w, server)
				case http.MethodGet:
					getCount++
					if req.URL.Path != "/v1/managed-tools/servers" || req.URL.Query().Get("page_size") != "100" {
						t.Errorf("read URL = %s", req.URL)
					}
					writeJSON(t, w, map[string]any{"items": []managedToolServerAPI{server}, "next_cursor": nil})
				case http.MethodDelete:
					deleteCount++
					if req.URL.Path != "/v1/managed-tools/servers/server-id" {
						t.Errorf("delete path = %q", req.URL.Path)
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected method = %s", req.Method)
				}
			}), option.WithTenantID("provider-workspace"))}
			state := managedToolServerTestState(t, r, model)
			createResp := resource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan(state)}, &createResp)
			if createResp.Diagnostics.HasError() {
				t.Fatalf("Create: %v", createResp.Diagnostics)
			}
			var created managedToolServerModel
			if diags := createResp.State.Get(ctx, &created); diags.HasError() {
				t.Fatal(diags)
			}
			if created.ID.ValueString() != "server-id" || !created.Enabled.ValueBool() || !created.WorkspaceID.Equal(model.WorkspaceID) || !created.OAuthSetup.IsNull() {
				t.Fatalf("created = %#v", created)
			}
			readResp := resource.ReadResponse{State: createResp.State}
			r.Read(ctx, resource.ReadRequest{State: createResp.State}, &readResp)
			if readResp.Diagnostics.HasError() {
				t.Fatalf("Read: %v", readResp.Diagnostics)
			}
			importResp := resource.ImportStateResponse{State: tfsdk.State{Schema: state.Schema, Raw: tftypes.NewValue(state.Raw.Type(), nil)}}
			r.ImportState(ctx, resource.ImportStateRequest{ID: importID}, &importResp)
			if importResp.Diagnostics.HasError() {
				t.Fatalf("Import: %v", importResp.Diagnostics)
			}
			importRead := resource.ReadResponse{State: importResp.State}
			r.Read(ctx, resource.ReadRequest{State: importResp.State}, &importRead)
			if importRead.Diagnostics.HasError() {
				t.Fatalf("Read imported state: %v", importRead.Diagnostics)
			}
			deleteResp := resource.DeleteResponse{State: readResp.State}
			r.Delete(ctx, resource.DeleteRequest{State: readResp.State}, &deleteResp)
			if deleteResp.Diagnostics.HasError() {
				t.Fatalf("Delete: %v", deleteResp.Diagnostics)
			}
			if postCount != 1 || getCount != 2 || deleteCount != 1 {
				t.Fatalf("requests POST=%d GET=%d DELETE=%d", postCount, getCount, deleteCount)
			}
			wantWorkspace = "provider-workspace"
			var result any
			if err := r.client.Get(ctx, managedToolServersPath, nil, &result, option.WithQuery("page_size", "100")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManagedToolServerOAuthAndPagination(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic connection", true: "existing connection"}[manual], func(t *testing.T) {
			ctx := context.Background()
			model := managedToolServerTestModel()
			model.ConnectionType = types.StringValue("oauth")
			if manual {
				model.AgentAuthConnectionSlug = types.StringValue("existing-oauth")
			}
			model.WorkspaceID = types.StringValue("other-workspace")
			server := managedToolServerAPI{ID: "server-id", Name: model.Name.ValueString(), UpstreamURL: model.UpstreamURL.ValueString(), Owner: "workspace", Scope: "workspace", Enabled: true, Connection: managedToolServerConnectionAPI{Type: "oauth", AgentAuthConnectionSlug: model.AgentAuthConnectionSlug.ValueStringPointer(), OAuthSetup: &managedToolServerOAuthSetupAPI{ProviderSlug: "provider", CallbackURL: "https://auth.example.com/callback", AuthorizationURL: "https://oauth.example.com/authorize", TokenURL: "https://oauth.example.com/token", TokenEndpointAuthMethod: "client_secret_post", Scopes: []string{"read"}, PKCE: true}}}
			pages := 0
			r := &managedToolServerResource{client: newTagTestClientWithOptions(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get("X-Tenant-Id") != "other-workspace" {
					t.Errorf("workspace = %q", req.Header.Get("X-Tenant-Id"))
				}
				if req.Method == http.MethodPost {
					var payload struct {
						Connection map[string]any `json:"connection"`
					}
					decodeJSON(t, req, &payload)
					wantFields := 1
					if manual {
						wantFields = 2
					}
					if len(payload.Connection) != wantFields || payload.Connection["type"] != "oauth" {
						t.Errorf("connection = %#v", payload.Connection)
					}
					if manual && payload.Connection["agent_auth_connection_slug"] != "existing-oauth" {
						t.Errorf("manual connection = %#v", payload.Connection)
					}
					writeJSON(t, w, server)
					return
				}
				pages++
				switch req.URL.Query().Get("cursor") {
				case "":
					writeJSON(t, w, map[string]any{"items": []any{}, "next_cursor": "opaque+/="})
				case "opaque+/=":
					writeJSON(t, w, map[string]any{"items": []managedToolServerAPI{server}, "next_cursor": nil})
				default:
					t.Errorf("cursor = %q", req.URL.Query().Get("cursor"))
				}
			}), option.WithTenantID("provider-workspace"))}
			state := managedToolServerTestState(t, r, model)
			created := resource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan(state)}, &created)
			if created.Diagnostics.HasError() {
				t.Fatal(created.Diagnostics)
			}
			var createdModel managedToolServerModel
			if diags := created.State.Get(ctx, &createdModel); diags.HasError() {
				t.Fatal(diags)
			}
			if !createdModel.AgentAuthConnectionSlug.Equal(model.AgentAuthConnectionSlug) {
				t.Fatalf("created slug = %s, want %s", createdModel.AgentAuthConnectionSlug, model.AgentAuthConnectionSlug)
			}
			response := resource.ReadResponse{State: created.State}
			r.Read(ctx, resource.ReadRequest{State: created.State}, &response)
			if response.Diagnostics.HasError() {
				t.Fatal(response.Diagnostics)
			}
			var actual managedToolServerModel
			if diags := response.State.Get(ctx, &actual); diags.HasError() {
				t.Fatal(diags)
			}
			if pages != 2 || !actual.AgentAuthConnectionSlug.Equal(model.AgentAuthConnectionSlug) {
				t.Fatalf("pages=%d slug=%s", pages, actual.AgentAuthConnectionSlug)
			}
			attrs := actual.OAuthSetup.Attributes()
			if mustAs[types.String](t, attrs["callback_url"]).ValueString() != "https://auth.example.com/callback" || !mustAs[types.Bool](t, attrs["pkce"]).ValueBool() || len(mustAs[types.Set](t, attrs["scopes"]).Elements()) != 1 || !attrs["oauth_provider_id"].IsNull() {
				t.Fatalf("setup = %v", attrs)
			}
		})
	}
}

func TestManagedToolServerReadFailuresAndDeletion(t *testing.T) {
	for _, tc := range []struct {
		name, body             string
		status                 int
		wantError, wantRemoved bool
	}{
		{name: "external deletion", body: `{"items":[],"next_cursor":null}`, wantRemoved: true},
		{name: "repeated cursor", body: `{"items":[],"next_cursor":"same"}`, wantError: true},
		{name: "empty cursor", body: `{"items":[],"next_cursor":""}`, wantError: true},
		{name: "malformed page", body: `{}`, wantError: true},
		{name: "list forbidden", status: 403, body: `{"detail":"forbidden"}`, wantError: true},
		{name: "list not found is not deletion", status: 404, body: `{"detail":"not found"}`, wantError: true},
		{name: "personal import rejected", body: `{"items":[{"id":"server-id","owner":"workspace","scope":"personal","connection":{"type":"none"}}]}`, wantError: true},
		{name: "builtin import rejected", body: `{"items":[{"id":"server-id","owner":"langsmith","connection":{"type":"none"}}]}`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			r := &managedToolServerResource{client: newTagTestClientWithOptions(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write([]byte(tc.body))
			}), option.WithMaxRetries(0))}
			state := managedToolServerTestState(t, r, managedToolServerTestModel())
			response := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &response)
			if response.Diagnostics.HasError() != tc.wantError || response.State.Raw.IsNull() != tc.wantRemoved {
				t.Fatalf("diagnostics=%v removed=%t", response.Diagnostics, response.State.Raw.IsNull())
			}
			if requests > 2 {
				t.Fatalf("pagination did not stop: requests=%d", requests)
			}
		})
	}
}

func TestManagedToolServerPaginationFailure(t *testing.T) {
	requests := 0
	r := &managedToolServerResource{client: newTagTestClientWithOptions(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		if req.URL.Query().Get("cursor") == "" {
			writeJSON(t, w, map[string]any{"items": []any{}, "next_cursor": "next"})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"detail":"list unavailable"}`))
	}), option.WithMaxRetries(0))}
	state := managedToolServerTestState(t, r, managedToolServerTestModel())
	response := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &response)
	if !response.Diagnostics.HasError() || response.State.Raw.IsNull() || requests != 2 {
		t.Fatalf("diagnostics=%v removed=%t requests=%d", response.Diagnostics, response.State.Raw.IsNull(), requests)
	}
}

func TestManagedToolServerDeleteNotFound(t *testing.T) {
	r := &managedToolServerResource{client: newTagTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"not found"}`))
	}))}
	state := managedToolServerTestState(t, r, managedToolServerTestModel())
	response := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &response)
	if response.Diagnostics.HasError() {
		t.Fatal(response.Diagnostics)
	}
}

func TestManagedToolServerImmutableConfiguration(t *testing.T) {
	ctx := context.Background()
	r := &managedToolServerResource{}
	state := managedToolServerTestState(t, r, managedToolServerTestModel())
	var schemaResponse resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
	for _, name := range []string{"name", "upstream_url", "connection_type", "agent_auth_connection_slug", "workspace_id"} {
		t.Run(name, func(t *testing.T) {
			attribute := mustAs[schema.StringAttribute](t, schemaResponse.Schema.Attributes[name])
			request := planmodifier.StringRequest{Path: path.Root(name), State: state, StateValue: types.StringValue("before"), Plan: tfsdk.Plan(state), PlanValue: types.StringValue("after"), ConfigValue: types.StringValue("after")}
			response := planmodifier.StringResponse{PlanValue: request.PlanValue}
			for _, modifier := range attribute.PlanModifiers {
				modifier.PlanModifyString(ctx, request, &response)
			}
			if !response.RequiresReplace {
				t.Fatalf("%s change does not require replacement", name)
			}
		})
	}
	if !schemaResponse.Schema.Attributes["enabled"].IsComputed() || schemaResponse.Schema.Attributes["enabled"].IsOptional() {
		t.Fatal("enabled must be computed only")
	}
}

func TestManagedToolServerProtocolPlanning(t *testing.T) {
	ctx := context.Background()
	server, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatal(err)
	}
	r := &managedToolServerResource{}
	prior := managedToolServerTestModel()
	prior.URL = types.StringValue("/v1/managed-tools/servers/server-id/mcp")
	prior.Enabled = types.BoolValue(true)
	priorState := managedToolServerTestState(t, r, prior)
	dynamic := func(state tfsdk.State) *tfprotov6.DynamicValue {
		value, err := tfprotov6.NewDynamicValue(state.Raw.Type(), state.Raw)
		if err != nil {
			t.Fatal(err)
		}
		return &value
	}
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-op", true: "replacement"}[changed], func(t *testing.T) {
			config := prior
			config.ID = types.StringNull()
			config.URL = types.StringNull()
			config.Enabled = types.BoolNull()
			proposed := prior
			if changed {
				config.Name = types.StringValue("New-MCP")
				proposed.Name = config.Name
			}
			response, err := server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{
				TypeName:         "langsmith_managed_tool_server",
				PriorState:       dynamic(priorState),
				ProposedNewState: dynamic(managedToolServerTestState(t, r, proposed)),
				Config:           dynamic(managedToolServerTestState(t, r, config)),
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, diagnostic := range response.Diagnostics {
				if diagnostic.Severity == tfprotov6.DiagnosticSeverityError {
					t.Fatalf("plan diagnostic: %s: %s", diagnostic.Summary, diagnostic.Detail)
				}
			}
			if changed {
				if len(response.RequiresReplace) == 0 {
					t.Fatal("name change did not plan replacement")
				}
				return
			}
			planned, err := response.PlannedState.Unmarshal(priorState.Raw.Type())
			if err != nil {
				t.Fatal(err)
			}
			if !planned.Equal(priorState.Raw) || len(response.RequiresReplace) != 0 {
				t.Fatalf("no-op plan changed state: %s; replacement=%v", planned, response.RequiresReplace)
			}
		})
	}
}

func TestManagedToolServerImportInvalid(t *testing.T) {
	for _, id := range []string{"", "/server", "workspace/", "workspace/server/extra"} {
		t.Run(strings.ReplaceAll(id, "/", "_"), func(t *testing.T) {
			r := &managedToolServerResource{}
			state := managedToolServerTestState(t, r, managedToolServerTestModel())
			response := resource.ImportStateResponse{State: tfsdk.State{Schema: state.Schema, Raw: tftypes.NewValue(state.Raw.Type(), nil)}}
			r.ImportState(context.Background(), resource.ImportStateRequest{ID: id}, &response)
			if !response.Diagnostics.HasError() {
				t.Fatalf("accepted import %q", id)
			}
		})
	}
}

func TestManagedToolServerSecretConnection(t *testing.T) {
	ctx := context.Background()
	model := managedToolServerTestModel()
	model.ConnectionType = types.StringValue("secret")
	model.SecretHeaders = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("X-Api-Key"), types.StringValue("Authorization")})
	server := managedToolServerAPI{ID: "server-id", Name: "Example-MCP", UpstreamURL: "https://mcp.example.com/mcp", Owner: "workspace", Scope: "workspace", Enabled: true, Connection: managedToolServerConnectionAPI{Type: "secret", Fields: []managedToolServerSecretField{
		{Name: "Authorization", Injection: managedToolServerSecretInjection{Type: "header", Name: "Authorization"}},
		{Name: "X-Api-Key", Injection: managedToolServerSecretInjection{Type: "header", Name: "X-Api-Key"}},
	}}}
	r := &managedToolServerResource{client: newTagTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			var payload map[string]json.RawMessage
			decodeJSON(t, req, &payload)
			want := `{"type":"secret","fields":[{"name":"Authorization","injection":{"type":"header","name":"Authorization"}},{"name":"X-Api-Key","injection":{"type":"header","name":"X-Api-Key"}}]}`
			if string(payload["connection"]) != want {
				t.Errorf("connection = %s, want %s", payload["connection"], want)
			}
			if _, ok := payload["credential"]; ok {
				t.Error("create must not send a credential")
			}
			writeJSON(t, w, server)
			return
		}
		writeJSON(t, w, map[string]any{"items": []managedToolServerAPI{server}, "next_cursor": nil})
	}))}
	state := managedToolServerTestState(t, r, model)
	created := resource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan(state)}, &created)
	if created.Diagnostics.HasError() {
		t.Fatal(created.Diagnostics)
	}
	read := resource.ReadResponse{State: created.State}
	r.Read(ctx, resource.ReadRequest{State: created.State}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	var actual managedToolServerModel
	if diags := read.State.Get(ctx, &actual); diags.HasError() {
		t.Fatal(diags)
	}
	if actual.ConnectionType.ValueString() != "secret" || !actual.SecretHeaders.Equal(model.SecretHeaders) || !actual.OAuthSetup.IsNull() {
		t.Fatalf("read = %#v", actual)
	}
}

func TestManagedToolServerSecretHeadersValidation(t *testing.T) {
	headers := types.SetValueMust(types.StringType, []attr.Value{types.StringValue("Authorization")})
	for _, tc := range []struct {
		name           string
		connectionType string
		headers        types.Set
		wantError      bool
	}{
		{name: "secret with headers", connectionType: "secret", headers: headers},
		{name: "secret without headers", connectionType: "secret", headers: types.SetNull(types.StringType), wantError: true},
		{name: "oauth with headers", connectionType: "oauth", headers: headers, wantError: true},
		{name: "public without headers", connectionType: "none", headers: types.SetNull(types.StringType)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &managedToolServerResource{}
			model := managedToolServerTestModel()
			model.ConnectionType = types.StringValue(tc.connectionType)
			model.SecretHeaders = tc.headers
			state := managedToolServerTestState(t, r, model)
			response := resource.ValidateConfigResponse{}
			r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: tfsdk.Config(state)}, &response)
			if response.Diagnostics.HasError() != tc.wantError {
				t.Fatalf("diagnostics = %v", response.Diagnostics)
			}
		})
	}
}
