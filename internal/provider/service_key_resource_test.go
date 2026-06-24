package provider

import (
	"context"
	"encoding/json"
	"errors"
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

func TestServiceKeyCreatePayloadFromModel(t *testing.T) {
	payload := serviceKeyCreatePayloadFromModel(serviceKeyResourceModel{
		Description:        types.StringValue("ci automation"),
		ExpiresAt:          types.StringValue("2027-01-01T00:00:00Z"),
		Workspaces:         []string{"ws-1", "ws-2"},
		DefaultWorkspaceID: types.StringValue("ws-1"),
		RoleID:             types.StringValue("role-1"),
		OrgRoleID:          types.StringValue("org-role-1"),
	})

	if payload.Description != "ci automation" {
		t.Fatalf("Description = %q", payload.Description)
	}
	if payload.ExpiresAt != "2027-01-01T00:00:00Z" {
		t.Fatalf("ExpiresAt = %q", payload.ExpiresAt)
	}
	if !reflect.DeepEqual(payload.Workspaces, []string{"ws-1", "ws-2"}) {
		t.Fatalf("Workspaces = %#v", payload.Workspaces)
	}
	if payload.DefaultWorkspaceID != "ws-1" {
		t.Fatalf("DefaultWorkspaceID = %q", payload.DefaultWorkspaceID)
	}
	if payload.RoleID != "role-1" || payload.OrgRoleID != "org-role-1" {
		t.Fatalf("role fields = %q / %q", payload.RoleID, payload.OrgRoleID)
	}
}

func TestServiceKeyCreatePayloadOmitsEmptyOptionalFields(t *testing.T) {
	payload := serviceKeyCreatePayloadFromModel(serviceKeyResourceModel{
		Description: types.StringValue("only a description"),
	})

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(encoded)
	want := `{"description":"only a description"}`
	if got != want {
		t.Fatalf("encoded payload = %s, want %s", got, want)
	}
}

func TestServiceKeyUpdatePayloadFromModel(t *testing.T) {
	payload := serviceKeyUpdatePayloadFromModel(serviceKeyResourceModel{
		RoleID:    types.StringValue("role-2"),
		OrgRoleID: types.StringNull(),
	})

	if payload.RoleID == nil || *payload.RoleID != "role-2" {
		t.Fatalf("RoleID = %#v, want pointer to role-2", payload.RoleID)
	}
	if payload.OrgRoleID != nil {
		t.Fatalf("OrgRoleID = %#v, want nil (cleared)", payload.OrgRoleID)
	}
}

func TestServiceKeyModelFromAPIPreservesSecretAndConfigOnlyInputs(t *testing.T) {
	previous := serviceKeyResourceModel{
		Key:                types.StringValue("lsv2_sk_secret"),
		Workspaces:         []string{"ws-1"},
		DefaultWorkspaceID: types.StringValue("ws-1"),
		ExpiresAt:          types.StringValue("2027-01-01T00:00:00Z"),
	}

	// A list/get response never carries these fields back.
	next := serviceKeyModelFromAPI(serviceKeyAPI{
		ID:                   "key-id",
		ShortKey:             "lsv2...abcd",
		Description:          "ci automation",
		CreatedAt:            "2026-06-23T00:00:00Z",
		AccessScope:          "workspace",
		RoleID:               "role-1",
		WorkspaceNames:       []string{"Engineering"},
		DefaultWorkspaceName: "Engineering",
	}, previous)

	if next.Key.ValueString() != "lsv2_sk_secret" {
		t.Fatalf("Key = %q, want preserved secret", next.Key.ValueString())
	}
	if !reflect.DeepEqual(next.Workspaces, []string{"ws-1"}) {
		t.Fatalf("Workspaces = %#v, want preserved", next.Workspaces)
	}
	if next.DefaultWorkspaceID.ValueString() != "ws-1" {
		t.Fatalf("DefaultWorkspaceID = %q, want preserved", next.DefaultWorkspaceID.ValueString())
	}
	if next.ExpiresAt.ValueString() != "2027-01-01T00:00:00Z" {
		t.Fatalf("ExpiresAt = %q, want preserved config value", next.ExpiresAt.ValueString())
	}
	if next.ID.ValueString() != "key-id" || next.ShortKey.ValueString() != "lsv2...abcd" {
		t.Fatalf("computed fields not set: %#v", next)
	}
	if next.AccessScope.ValueString() != "workspace" || next.RoleID.ValueString() != "role-1" {
		t.Fatalf("refreshed fields = %q / %q", next.AccessScope.ValueString(), next.RoleID.ValueString())
	}
	if next.DefaultWorkspaceName.ValueString() != "Engineering" {
		t.Fatalf("DefaultWorkspaceName = %q", next.DefaultWorkspaceName.ValueString())
	}
}

func TestServiceKeyModelFromAPISetsSecretFromCreateResponse(t *testing.T) {
	next := serviceKeyModelFromAPI(serviceKeyAPI{
		ID:          "key-id",
		ShortKey:    "lsv2...abcd",
		Description: "ci automation",
		Key:         "lsv2_sk_freshsecret",
	}, serviceKeyResourceModel{})

	if next.Key.ValueString() != "lsv2_sk_freshsecret" {
		t.Fatalf("Key = %q, want secret from create response", next.Key.ValueString())
	}
}

func TestServiceKeyModelFromAPIDoesNotClobberSecretWhenResponseOmitsKey(t *testing.T) {
	previous := serviceKeyResourceModel{Key: types.StringValue("lsv2_sk_secret")}
	next := serviceKeyModelFromAPI(serviceKeyAPI{ID: "key-id", ShortKey: "lsv2...abcd"}, previous)

	if next.Key.ValueString() != "lsv2_sk_secret" {
		t.Fatalf("Key = %q, want preserved secret (response had no key)", next.Key.ValueString())
	}
}

func TestFindServiceKeyByIDReturnsNotFoundSentinel(t *testing.T) {
	_, err := findServiceKeyByID([]serviceKeyAPI{{ID: "other"}}, "missing")
	if !errors.Is(err, errServiceKeyNotFound) {
		t.Fatalf("findServiceKeyByID error = %v, want errServiceKeyNotFound", err)
	}
}

func TestFindServiceKeyByIDReturnsMatch(t *testing.T) {
	key, err := findServiceKeyByID([]serviceKeyAPI{{ID: "a"}, {ID: "b", ShortKey: "lsv2...b"}}, "b")
	if err != nil {
		t.Fatalf("findServiceKeyByID error = %v", err)
	}
	if key.ShortKey != "lsv2...b" {
		t.Fatalf("matched wrong key: %#v", key)
	}
}

func newServiceKeyResourceWithServer(t *testing.T, handler http.Handler) *ServiceKeyResource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &ServiceKeyResource{
		client: langsmith.NewClient(
			option.WithBaseURL(server.URL),
			option.WithAPIKey("test-key"),
		),
	}
}

func TestServiceKeyResourceMetadata(t *testing.T) {
	var resp resource.MetadataResponse
	NewServiceKeyResource().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "langsmith"}, &resp)
	if resp.TypeName != "langsmith_service_key" {
		t.Fatalf("TypeName = %q, want langsmith_service_key", resp.TypeName)
	}
}

func TestServiceKeyResourceSchemaMarksKeySensitive(t *testing.T) {
	var resp resource.SchemaResponse
	NewServiceKeyResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)

	attr, ok := resp.Schema.Attributes["key"]
	if !ok {
		t.Fatalf("schema is missing the key attribute")
	}
	if !attr.IsSensitive() {
		t.Fatalf("key attribute must be marked Sensitive")
	}
	if !attr.IsComputed() {
		t.Fatalf("key attribute must be Computed")
	}
}

func TestServiceKeyResourceCreatePostsPayloadAndCapturesSecretWithoutReReading(t *testing.T) {
	requests := make([]string, 0, 1)
	res := newServiceKeyResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/current/service-keys":
			var payload serviceKeyCreatePayload
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode create payload: %v", err)
			}
			if payload.Description != "ci automation" {
				t.Fatalf("payload.Description = %q", payload.Description)
			}
			writeJSON(t, w, serviceKeyAPI{
				ID:          "key-id",
				ShortKey:    "lsv2...abcd",
				Description: "ci automation",
				AccessScope: "organization",
				Key:         "lsv2_sk_freshsecret",
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))

	model, err := res.createServiceKey(context.Background(), serviceKeyResourceModel{
		Description: types.StringValue("ci automation"),
	})
	if err != nil {
		t.Fatalf("createServiceKey returned error: %v", err)
	}
	if model.Key.ValueString() != "lsv2_sk_freshsecret" {
		t.Fatalf("Key = %q, want secret from POST response", model.Key.ValueString())
	}
	// Create must not re-read via GET (the list endpoint never returns the secret).
	if !reflect.DeepEqual(requests, []string{"POST /api/v1/orgs/current/service-keys"}) {
		t.Fatalf("requests = %#v, want a single POST", requests)
	}
}

func TestServiceKeyResourceReadFindsKeyAndPreservesSecret(t *testing.T) {
	res := newServiceKeyResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/orgs/current/service-keys" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		writeJSON(t, w, []serviceKeyAPI{
			{ID: "other-id", ShortKey: "lsv2...zzzz"},
			{ID: "key-id", ShortKey: "lsv2...abcd", Description: "ci automation", AccessScope: "organization"},
		})
	}))

	model, err := res.readServiceKey(context.Background(), "key-id", serviceKeyResourceModel{
		Key: types.StringValue("lsv2_sk_secret"),
	})
	if err != nil {
		t.Fatalf("readServiceKey returned error: %v", err)
	}
	if model.ShortKey.ValueString() != "lsv2...abcd" {
		t.Fatalf("ShortKey = %q", model.ShortKey.ValueString())
	}
	if model.Key.ValueString() != "lsv2_sk_secret" {
		t.Fatalf("Key = %q, want preserved secret", model.Key.ValueString())
	}
}

func TestServiceKeyResourceReadReturnsNotFoundSentinel(t *testing.T) {
	res := newServiceKeyResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []serviceKeyAPI{{ID: "other-id"}})
	}))

	_, err := res.readServiceKey(context.Background(), "missing-id", serviceKeyResourceModel{})
	if !errors.Is(err, errServiceKeyNotFound) {
		t.Fatalf("readServiceKey error = %v, want errServiceKeyNotFound", err)
	}
}

func TestServiceKeyResourceUpdatePatchesRolesAndPreservesSecret(t *testing.T) {
	requests := make([]string, 0, 2)
	res := newServiceKeyResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/orgs/current/service-keys/key-id":
			var payload serviceKeyUpdatePayload
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode update payload: %v", err)
			}
			if payload.RoleID == nil || *payload.RoleID != "role-2" {
				t.Fatalf("payload.RoleID = %#v, want role-2", payload.RoleID)
			}
			writeJSON(t, w, serviceKeyAPI{ID: "key-id"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/current/service-keys":
			writeJSON(t, w, []serviceKeyAPI{{ID: "key-id", ShortKey: "lsv2...abcd", RoleID: "role-2"}})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))

	model, err := res.updateServiceKey(context.Background(), "key-id",
		serviceKeyResourceModel{RoleID: types.StringValue("role-2")},
		serviceKeyResourceModel{Key: types.StringValue("lsv2_sk_secret")},
	)
	if err != nil {
		t.Fatalf("updateServiceKey returned error: %v", err)
	}
	if model.RoleID.ValueString() != "role-2" {
		t.Fatalf("RoleID = %q, want role-2", model.RoleID.ValueString())
	}
	if model.Key.ValueString() != "lsv2_sk_secret" {
		t.Fatalf("Key = %q, want preserved secret", model.Key.ValueString())
	}
	if !reflect.DeepEqual(requests, []string{
		"PATCH /api/v1/orgs/current/service-keys/key-id",
		"GET /api/v1/orgs/current/service-keys",
	}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestServiceKeyResourceDeleteTreatsNotFoundAsSuccess(t *testing.T) {
	res := newServiceKeyResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/orgs/current/service-keys/missing-id" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		http.NotFound(w, r)
	}))

	if err := res.deleteServiceKey(context.Background(), "missing-id"); err != nil {
		t.Fatalf("deleteServiceKey error = %v, want nil for 404", err)
	}
}

// TestAccServiceKeyCRUDLocal exercises create/read/delete against a live
// LangSmith org. It requires an organization-admin-capable credential and is
// skipped unless LANGSMITH_PROVIDER_ACC=1, mirroring the other local smoke tests.
func TestAccServiceKeyCRUDLocal(t *testing.T) {
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 to run local service key CRUD smoke test")
	}

	profile := os.Getenv("LANGSMITH_PROFILE")
	if profile == "" {
		profile = "local"
	}
	res := &ServiceKeyResource{client: langsmith.NewClient(langsmith.WithProfile(profile))}
	ctx := context.Background()

	created, err := res.createServiceKey(ctx, serviceKeyResourceModel{
		Description: types.StringValue(fmt.Sprintf("terraform-provider service key smoke %d", time.Now().UnixNano())),
	})
	if err != nil {
		t.Fatalf("createServiceKey: %v", err)
	}
	if created.Key.ValueString() == "" {
		t.Fatalf("created service key has empty secret")
	}
	id := created.ID.ValueString()
	t.Cleanup(func() { _ = res.deleteServiceKey(context.Background(), id) })

	read, err := res.readServiceKey(ctx, id, created)
	if err != nil {
		t.Fatalf("readServiceKey: %v", err)
	}
	if read.ShortKey.ValueString() == "" {
		t.Fatalf("read service key has empty short_key")
	}
	if read.Key.ValueString() != created.Key.ValueString() {
		t.Fatalf("read did not preserve the secret from prior state")
	}

	if err := res.deleteServiceKey(ctx, id); err != nil {
		t.Fatalf("deleteServiceKey: %v", err)
	}
	if _, err := res.readServiceKey(ctx, id, created); err == nil {
		t.Fatalf("service key still present after delete")
	} else if !errors.Is(err, errServiceKeyNotFound) && !isLangSmithNotFound(err) {
		t.Fatalf("unexpected error after delete: %v", err)
	}
}
