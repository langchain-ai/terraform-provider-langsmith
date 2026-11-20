package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestAuthServerCRUDWorkspace(t *testing.T) {
	ctx := t.Context()
	for _, workspace := range []types.String{types.StringNull(), types.StringValue("override")} {
		t.Run(workspace.ValueString(), func(t *testing.T) {
			expected := "default"
			if !workspace.IsNull() {
				expected = "override"
			}
			var methods []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				methods = append(methods, req.Method)
				if req.Header.Get("X-Tenant-Id") != expected {
					t.Errorf("workspace = %q", req.Header.Get("X-Tenant-Id"))
				}
				wantPath := "/v1/agent-auth/oauth-providers"
				if req.Method != "POST" {
					wantPath += "/server-id"
				}
				if req.URL.Path != wantPath {
					t.Errorf("path = %s", req.URL.Path)
				}
				if req.Method == "POST" || req.Method == "PATCH" {
					var payload map[string]interface{}
					if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					if payload["client_secret"] != "secret" {
						t.Error("secret missing")
					}
					if _, exists := payload["slug"]; exists != (req.Method == "POST") {
						t.Error("slug mutation")
					}
				}
				w.Header().Set("Content-Type", "application/json")
				if req.Method == "DELETE" {
					w.WriteHeader(204)
					return
				}
				if req.Method == "POST" {
					w.WriteHeader(201)
				}
				if err := json.NewEncoder(w).Encode(authServerAPI{ID: "server-id", Slug: "test", DisplayName: "Test", ClientID: "client", TokenURL: "https://example.com/token", TokenEndpointAuthMethod: "client_secret_post", GrantType: "client_credentials", HasClientSecret: true}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			r := &AuthServerResource{client: langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("key"), option.WithTenantID("default"))}
			headers, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"X-Key": "hidden"})
			plan := authServerResourceModel{WorkspaceID: workspace, Slug: types.StringValue("test"), DisplayName: types.StringValue("Test"), ClientID: types.StringValue("client"), ClientSecret: types.StringValue("secret"), TokenRequestHeaders: headers, TokenURL: types.StringValue("https://example.com/token"), TokenEndpointAuthMethod: types.StringValue("client_secret_post"), GrantType: types.StringValue("client_credentials")}
			created, err := r.createAuthServer(ctx, plan)
			if err != nil {
				t.Fatal(err)
			}
			read, err := r.readAuthServer(ctx, "server-id", created, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if !read.ClientSecret.Equal(plan.ClientSecret) || !read.TokenRequestHeaders.Equal(headers) || !read.WorkspaceID.Equal(workspace) {
				t.Fatal("hidden credentials/workspace lost")
			}
			if _, err := r.updateAuthServer(ctx, "server-id", read, workspace, read); err != nil {
				t.Fatal(err)
			}
			if err := r.deleteAuthServer(ctx, "server-id", workspace); err != nil {
				t.Fatal(err)
			}
			if strings.Join(methods, ",") != "POST,GET,PATCH,DELETE" {
				t.Fatal(methods)
			}
		})
	}
}
func TestAuthServerErrors(t *testing.T) {
	for _, status := range []int{404, 403} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(status) }))
		r := &AuthServerResource{client: langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("key"), option.WithMaxRetries(0))}
		_, err := r.readAuthServer(t.Context(), "id", authServerResourceModel{})
		if err == nil {
			t.Fatal("read must fail")
		}
		if status == 404 && !isLangSmithNotFound(err) {
			t.Fatal(err)
		}
		err = r.deleteAuthServer(t.Context(), "id")
		if (err == nil) != (status == 404) {
			t.Fatalf("delete error = %v", err)
		}
		server.Close()
	}
}
func TestAuthServerPayloadClearsRemovedSecrets(t *testing.T) {
	p := authServerPayloadFromModel(t.Context(), authServerResourceModel{}, true)
	if value, ok := p["client_secret"]; !ok || value != nil {
		t.Fatal("secret not cleared")
	}
	headers, ok := p["token_request_headers"].(map[string]string)
	if !ok || len(headers) != 0 {
		t.Fatal("headers not cleared")
	}
}
func TestAuthServerResourceImports(t *testing.T) {
	ctx := t.Context()
	r := &AuthServerResource{}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	for _, tc := range []struct {
		id, workspace, name string
		valid               bool
	}{
		{id: "docker-hub", name: "docker-hub", valid: true},
		{id: "workspace-id/docker-hub", workspace: "workspace-id", name: "docker-hub", valid: true},
		{id: ""}, {id: "/docker-hub"}, {id: "workspace-id/"},
	} {
		t.Run(strings.ReplaceAll(tc.id, "/", "_"), func(t *testing.T) {
			resp := resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)}}
			r.ImportState(ctx, resource.ImportStateRequest{ID: tc.id}, &resp)
			if !tc.valid {
				if !resp.Diagnostics.HasError() {
					t.Fatalf("ImportState(%q) diagnostics = %v", tc.id, resp.Diagnostics)
				}
				return
			}
			var workspace, name types.String
			resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("workspace_id"), &workspace)...)
			resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("id"), &name)...)
			if resp.Diagnostics.HasError() || workspace.ValueString() != tc.workspace || name.ValueString() != tc.name {
				t.Fatalf("ImportState(%q) = workspace %q, name %q, diagnostics %v", tc.id, workspace.ValueString(), name.ValueString(), resp.Diagnostics)
			}
		})
	}
}

func TestAuthServerSchema(t *testing.T) {
	var response resource.SchemaResponse
	NewAuthServerResource().Schema(t.Context(), resource.SchemaRequest{}, &response)
	for _, name := range []string{"client_secret", "token_request_headers"} {
		if !response.Schema.Attributes[name].IsSensitive() {
			t.Fatalf("%s must be sensitive", name)
		}
	}
	for _, name := range []string{"slug", "workspace_id"} {
		attribute, ok := response.Schema.Attributes[name].(schema.StringAttribute)
		if !ok || len(attribute.PlanModifiers) == 0 {
			t.Fatalf("%s must require replacement", name)
		}
	}
}
func TestAuthServerImportedUpdatePreservesUnconfiguredSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var payload map[string]interface{}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"client_secret", "token_request_headers"} {
			if _, exists := payload[field]; exists {
				t.Errorf("unconfigured imported %s must be preserved", field)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(authServerAPI{ID: "imported"}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	r := &AuthServerResource{client: langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("key"))}
	model := authServerResourceModel{ClientSecret: types.StringNull(), TokenRequestHeaders: types.MapNull(types.StringType)}
	if _, err := r.updateAuthServer(t.Context(), "imported", model, types.StringNull(), model); err != nil {
		t.Fatal(err)
	}
}

func TestAuthServerGitHubDefaults(t *testing.T) {
	ctx := t.Context()
	scopes, _ := types.ListValueFrom(ctx, types.StringType, []string{"issues:write", "contents:read"})
	for _, unknown := range []bool{false, true} {
		tokenURL := types.StringNull()
		defaultScopes := types.ListNull(types.StringType)
		if unknown {
			tokenURL = types.StringUnknown()
			defaultScopes = types.ListUnknown(types.StringType)
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			var payload map[string]interface{}
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			for _, field := range []string{"token_url", "default_scopes"} {
				if _, exists := payload[field]; exists {
					t.Errorf("%s must be omitted", field)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(authServerAPI{ID: "github", TokenURL: "https://api.github.com", DefaultScopes: []string{"issues:write", "contents:read"}, AllowedScopes: []string{"issues:write", "contents:read"}}); err != nil {
				t.Error(err)
			}
		}))
		r := &AuthServerResource{client: langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("key"))}
		plan := authServerResourceModel{TokenURL: tokenURL, DefaultScopes: defaultScopes, AllowedScopes: scopes, GrantType: types.StringValue("github_app")}
		created, err := r.createAuthServer(ctx, plan)
		if err != nil {
			t.Fatal(err)
		}
		if created.TokenURL.ValueString() != "https://api.github.com" || !created.DefaultScopes.Equal(scopes) || !created.AllowedScopes.Equal(scopes) {
			t.Fatalf("defaults or scope ordering lost: %#v", created)
		}
		updated, err := r.updateAuthServer(ctx, "github", plan, types.StringNull(), created)
		if err != nil {
			t.Fatal(err)
		}
		if !updated.DefaultScopes.Equal(scopes) {
			t.Fatal("update defaults lost")
		}
		server.Close()
	}
}

func TestAuthServerExplicitEmptyDefaultScopes(t *testing.T) {
	scopes, _ := types.ListValueFrom(t.Context(), types.StringType, []string{})
	for _, update := range []bool{false, true} {
		payload := authServerPayloadFromModel(t.Context(), authServerResourceModel{DefaultScopes: scopes}, update)
		value, ok := payload["default_scopes"].([]string)
		if !ok || value == nil || len(value) != 0 {
			t.Fatalf("explicit empty scopes not sent: %#v", payload)
		}
	}
	model := authServerModelFromAPI(authServerAPI{}, authServerResourceModel{DefaultScopes: types.ListUnknown(types.StringType)})
	if !model.DefaultScopes.Equal(scopes) {
		t.Fatal("empty default scopes must resolve to a known empty list")
	}
}

func TestAuthServerDefaultableSchema(t *testing.T) {
	var response resource.SchemaResponse
	NewAuthServerResource().Schema(t.Context(), resource.SchemaRequest{}, &response)
	for _, name := range []string{"token_url", "default_scopes"} {
		attribute := response.Schema.Attributes[name]
		if !attribute.IsOptional() || !attribute.IsComputed() {
			t.Fatalf("%s must be optional and computed", name)
		}
	}
}
