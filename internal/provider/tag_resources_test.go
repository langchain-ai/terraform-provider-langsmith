package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestTagKeyResourceLifecycleRequests(t *testing.T) {
	description := "Deployment environment"
	resource := newTagKeyResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPost:
			if req.URL.Path != "/api/v1/workspaces/current/tag-keys" {
				t.Fatalf("POST path = %q", req.URL.Path)
			}
			var payload tagKeyPayload
			decodeJSON(t, req, &payload)
			if payload.Key != "Environment" || payload.Description == nil || *payload.Description != description {
				t.Fatalf("create payload = %#v", payload)
			}
			writeJSON(t, w, tagKeyAPI{ID: "key-id", Key: payload.Key, Description: payload.Description, CreatedAt: "created", UpdatedAt: "created"})
		case http.MethodGet:
			if req.URL.Path != "/api/v1/workspaces/current/tag-keys/key-id" {
				t.Fatalf("GET path = %q", req.URL.Path)
			}
			writeJSON(t, w, tagKeyAPI{ID: "key-id", Key: "Environment", Description: &description, CreatedAt: "created", UpdatedAt: "updated"})
		case http.MethodPatch:
			var payload tagKeyPayload
			decodeJSON(t, req, &payload)
			if payload.Key != "Stage" || payload.Description != nil {
				t.Fatalf("update payload = %#v, want key Stage and explicit null description", payload)
			}
			writeJSON(t, w, tagKeyAPI{ID: "key-id", Key: payload.Key, CreatedAt: "created", UpdatedAt: "updated"})
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
	}))

	created, err := resource.createTagKey(context.Background(), tagKeyResourceModel{Key: types.StringValue("Environment"), Description: types.StringValue(description)})
	if err != nil || created.ID.ValueString() != "key-id" {
		t.Fatalf("createTagKey() = %#v, %v", created, err)
	}
	read, err := resource.readTagKey(context.Background(), "key-id")
	if err != nil || read.UpdatedAt.ValueString() != "updated" {
		t.Fatalf("readTagKey() = %#v, %v", read, err)
	}
	updated, err := resource.updateTagKey(context.Background(), "key-id", tagKeyResourceModel{Key: types.StringValue("Stage"), Description: types.StringNull()})
	if err != nil || !updated.Description.IsNull() {
		t.Fatalf("updateTagKey() = %#v, %v", updated, err)
	}
	if err := resource.deleteTagKey(context.Background(), "key-id"); err != nil {
		t.Fatalf("deleteTagKey() error = %v", err)
	}
}

func TestTagValueResourceLifecycleRequests(t *testing.T) {
	description := "Production workloads"
	resource := newTagValueResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/v1/workspaces/current/tag-keys/key-id/tag-values" && req.URL.Path != "/api/v1/workspaces/current/tag-keys/key-id/tag-values/value-id" {
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		switch req.Method {
		case http.MethodPost:
			var payload tagValuePayload
			decodeJSON(t, req, &payload)
			writeJSON(t, w, tagValueAPI{ID: "value-id", TagKeyID: "key-id", Value: payload.Value, Description: payload.Description, CreatedAt: "created", UpdatedAt: "created"})
		case http.MethodGet:
			writeJSON(t, w, tagValueAPI{ID: "value-id", TagKeyID: "key-id", Value: "production", Description: &description, CreatedAt: "created", UpdatedAt: "updated"})
		case http.MethodPatch:
			var payload tagValuePayload
			decodeJSON(t, req, &payload)
			writeJSON(t, w, tagValueAPI{ID: "value-id", TagKeyID: "key-id", Value: payload.Value, Description: payload.Description, CreatedAt: "created", UpdatedAt: "updated"})
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	}))

	created, err := resource.createTagValue(context.Background(), tagValueResourceModel{TagKeyID: types.StringValue("key-id"), Value: types.StringValue("production"), Description: types.StringValue(description)})
	if err != nil || created.TagKeyID.ValueString() != "key-id" {
		t.Fatalf("createTagValue() = %#v, %v", created, err)
	}
	if _, err := resource.readTagValue(context.Background(), "key-id", "value-id"); err != nil {
		t.Fatalf("readTagValue() error = %v", err)
	}
	updated, err := resource.updateTagValue(context.Background(), "key-id", "value-id", tagValueResourceModel{Value: types.StringValue("staging"), Description: types.StringNull()})
	if err != nil || updated.Value.ValueString() != "staging" || !updated.Description.IsNull() {
		t.Fatalf("updateTagValue() = %#v, %v", updated, err)
	}
	if err := resource.deleteTagValue(context.Background(), "key-id", "value-id"); err != nil {
		t.Fatalf("deleteTagValue() error = %v", err)
	}
}

func TestTaggingResourceLifecycleRequests(t *testing.T) {
	resource := newTaggingResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPost:
			if req.URL.Path != "/api/v1/workspaces/current/taggings" {
				t.Fatalf("POST path = %q", req.URL.Path)
			}
			var payload taggingAPI
			decodeJSON(t, req, &payload)
			payload.ID = "tagging-id"
			payload.CreatedAt = "created"
			writeJSON(t, w, payload)
		case http.MethodGet:
			if req.URL.Path != "/api/v1/workspaces/current/tags/resource" || req.URL.Query().Get("resource_type") != "project" || req.URL.Query().Get("resource_id") != "project-id" {
				t.Fatalf("unexpected read URL: %s", req.URL.String())
			}
			writeJSON(t, w, []tagKeyWithTaggingsAPI{{Values: []tagValueWithTaggingsAPI{{tagValueAPI: tagValueAPI{ID: "value-id"}, Taggings: []taggingAPI{{ID: "tagging-id", TagValueID: "value-id", ResourceType: "project", ResourceID: "project-id", CreatedAt: "created"}}}}}})
		case http.MethodDelete:
			if req.URL.Path != "/api/v1/workspaces/current/taggings/tagging-id" {
				t.Fatalf("DELETE path = %q", req.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}))

	plan := taggingResourceModel{TagValueID: types.StringValue("value-id"), ResourceType: types.StringValue("project"), ResourceID: types.StringValue("project-id")}
	created, err := resource.createTagging(context.Background(), plan)
	if err != nil || created.ID.ValueString() != "tagging-id" {
		t.Fatalf("createTagging() = %#v, %v", created, err)
	}
	read, err := resource.readTagging(context.Background(), created)
	if err != nil || read.TagValueID.ValueString() != "value-id" {
		t.Fatalf("readTagging() = %#v, %v", read, err)
	}
	if err := resource.deleteTagging(context.Background(), "tagging-id"); err != nil {
		t.Fatalf("deleteTagging() error = %v", err)
	}
}

func TestTagResourceConvenienceLifecycle(t *testing.T) {
	resource := newTagResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/api/v1/workspaces/current/tag-keys":
			var payload tagKeyPayload
			decodeJSON(t, req, &payload)
			writeJSON(t, w, tagKeyAPI{ID: "key-id", Key: payload.Key, Description: payload.Description})
		case req.Method == http.MethodPost && req.URL.Path == "/api/v1/workspaces/current/tag-keys/key-id/tag-values":
			var payload tagValuePayload
			decodeJSON(t, req, &payload)
			writeJSON(t, w, tagValueAPI{ID: "value-id", TagKeyID: "key-id", Value: payload.Value, Description: payload.Description})
		case req.Method == http.MethodGet && req.URL.Path == "/api/v1/workspaces/current/tag-keys/key-id":
			writeJSON(t, w, tagKeyAPI{ID: "key-id", Key: "Environment"})
		case req.Method == http.MethodGet && req.URL.Path == "/api/v1/workspaces/current/tag-keys/key-id/tag-values/value-id":
			writeJSON(t, w, tagValueAPI{ID: "value-id", TagKeyID: "key-id", Value: "production"})
		case req.Method == http.MethodPatch && req.URL.Path == "/api/v1/workspaces/current/tag-keys/key-id":
			writeJSON(t, w, tagKeyAPI{ID: "key-id", Key: "Stage"})
		case req.Method == http.MethodPatch && req.URL.Path == "/api/v1/workspaces/current/tag-keys/key-id/tag-values/value-id":
			writeJSON(t, w, tagValueAPI{ID: "value-id", TagKeyID: "key-id", Value: "staging"})
		case req.Method == http.MethodDelete && req.URL.Path == "/api/v1/workspaces/current/tag-keys/key-id":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
	}))

	created, err := resource.createTag(context.Background(), tagResourceModel{Key: types.StringValue("Environment"), Value: types.StringValue("production")})
	if err != nil || created.TagKeyID.ValueString() != "key-id" || created.TagValueID.ValueString() != "value-id" {
		t.Fatalf("createTag() = %#v, %v", created, err)
	}
	if _, err := resource.readTag(context.Background(), "key-id", "value-id"); err != nil {
		t.Fatalf("readTag() error = %v", err)
	}
	updated, err := resource.updateTag(context.Background(), "key-id", "value-id", tagResourceModel{Key: types.StringValue("Stage"), Value: types.StringValue("staging")})
	if err != nil || updated.Key.ValueString() != "Stage" || updated.Value.ValueString() != "staging" {
		t.Fatalf("updateTag() = %#v, %v", updated, err)
	}
	if err := (&TagKeyResource{client: resource.client}).deleteTagKey(context.Background(), "key-id"); err != nil {
		t.Fatalf("deleteTag() error = %v", err)
	}
}

func newTagKeyResourceWithServer(t *testing.T, handler http.Handler) *TagKeyResource {
	return &TagKeyResource{client: newTagTestClient(t, handler)}
}

func newTagValueResourceWithServer(t *testing.T, handler http.Handler) *TagValueResource {
	return &TagValueResource{client: newTagTestClient(t, handler)}
}

func newTaggingResourceWithServer(t *testing.T, handler http.Handler) *TaggingResource {
	return &TaggingResource{client: newTagTestClient(t, handler)}
}

func newTagResourceWithServer(t *testing.T, handler http.Handler) *TagResource {
	return &TagResource{client: newTagTestClient(t, handler)}
}

func newTagTestClient(t *testing.T, handler http.Handler) *langsmith.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("test-key"))
}

func decodeJSON(t *testing.T, req *http.Request, value any) {
	t.Helper()
	if err := json.NewDecoder(req.Body).Decode(value); err != nil {
		t.Fatalf("decode request: %v", err)
	}
}
