package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func oauthConnectionTestModel() oauthConnectionModel {
	return oauthConnectionModel{
		Slug: types.StringValue("example-oauth"), DisplayName: types.StringValue("Example OAuth"),
		ClientID: types.StringValue("example-client"), ClientSecretEnv: types.StringValue("TF_TEST_OAUTH_CLIENT_SECRET"),
		ClientSecretVersion:     types.StringValue("revision-1"),
		AuthorizationURL:        types.StringValue("https://identity.example.com/authorize"),
		TokenURL:                types.StringValue("https://identity.example.com/token"),
		TokenEndpointAuthMethod: types.StringValue("client_secret_basic"),
		DefaultScopes:           oauthScopeSet([]string{"tools:read"}), AllowedScopes: oauthScopeSet([]string{"tools:read", "tools:write"}),
	}
}

func oauthConnectionTestAPI() oauthConnectionAPI {
	result := oauthConnectionAPI{ID: "connection-id", Slug: "example-oauth", DisplayName: "Example OAuth"}
	result.Credential.Kind = "oauth2"
	result.Credential.OAuthProviderID = "provider-id"
	return result
}

func oauthConnectionTestResource(t *testing.T, handler http.HandlerFunc) *oauthConnectionResource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &oauthConnectionResource{client: langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("test-key"), option.WithTenantID("provider-workspace"), option.WithMaxRetries(0))}
}

func oauthConnectionTestSchema() schema.Schema {
	var response resource.SchemaResponse
	NewOAuthConnectionResource().Schema(context.Background(), resource.SchemaRequest{}, &response)
	return response.Schema
}

func oauthConnectionTestPlan(t *testing.T, model oauthConnectionModel) tfsdk.Plan {
	t.Helper()
	plan := tfsdk.Plan{Schema: oauthConnectionTestSchema()}
	if diags := plan.Set(context.Background(), model); diags.HasError() {
		t.Fatal(diags)
	}
	return plan
}

func TestOAuthConnectionLifecycle(t *testing.T) {
	const secret = "synthetic-oauth-client-secret"
	t.Setenv("TF_TEST_OAUTH_CLIENT_SECRET", secret)
	for _, tc := range []struct {
		name, workspace, wantWorkspace string
	}{
		{name: "provider fallback", wantWorkspace: "provider-workspace"},
		{name: "workspace override", workspace: "resource-workspace", wantWorkspace: "resource-workspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			requests := 0
			res := oauthConnectionTestResource(t, func(w http.ResponseWriter, req *http.Request) {
				requests++
				wantWorkspace := tc.wantWorkspace
				if req.URL.Path == "/check-default" {
					wantWorkspace = "provider-workspace"
				}
				if got := req.Header.Get("X-Tenant-Id"); got != wantWorkspace {
					t.Errorf("workspace = %q, want %q", got, wantWorkspace)
				}
				switch req.Method + " " + req.URL.Path {
				case "POST /v1/agent-auth/connections":
					var payload struct {
						Slug        string `json:"slug"`
						DisplayName string `json:"display_name"`
						Credential  struct {
							Kind     string         `json:"kind"`
							Provider map[string]any `json:"provider"`
						} `json:"credential"`
					}
					decodeJSON(t, req, &payload)
					if payload.Slug != "example-oauth" || payload.DisplayName != "Example OAuth" || payload.Credential.Kind != "oauth2" {
						t.Error("incorrect connection create payload")
					}
					provider := payload.Credential.Provider
					if provider["client_secret"] != secret || provider["client_id"] != "example-client" || provider["token_endpoint_auth_method"] != "client_secret_basic" || provider["grant_type"] != "authorization_code" {
						t.Error("incorrect OAuth app configuration")
					}
					if provider["authorization_url"] != "https://identity.example.com/authorize" || provider["token_url"] != "https://identity.example.com/token" {
						t.Error("incorrect OAuth endpoints")
					}
					if got := mustAs[[]any](t, provider["default_scopes"]); len(got) != 1 || got[0] != "tools:read" {
						t.Errorf("default scopes = %v", got)
					}
					if got := mustAs[[]any](t, provider["allowed_scopes"]); len(got) != 2 {
						t.Errorf("allowed scopes = %v", got)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					writeJSON(t, w, oauthConnectionTestAPI())
				case "GET /v1/agent-auth/connections/connection-id":
					writeJSON(t, w, oauthConnectionTestAPI())
				case "GET /v1/agent-auth/oauth-providers/provider-id":
					writeJSON(t, w, oauthProviderAPI{
						ClientID: "changed-client", AuthorizationURL: "https://identity.example.com/new-authorize",
						TokenURL: "https://identity.example.com/new-token", TokenEndpointAuthMethod: "client_secret_post",
						GrantType: "authorization_code", DefaultScopes: []string{"changed-scope"}, AllowedScopes: []string{"changed-scope"},
					})
				case "PATCH /v1/agent-auth/oauth-providers/provider-id":
					var payload map[string]any
					decodeJSON(t, req, &payload)
					if _, ok := payload["client_secret"]; ok {
						t.Error("an unchanged secret reference must not resend the secret")
					}
					if payload["client_id"] != "changed-client" || payload["token_endpoint_auth_method"] != "client_secret_post" {
						t.Errorf("provider PATCH = %v", payload)
					}
					w.WriteHeader(http.StatusOK)
					writeJSON(t, w, map[string]any{})
				case "PATCH /v1/agent-auth/connections/connection-id":
					var payload map[string]any
					decodeJSON(t, req, &payload)
					if len(payload) != 1 || payload["display_name"] != "Renamed connection" {
						t.Errorf("PATCH must only update connection display name: %v", payload)
					}
					result := oauthConnectionTestAPI()
					result.DisplayName = "Renamed connection"
					writeJSON(t, w, result)
				case "DELETE /v1/agent-auth/connections/connection-id", "GET /check-default":
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			plan := oauthConnectionTestModel()
			if tc.workspace != "" {
				plan.WorkspaceID = types.StringValue(tc.workspace)
			}
			created := resource.CreateResponse{State: tfsdk.State{Schema: oauthConnectionTestSchema()}}
			res.Create(ctx, resource.CreateRequest{Plan: oauthConnectionTestPlan(t, plan)}, &created)
			if created.Diagnostics.HasError() {
				t.Fatal(created.Diagnostics)
			}
			if strings.Contains(created.State.Raw.String(), secret) {
				t.Fatal("secret was persisted to state")
			}
			var model oauthConnectionModel
			if diags := created.State.Get(ctx, &model); diags.HasError() {
				t.Fatal(diags)
			}
			if model.ID.ValueString() != "connection-id" || model.OAuthProviderID.ValueString() != "provider-id" || !model.WorkspaceID.Equal(plan.WorkspaceID) {
				t.Fatal("create did not preserve workspace and returned identities")
			}
			read := resource.ReadResponse{State: created.State}
			res.Read(ctx, resource.ReadRequest{State: created.State}, &read)
			if read.Diagnostics.HasError() {
				t.Fatal(read.Diagnostics)
			}
			if diags := read.State.Get(ctx, &model); diags.HasError() {
				t.Fatal(diags)
			}
			if model.ClientID.ValueString() != "changed-client" || model.TokenEndpointAuthMethod.ValueString() != "client_secret_post" || model.TokenURL.ValueString() != "https://identity.example.com/new-token" || model.AuthorizationURL.ValueString() != "https://identity.example.com/new-authorize" || !model.DefaultScopes.Equal(oauthScopeSet([]string{"changed-scope"})) || !model.AllowedScopes.Equal(oauthScopeSet([]string{"changed-scope"})) {
				t.Fatal("provider configuration drift not refreshed")
			}
			if !model.ClientSecretEnv.Equal(plan.ClientSecretEnv) || !model.ClientSecretVersion.Equal(plan.ClientSecretVersion) {
				t.Fatal("read did not preserve local secret references")
			}
			model.DisplayName = types.StringValue("Renamed connection")
			updated := resource.UpdateResponse{State: read.State}
			res.Update(ctx, resource.UpdateRequest{Plan: oauthConnectionTestPlan(t, model), State: read.State}, &updated)
			if updated.Diagnostics.HasError() {
				t.Fatal(updated.Diagnostics)
			}
			var deleted resource.DeleteResponse
			res.Delete(ctx, resource.DeleteRequest{State: updated.State}, &deleted)
			if deleted.Diagnostics.HasError() {
				t.Fatal(deleted.Diagnostics)
			}
			if err := res.client.Get(ctx, "check-default", nil, nil); err != nil {
				t.Fatal(err)
			}
			if requests != 7 {
				t.Errorf("requests = %d, want 7", requests)
			}
		})
	}
}

func TestOAuthConnectionSecretValidation(t *testing.T) {
	t.Setenv("TF_TEST_OAUTH_EMPTY_SECRET", "")
	for _, tc := range []struct {
		name, env, method string
		wantError         error
	}{
		{name: "missing reference", method: "client_secret_basic", wantError: errOAuthSecretEnv},
		{name: "empty environment value", env: "TF_TEST_OAUTH_EMPTY_SECRET", method: "client_secret_post", wantError: errOAuthSecretEnv},
		{name: "public client rejects secret reference", env: "TF_TEST_OAUTH_EMPTY_SECRET", method: "none", wantError: errOAuthPublicClientSecret},
		{name: "public client", method: "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			res := oauthConnectionTestResource(t, func(w http.ResponseWriter, req *http.Request) {
				requests++
				var payload map[string]any
				decodeJSON(t, req, &payload)
				provider := mustAs[map[string]any](t, mustAs[map[string]any](t, payload["credential"])["provider"])
				if _, exists := provider["client_secret"]; exists {
					t.Error("public client must not send a client secret")
				}
				writeJSON(t, w, oauthConnectionTestAPI())
			})
			plan := oauthConnectionTestModel()
			plan.ClientSecretEnv = types.StringNull()
			if tc.env != "" {
				plan.ClientSecretEnv = types.StringValue(tc.env)
			}
			plan.TokenEndpointAuthMethod = types.StringValue(tc.method)
			_, err := res.createOAuthConnection(context.Background(), plan)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("error = %v, want %v", err, tc.wantError)
			}
			if tc.wantError != nil && requests != 0 {
				t.Fatal("invalid credentials must not cause a network request")
			}
		})
	}
}

func TestOAuthConnectionErrorsSuppressSecrets(t *testing.T) {
	const secret = "synthetic-secret-that-must-not-be-disclosed"
	t.Setenv("TF_TEST_OAUTH_CLIENT_SECRET", secret)
	res := oauthConnectionTestResource(t, func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": secret})
	})
	var response resource.CreateResponse
	res.Create(context.Background(), resource.CreateRequest{Plan: oauthConnectionTestPlan(t, oauthConnectionTestModel())}, &response)
	if !response.Diagnostics.HasError() || strings.Contains(fmt.Sprint(response.Diagnostics), secret) || !strings.Contains(fmt.Sprint(response.Diagnostics), "HTTP 400") {
		t.Fatal("API error must report status without echoing the response body")
	}
	if got := oauthConnectionError(fmt.Errorf("transport error with %s", secret)); strings.Contains(got, secret) {
		t.Fatal("transport error disclosed credentials")
	}
}

func TestOAuthConnectionMissingAndInvalidRead(t *testing.T) {
	for _, tc := range []struct {
		name                                                      string
		connectionMissing, providerMissing, wrongKind, wrongGrant bool
	}{
		{name: "connection deleted", connectionMissing: true},
		{name: "provider missing", providerMissing: true},
		{name: "secret connection", wrongKind: true},
		{name: "unsupported grant", wrongGrant: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := oauthConnectionTestResource(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == http.MethodDelete || tc.connectionMissing || (tc.providerMissing && strings.Contains(req.URL.Path, "oauth-providers")) {
					http.NotFound(w, req)
					return
				}
				if strings.Contains(req.URL.Path, "oauth-providers") {
					writeJSON(t, w, oauthProviderAPI{GrantType: "client_credentials"})
					return
				}
				result := oauthConnectionTestAPI()
				if tc.wrongKind {
					result.Credential.Kind = "secret"
				}
				writeJSON(t, w, result)
			})
			ctx := context.Background()
			model := oauthConnectionTestModel()
			model.ID = types.StringValue("connection-id")
			state := tfsdk.State{Schema: oauthConnectionTestSchema()}
			if diags := state.Set(ctx, model); diags.HasError() {
				t.Fatal(diags)
			}
			response := resource.ReadResponse{State: state}
			res.Read(ctx, resource.ReadRequest{State: state}, &response)
			if tc.connectionMissing {
				if response.Diagnostics.HasError() || !response.State.Raw.IsNull() {
					t.Fatal("missing connection must be removed from state")
				}
			} else if !response.Diagnostics.HasError() || response.State.Raw.IsNull() {
				t.Fatal("invalid provider or connection must report an error without dropping state")
			}
			if err := res.deleteOAuthConnection(ctx, model); err != nil {
				t.Fatalf("delete 404 must succeed: %v", err)
			}
		})
	}
}

func TestOAuthConnectionImports(t *testing.T) {
	for _, tc := range []struct {
		id, workspace, connection string
		valid                     bool
	}{
		{id: "connection-id", connection: "connection-id", valid: true},
		{id: "resource-workspace/connection-id", workspace: "resource-workspace", connection: "connection-id", valid: true},
		{id: ""}, {id: "/connection-id"}, {id: "workspace/"}, {id: "workspace/id/extra"},
	} {
		t.Run(strings.ReplaceAll(tc.id, "/", "_"), func(t *testing.T) {
			ctx := context.Background()
			res := oauthConnectionTestResource(t, func(w http.ResponseWriter, req *http.Request) {
				wantWorkspace := tc.workspace
				if wantWorkspace == "" {
					wantWorkspace = "provider-workspace"
				}
				if req.Header.Get("X-Tenant-Id") != wantWorkspace {
					t.Error("import refresh did not preserve workspace routing")
				}
				if strings.Contains(req.URL.Path, "oauth-providers") {
					writeJSON(t, w, oauthProviderAPI{ClientID: "imported-client", GrantType: "authorization_code"})
					return
				}
				writeJSON(t, w, oauthConnectionTestAPI())
			})
			s := oauthConnectionTestSchema()
			response := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}}
			res.ImportState(ctx, resource.ImportStateRequest{ID: tc.id}, &response)
			if !tc.valid {
				if !response.Diagnostics.HasError() {
					t.Fatal("invalid import ID accepted")
				}
				return
			}
			var id, workspace types.String
			response.Diagnostics.Append(response.State.GetAttribute(ctx, path.Root("id"), &id)...)
			response.Diagnostics.Append(response.State.GetAttribute(ctx, path.Root("workspace_id"), &workspace)...)
			if response.Diagnostics.HasError() || id.ValueString() != tc.connection || workspace.ValueString() != tc.workspace {
				t.Fatal("import identity or workspace mismatch")
			}
			read := resource.ReadResponse{State: response.State}
			res.Read(ctx, resource.ReadRequest{State: response.State}, &read)
			if read.Diagnostics.HasError() {
				t.Fatal(read.Diagnostics)
			}
			var model oauthConnectionModel
			if diags := read.State.Get(ctx, &model); diags.HasError() {
				t.Fatal(diags)
			}
			if model.ClientID.ValueString() != "imported-client" || model.Slug.ValueString() != "example-oauth" || model.OAuthProviderID.ValueString() != "provider-id" || !model.ClientSecretEnv.IsNull() || !model.ClientSecretVersion.IsNull() {
				t.Fatal("import must recover public settings without inventing secret references")
			}
			if model.DefaultScopes.IsNull() || model.AllowedScopes.IsNull() {
				t.Fatal("empty scopes must refresh as empty sets")
			}
		})
	}
}

func TestOAuthConnectionValidateConfig(t *testing.T) {
	for _, tc := range []struct {
		name        string
		method, env types.String
		wantError   bool
	}{
		{name: "confidential requires reference", method: types.StringValue("client_secret_basic"), env: types.StringNull(), wantError: true},
		{name: "confidential configured", method: types.StringValue("client_secret_post"), env: types.StringValue("OAUTH_SECRET")},
		{name: "public rejects reference", method: types.StringValue("none"), env: types.StringValue("OAUTH_SECRET"), wantError: true},
		{name: "public client", method: types.StringValue("none"), env: types.StringNull()},
		{name: "unknown reference", method: types.StringValue("client_secret_basic"), env: types.StringUnknown()},
		{name: "unknown method", method: types.StringUnknown(), env: types.StringNull()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := oauthConnectionTestModel()
			model.TokenEndpointAuthMethod = tc.method
			model.ClientSecretEnv = tc.env
			plan := oauthConnectionTestPlan(t, model)
			config := tfsdk.Config(plan)
			var response resource.ValidateConfigResponse
			mustAs[*oauthConnectionResource](t, NewOAuthConnectionResource()).ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: config}, &response)
			if response.Diagnostics.HasError() != tc.wantError {
				t.Fatalf("validation diagnostics = %v, want error %t", response.Diagnostics, tc.wantError)
			}
		})
	}
}

func TestOAuthConnectionReplacement(t *testing.T) {
	ctx := context.Background()
	model := oauthConnectionTestModel()
	state := tfsdk.State{Schema: oauthConnectionTestSchema()}
	if diags := state.Set(ctx, model); diags.HasError() {
		t.Fatal(diags)
	}
	plan := oauthConnectionTestPlan(t, model)
	for _, name := range []string{"workspace_id", "slug", "display_name", "client_id", "authorization_url", "token_url", "token_endpoint_auth_method", "client_secret_env", "client_secret_version"} {
		t.Run(name, func(t *testing.T) {
			attribute := mustAs[schema.StringAttribute](t, oauthConnectionTestSchema().Attributes[name])
			req := planmodifier.StringRequest{State: state, Plan: plan, StateValue: types.StringValue("previous"), PlanValue: types.StringValue("changed"), ConfigValue: types.StringValue("changed")}
			var response planmodifier.StringResponse
			for _, modifier := range attribute.PlanModifiers {
				modifier.PlanModifyString(ctx, req, &response)
			}
			want := name == "workspace_id" || name == "slug"
			if response.Diagnostics.HasError() || response.RequiresReplace != want {
				t.Fatalf("changing %s: replace = %t, want %t: %v", name, response.RequiresReplace, want, response.Diagnostics)
			}
		})
	}
	for _, name := range []string{"default_scopes", "allowed_scopes"} {
		attribute := mustAs[schema.SetAttribute](t, oauthConnectionTestSchema().Attributes[name])
		req := planmodifier.SetRequest{State: state, Plan: plan, StateValue: oauthScopeSet([]string{"old"}), PlanValue: oauthScopeSet([]string{"new"}), ConfigValue: oauthScopeSet([]string{"new"})}
		var response planmodifier.SetResponse
		for _, modifier := range attribute.PlanModifiers {
			modifier.PlanModifySet(ctx, req, &response)
		}
		if response.Diagnostics.HasError() || response.RequiresReplace {
			t.Fatalf("changing %s must update in place: %v", name, response.Diagnostics)
		}
	}
}

func TestOAuthConnectionUpdateSecret(t *testing.T) {
	const secret = "synthetic-rotated-secret"
	t.Setenv("TF_TEST_OAUTH_CLIENT_SECRET", secret)
	public := func(m *oauthConnectionModel) {
		m.TokenEndpointAuthMethod = types.StringValue("none")
		m.ClientSecretEnv, m.ClientSecretVersion = types.StringNull(), types.StringNull()
	}
	for _, tc := range []struct {
		name          string
		before, after func(*oauthConnectionModel)
		want          any // nil: omitted; "clear": JSON null; otherwise the secret
		wantErr       bool
	}{
		{name: "scope change keeps secret", after: func(m *oauthConnectionModel) { m.DefaultScopes = oauthScopeSet([]string{"tools:write"}) }},
		{name: "version change rotates", after: func(m *oauthConnectionModel) { m.ClientSecretVersion = types.StringValue("revision-2") }, want: secret},
		{name: "env change rotates", before: func(m *oauthConnectionModel) { m.ClientSecretEnv = types.StringValue("OLD_ENV") }, want: secret},
		{name: "public to confidential sends secret", before: public, want: secret},
		{name: "confidential to public clears secret", after: public, want: "clear"},
		{name: "missing secret fails", after: func(m *oauthConnectionModel) { m.ClientSecretEnv = types.StringValue("TF_TEST_OAUTH_UNSET") }, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, plan := oauthConnectionTestModel(), oauthConnectionTestModel()
			state.ID, state.OAuthProviderID = types.StringValue("connection-id"), types.StringValue("provider-id")
			plan.ID, plan.OAuthProviderID = state.ID, state.OAuthProviderID
			if tc.before != nil {
				tc.before(&state)
			}
			if tc.after != nil {
				tc.after(&plan)
			}
			patched := false
			res := oauthConnectionTestResource(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodPatch || req.URL.Path != "/v1/agent-auth/oauth-providers/provider-id" {
					t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
				}
				patched = true
				var payload map[string]json.RawMessage
				decodeJSON(t, req, &payload)
				raw, ok := payload["client_secret"]
				switch tc.want {
				case nil:
					if ok {
						t.Error("secret must be omitted")
					}
				case "clear":
					if string(raw) != "null" {
						t.Errorf("client_secret = %s, want null", raw)
					}
				default:
					if string(raw) != fmt.Sprintf("%q", tc.want) {
						t.Error("rotated secret not sent")
					}
				}
				writeJSON(t, w, map[string]any{})
			})
			stateValue := tfsdk.State{Schema: oauthConnectionTestSchema()}
			if diags := stateValue.Set(context.Background(), state); diags.HasError() {
				t.Fatal(diags)
			}
			response := resource.UpdateResponse{State: stateValue}
			res.Update(context.Background(), resource.UpdateRequest{Plan: oauthConnectionTestPlan(t, plan), State: stateValue}, &response)
			if response.Diagnostics.HasError() != tc.wantErr {
				t.Fatalf("diagnostics = %v", response.Diagnostics)
			}
			if tc.wantErr {
				if patched {
					t.Error("a failed secret lookup must not patch the provider")
				}
				for _, diagnostic := range response.Diagnostics {
					if strings.Contains(diagnostic.Detail(), secret) {
						t.Fatal("secret leaked into diagnostics")
					}
				}
			}
		})
	}
}
