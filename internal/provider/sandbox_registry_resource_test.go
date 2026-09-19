package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestSandboxRegistryPayloadFromModel(t *testing.T) {
	payload := sandboxRegistryPayloadFromModel(sandboxRegistryResourceModel{
		Name:     types.StringValue("docker-hub"),
		URL:      types.StringValue("https://index.docker.io/v1/"),
		Username: types.StringValue("robot"),
		Password: types.StringValue("s3cret"),
	})
	if payload.Name != "docker-hub" || payload.URL != "https://index.docker.io/v1/" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.Username != "robot" || payload.Password != "s3cret" {
		t.Fatalf("creds = %q / %q", payload.Username, payload.Password)
	}
}

func TestSandboxRegistryModelFromAPIPreservesCredentials(t *testing.T) {
	previous := sandboxRegistryResourceModel{
		Username: types.StringValue("robot"),
		Password: types.StringValue("s3cret"),
	}
	next := sandboxRegistryModelFromAPI(sandboxRegistryAPI{
		ID:        "reg-id",
		Name:      "docker-hub",
		URL:       "https://index.docker.io/v1/",
		CreatedAt: "2026-06-24T00:00:00Z",
		UpdatedAt: "2026-06-24T00:00:00Z",
		CreatedBy: "user-1",
	}, previous)

	if next.Username.ValueString() != "robot" || next.Password.ValueString() != "s3cret" {
		t.Fatalf("credentials not preserved: %q / %q", next.Username.ValueString(), next.Password.ValueString())
	}
	if next.ID.ValueString() != "reg-id" || next.Name.ValueString() != "docker-hub" {
		t.Fatalf("computed/identity fields: %#v", next)
	}
	if next.URL.ValueString() != "https://index.docker.io/v1/" {
		t.Fatalf("URL = %q", next.URL.ValueString())
	}
	if next.CreatedBy.ValueString() != "user-1" {
		t.Fatalf("CreatedBy = %q", next.CreatedBy.ValueString())
	}
}

func TestSandboxRegistryModelFromAPINullsAbsentFields(t *testing.T) {
	next := sandboxRegistryModelFromAPI(sandboxRegistryAPI{ID: "reg-id", Name: "n", URL: "u"}, sandboxRegistryResourceModel{})
	if !next.UpdatedAt.IsNull() {
		t.Fatalf("UpdatedAt = %v, want null", next.UpdatedAt)
	}
	if !next.UpdatedBy.IsNull() {
		t.Fatalf("UpdatedBy = %v, want null", next.UpdatedBy)
	}
}

func newSandboxRegistryResourceWithServer(t *testing.T, handler http.Handler, opts ...option.RequestOption) *SandboxRegistryResource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &SandboxRegistryResource{
		client: langsmith.NewClient(append([]option.RequestOption{option.WithBaseURL(server.URL), option.WithAPIKey("test-key")}, opts...)...),
	}
}

func TestSandboxRegistryResourceMetadata(t *testing.T) {
	var resp resource.MetadataResponse
	NewSandboxRegistryResource().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "langsmith"}, &resp)
	if resp.TypeName != "langsmith_sandbox_registry" {
		t.Fatalf("TypeName = %q, want langsmith_sandbox_registry", resp.TypeName)
	}
}

func TestSandboxRegistryResourceSchemaMarksCredentialsSensitive(t *testing.T) {
	var resp resource.SchemaResponse
	NewSandboxRegistryResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	for _, name := range []string{"username", "password"} {
		attr, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Fatalf("schema missing %q", name)
		}
		if !attr.IsSensitive() {
			t.Fatalf("%q must be Sensitive", name)
		}
	}
}

func TestSandboxRegistryResourceSchemaWorkspaceRequiresReplace(t *testing.T) {
	var resp resource.SchemaResponse
	NewSandboxRegistryResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	attr, ok := resp.Schema.Attributes["workspace_id"]
	if !ok || !attr.IsOptional() {
		t.Fatalf("workspace_id = %#v", attr)
	}
	stringAttr, ok := attr.(schema.StringAttribute)
	if !ok || len(stringAttr.PlanModifiers) != 1 {
		t.Fatalf("workspace_id attribute = %#v", attr)
	}
}

func TestSandboxRegistryResourceCreatePostsPayloadAndCapturesID(t *testing.T) {
	requests := make([]string, 0, 1)
	res := newSandboxRegistryResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/sandboxes/registries":
			var payload sandboxRegistryPayload
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if payload.Name != "docker-hub" || payload.Password != "s3cret" {
				t.Fatalf("payload = %#v", payload)
			}
			writeJSON(t, w, sandboxRegistryAPI{
				ID: "reg-id", Name: "docker-hub", URL: "https://index.docker.io/v1/", CreatedAt: "2026-06-24T00:00:00Z",
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))

	model, err := res.createSandboxRegistry(context.Background(), sandboxRegistryResourceModel{
		Name:     types.StringValue("docker-hub"),
		URL:      types.StringValue("https://index.docker.io/v1/"),
		Username: types.StringValue("robot"),
		Password: types.StringValue("s3cret"),
	})
	if err != nil {
		t.Fatalf("createSandboxRegistry: %v", err)
	}
	if model.ID.ValueString() != "reg-id" {
		t.Fatalf("ID = %q", model.ID.ValueString())
	}
	if model.Password.ValueString() != "s3cret" {
		t.Fatalf("Password not stored from config")
	}
	if !reflect.DeepEqual(requests, []string{"POST /api/v2/sandboxes/registries"}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestSandboxRegistryResourceReadByNamePreservesCredentials(t *testing.T) {
	res := newSandboxRegistryResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/sandboxes/registries/docker-hub" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		writeJSON(t, w, sandboxRegistryAPI{ID: "reg-id", Name: "docker-hub", URL: "https://updated.example.com", UpdatedAt: "2026-06-24T01:00:00Z"})
	}))

	model, err := res.readSandboxRegistry(context.Background(), "docker-hub", sandboxRegistryResourceModel{
		Username: types.StringValue("robot"),
		Password: types.StringValue("s3cret"),
	})
	if err != nil {
		t.Fatalf("readSandboxRegistry: %v", err)
	}
	if model.URL.ValueString() != "https://updated.example.com" {
		t.Fatalf("URL not refreshed: %q", model.URL.ValueString())
	}
	if model.Password.ValueString() != "s3cret" || model.Username.ValueString() != "robot" {
		t.Fatalf("credentials not preserved")
	}
}

func TestSandboxRegistryResourceReadReturnsNotFound(t *testing.T) {
	res := newSandboxRegistryResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	_, err := res.readSandboxRegistry(context.Background(), "missing", sandboxRegistryResourceModel{})
	if !isLangSmithNotFound(err) {
		t.Fatalf("err = %v, want LangSmith 404", err)
	}
}

func TestSandboxRegistryResourceUpdateRenamesUsingOldNameInPath(t *testing.T) {
	requests := make([]string, 0, 1)
	res := newSandboxRegistryResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v2/sandboxes/registries/old-name":
			var payload sandboxRegistryPayload
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if payload.Name != "new-name" {
				t.Fatalf("payload.Name = %q, want new-name", payload.Name)
			}
			// The all-or-nothing rule means update must resend the full credential set.
			if payload.URL == "" || payload.Username != "robot" || payload.Password != "s3cret" {
				t.Fatalf("update must resend url+username+password, got %#v", payload)
			}
			writeJSON(t, w, sandboxRegistryAPI{ID: "reg-id", Name: "new-name", URL: payload.URL, UpdatedAt: "2026-06-24T02:00:00Z"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))

	model, err := res.updateSandboxRegistry(context.Background(), "old-name", sandboxRegistryResourceModel{
		Name:     types.StringValue("new-name"),
		URL:      types.StringValue("https://index.docker.io/v1/"),
		Username: types.StringValue("robot"),
		Password: types.StringValue("s3cret"),
	})
	if err != nil {
		t.Fatalf("updateSandboxRegistry: %v", err)
	}
	if model.Name.ValueString() != "new-name" {
		t.Fatalf("Name = %q, want new-name", model.Name.ValueString())
	}
	if model.Password.ValueString() != "s3cret" {
		t.Fatalf("credentials not preserved")
	}
	if !reflect.DeepEqual(requests, []string{"PATCH /api/v2/sandboxes/registries/old-name"}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestSandboxRegistryResourceDeleteTreatsNotFoundAsSuccess(t *testing.T) {
	res := newSandboxRegistryResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v2/sandboxes/registries/docker-hub" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		http.NotFound(w, r)
	}))
	if err := res.deleteSandboxRegistry(context.Background(), "docker-hub"); err != nil {
		t.Fatalf("deleteSandboxRegistry = %v, want nil for 404", err)
	}
}

func TestSandboxRegistryResourceWorkspaceLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, workspace, want string
	}{
		{name: "provider default", want: "provider-workspace"},
		{name: "resource override", workspace: "resource-workspace", want: "resource-workspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			res := newSandboxRegistryResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests++
				if got := req.Header.Get("X-Tenant-Id"); got != tc.want {
					t.Fatalf("X-Tenant-Id = %q, want %q", got, tc.want)
				}
				switch req.Method {
				case http.MethodPost, http.MethodPatch:
					var payload sandboxRegistryPayload
					decodeJSON(t, req, &payload)
					writeJSON(t, w, sandboxRegistryAPI{ID: "reg-id", Name: payload.Name, URL: payload.URL})
				case http.MethodGet:
					writeJSON(t, w, sandboxRegistryAPI{ID: "reg-id", Name: "docker-hub", URL: "https://registry.example.com"})
				case http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
				}
			}), option.WithTenantID("provider-workspace"))
			workspace := types.StringNull()
			if tc.workspace != "" {
				workspace = types.StringValue(tc.workspace)
			}
			plan := sandboxRegistryResourceModel{WorkspaceID: workspace, Name: types.StringValue("docker-hub"), URL: types.StringValue("https://registry.example.com"), Username: types.StringValue("robot"), Password: types.StringValue("secret")}
			created, err := res.createSandboxRegistry(context.Background(), plan)
			if err != nil || !created.WorkspaceID.Equal(workspace) {
				t.Fatalf("createSandboxRegistry() = %#v, %v", created, err)
			}
			read, err := res.readSandboxRegistry(context.Background(), "docker-hub", created, workspace)
			if err != nil || !read.WorkspaceID.Equal(workspace) {
				t.Fatalf("readSandboxRegistry() = %#v, %v", read, err)
			}
			updated, err := res.updateSandboxRegistry(context.Background(), "docker-hub", plan, workspace)
			if err != nil || !updated.WorkspaceID.Equal(workspace) {
				t.Fatalf("updateSandboxRegistry() = %#v, %v", updated, err)
			}
			if err := res.deleteSandboxRegistry(context.Background(), "docker-hub", workspace); err != nil || requests != 4 {
				t.Fatalf("deleteSandboxRegistry() requests = %d, err = %v", requests, err)
			}
		})
	}
}

func TestSandboxRegistryResourceImports(t *testing.T) {
	ctx := context.Background()
	r := &SandboxRegistryResource{}
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
			resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("name"), &name)...)
			if resp.Diagnostics.HasError() || workspace.ValueString() != tc.workspace || name.ValueString() != tc.name {
				t.Fatalf("ImportState(%q) = workspace %q, name %q, diagnostics %v", tc.id, workspace.ValueString(), name.ValueString(), resp.Diagnostics)
			}
		})
	}
}

// TestAccSandboxRegistryCRUDLocal exercises create/read/rename/delete against a
// live workspace. Requires sandbox access and is skipped unless
// LANGSMITH_PROVIDER_ACC=1, mirroring the other local smoke tests.
func TestAccSandboxRegistryCRUDLocal(t *testing.T) {
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 to run local sandbox registry CRUD smoke test")
	}
	profile := os.Getenv("LANGSMITH_PROFILE")
	if profile == "" {
		profile = "local"
	}
	res := &SandboxRegistryResource{client: langsmith.NewClient(langsmith.WithProfile(profile))}
	ctx := context.Background()

	name := fmt.Sprintf("tf-provider-smoke-%d", time.Now().UnixNano())
	created, err := res.createSandboxRegistry(ctx, sandboxRegistryResourceModel{
		Name:     types.StringValue(name),
		URL:      types.StringValue("https://index.docker.io/v1/"),
		Username: types.StringValue("smoke-user"),
		Password: types.StringValue("smoke-pass"),
	})
	if err != nil {
		t.Fatalf("createSandboxRegistry: %v", err)
	}
	currentName := created.Name.ValueString()
	t.Cleanup(func() { _ = res.deleteSandboxRegistry(context.Background(), currentName) })

	if _, err := res.readSandboxRegistry(ctx, currentName, created); err != nil {
		t.Fatalf("readSandboxRegistry: %v", err)
	}

	updated, err := res.updateSandboxRegistry(ctx, currentName, sandboxRegistryResourceModel{
		Name:     types.StringValue(name + "-renamed"),
		URL:      types.StringValue("https://index.docker.io/v1/"),
		Username: types.StringValue("smoke-user"),
		Password: types.StringValue("smoke-pass"),
	})
	if err != nil {
		t.Fatalf("updateSandboxRegistry: %v", err)
	}
	currentName = updated.Name.ValueString()

	if err := res.deleteSandboxRegistry(ctx, currentName); err != nil {
		t.Fatalf("deleteSandboxRegistry: %v", err)
	}
}
