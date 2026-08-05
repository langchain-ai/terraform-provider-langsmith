package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
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

func newSandboxRegistryResourceWithServer(t *testing.T, handler http.Handler) *SandboxRegistryResource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &SandboxRegistryResource{
		client: langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("test-key")),
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
