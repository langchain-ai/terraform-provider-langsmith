package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
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
		if got := req.Header.Get("X-Tenant-Id"); got != "resource-workspace" {
			t.Fatalf("X-Tenant-Id = %q", got)
		}
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

	created, err := resource.createTag(context.Background(), tagResourceModel{WorkspaceID: types.StringValue("resource-workspace"), Key: types.StringValue("Environment"), Value: types.StringValue("production")})
	if err != nil || created.WorkspaceID.ValueString() != "resource-workspace" || created.TagKeyID.ValueString() != "key-id" || created.TagValueID.ValueString() != "value-id" {
		t.Fatalf("createTag() = %#v, %v", created, err)
	}
	if _, err := resource.readTag(context.Background(), "key-id", "value-id", types.StringValue("resource-workspace")); err != nil {
		t.Fatalf("readTag() error = %v", err)
	}
	updated, err := resource.updateTag(context.Background(), "key-id", "value-id", tagResourceModel{WorkspaceID: types.StringValue("resource-workspace"), Key: types.StringValue("Stage"), Value: types.StringValue("staging")})
	if err != nil || updated.WorkspaceID.ValueString() != "resource-workspace" || updated.Key.ValueString() != "Stage" || updated.Value.ValueString() != "staging" {
		t.Fatalf("updateTag() = %#v, %v", updated, err)
	}
	if err := (&TagKeyResource{client: resource.client}).deleteTagKey(context.Background(), "key-id", types.StringValue("resource-workspace")); err != nil {
		t.Fatalf("deleteTag() error = %v", err)
	}
}

func TestTagResourceRefreshRemovesSurvivingKeyWhenValueIsMissing(t *testing.T) {
	requests := []string{}
	resource := newTagResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.Method+" "+req.URL.Path)
		if got := req.Header.Get("X-Tenant-Id"); got != "resource-workspace" {
			t.Fatalf("X-Tenant-Id = %q", got)
		}
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/api/v1/workspaces/current/tag-keys/key-id":
			writeJSON(t, w, tagKeyAPI{ID: "key-id", Key: "Environment"})
		case req.Method == http.MethodGet && req.URL.Path == "/api/v1/workspaces/current/tag-keys/key-id/tag-values/value-id":
			http.NotFound(w, req)
		case req.Method == http.MethodDelete && req.URL.Path == "/api/v1/workspaces/current/tag-keys/key-id":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
	}))

	_, remove, err := resource.readTagForRefresh(context.Background(), "key-id", "value-id", types.StringValue("resource-workspace"))
	if err != nil || !remove {
		t.Fatalf("readTagForRefresh() remove = %t, err = %v", remove, err)
	}
	want := []string{
		"GET /api/v1/workspaces/current/tag-keys/key-id",
		"GET /api/v1/workspaces/current/tag-keys/key-id/tag-values/value-id",
		"DELETE /api/v1/workspaces/current/tag-keys/key-id",
	}
	if len(requests) != len(want) {
		t.Fatalf("requests = %#v, want %#v", requests, want)
	}
	for i := range want {
		if requests[i] != want[i] {
			t.Fatalf("requests = %#v, want %#v", requests, want)
		}
	}
}

func TestTagResourceWorkspaceLifecycle(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, workspace, want string
	}{
		{name: "provider default", want: "provider-workspace"},
		{name: "resource override", workspace: "resource-workspace", want: "resource-workspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			client := newTagTestClientWithOptions(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests++
				if got := req.Header.Get("X-Tenant-Id"); got != tc.want {
					t.Fatalf("X-Tenant-Id = %q, want %q", got, tc.want)
				}
				switch req.Method {
				case http.MethodGet:
					if strings.HasSuffix(req.URL.Path, "/tag-keys/key-id") {
						writeJSON(t, w, tagKeyAPI{ID: "key-id", Key: "Environment"})
					} else {
						writeJSON(t, w, tagValueAPI{ID: "value-id", TagKeyID: "key-id", Value: "production"})
					}
				case http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
				}
			}), option.WithTenantID("provider-workspace"))
			r := &TagResource{client: client}
			state := tagResourceModel{ID: types.StringValue("value-id"), WorkspaceID: types.StringNull(), TagKeyID: types.StringValue("key-id"), TagValueID: types.StringValue("value-id"), Key: types.StringValue("stale"), Value: types.StringValue("stale")}
			if tc.workspace != "" {
				state.WorkspaceID = types.StringValue(tc.workspace)
			}
			schema := tagResourceSchema(t, r)
			readState := stateForTag(t, schema, state)
			readResp := resource.ReadResponse{State: readState}
			r.Read(ctx, resource.ReadRequest{State: readState}, &readResp)
			var refreshed tagResourceModel
			readResp.Diagnostics.Append(readResp.State.Get(ctx, &refreshed)...)
			if readResp.Diagnostics.HasError() || !refreshed.WorkspaceID.Equal(state.WorkspaceID) || refreshed.Key.ValueString() != "Environment" {
				t.Fatalf("Read() state = %#v, diagnostics = %v", refreshed, readResp.Diagnostics)
			}
			deleteResp := resource.DeleteResponse{State: readResp.State}
			r.Delete(ctx, resource.DeleteRequest{State: readResp.State}, &deleteResp)
			if deleteResp.Diagnostics.HasError() || requests != 3 {
				t.Fatalf("Delete() requests = %d, diagnostics = %v", requests, deleteResp.Diagnostics)
			}
		})
	}
}

func TestTagResourceCreateRollbackUsesWorkspace(t *testing.T) {
	requests := []string{}
	resource := newTagResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.Method+" "+req.URL.Path)
		if got := req.Header.Get("X-Tenant-Id"); got != "resource-workspace" {
			t.Fatalf("X-Tenant-Id = %q", got)
		}
		switch {
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/tag-keys"):
			writeJSON(t, w, tagKeyAPI{ID: "key-id", Key: "Environment"})
		case req.Method == http.MethodPost:
			http.Error(w, "failed", http.StatusBadRequest)
		case req.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	_, err := resource.createTag(context.Background(), tagResourceModel{WorkspaceID: types.StringValue("resource-workspace"), Key: types.StringValue("Environment"), Value: types.StringValue("production")})
	if err == nil || len(requests) != 3 || !strings.HasSuffix(requests[2], "/tag-keys/key-id") {
		t.Fatalf("createTag() requests = %#v, err = %v", requests, err)
	}
}

func TestTagResourceImports(t *testing.T) {
	ctx := context.Background()
	r := &TagResource{}
	schema := tagResourceSchema(t, r)
	for _, tc := range []struct {
		id, workspace, key, value string
		valid                     bool
	}{
		{id: "key-id/value-id", key: "key-id", value: "value-id", valid: true},
		{id: "workspace-id/key-id/value-id", workspace: "workspace-id", key: "key-id", value: "value-id", valid: true},
		{id: "key-id"}, {id: "/value-id"}, {id: "key-id/"}, {id: "workspace-id//value-id"}, {id: "workspace-id/key-id/"}, {id: "too/many/id/parts"},
	} {
		t.Run(strings.ReplaceAll(tc.id, "/", "_"), func(t *testing.T) {
			resp := resource.ImportStateResponse{State: tfsdk.State{Schema: schema.Schema, Raw: tftypes.NewValue(schema.Schema.Type().TerraformType(ctx), nil)}}
			r.ImportState(ctx, resource.ImportStateRequest{ID: tc.id}, &resp)
			if !tc.valid {
				if !resp.Diagnostics.HasError() {
					t.Fatalf("ImportState(%q) diagnostics = %v", tc.id, resp.Diagnostics)
				}
				return
			}
			var workspace, key, value types.String
			resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("workspace_id"), &workspace)...)
			resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("tag_key_id"), &key)...)
			resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("tag_value_id"), &value)...)
			if resp.Diagnostics.HasError() || workspace.ValueString() != tc.workspace || key.ValueString() != tc.key || value.ValueString() != tc.value {
				t.Fatalf("ImportState(%q) = workspace %q, key %q, value %q, diagnostics %v", tc.id, workspace.ValueString(), key.ValueString(), value.ValueString(), resp.Diagnostics)
			}
		})
	}
}

func TestCompositeTagResourcesWorkspaceLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, workspace, want string
	}{
		{name: "provider default", want: "provider-workspace"},
		{name: "resource override", workspace: "resource-workspace", want: "resource-workspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newTagTestClientWithOptions(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if got := req.Header.Get("X-Tenant-Id"); got != tc.want {
					t.Fatalf("X-Tenant-Id = %q, want %q", got, tc.want)
				}
				switch {
				case req.Method == http.MethodPost && req.URL.Path == "/api/v1/workspaces/current/tag-keys":
					writeJSON(t, w, tagKeyAPI{ID: "key-id", Key: "Environment"})
				case req.Method == http.MethodGet && req.URL.Path == "/api/v1/workspaces/current/tag-keys/key-id":
					writeJSON(t, w, tagKeyAPI{ID: "key-id", Key: "Environment"})
				case req.Method == http.MethodPatch && req.URL.Path == "/api/v1/workspaces/current/tag-keys/key-id":
					writeJSON(t, w, tagKeyAPI{ID: "key-id", Key: "Stage"})
				case req.Method == http.MethodDelete && req.URL.Path == "/api/v1/workspaces/current/tag-keys/key-id":
					w.WriteHeader(http.StatusNoContent)
				case req.Method == http.MethodPost && req.URL.Path == "/api/v1/workspaces/current/tag-keys/key-id/tag-values":
					writeJSON(t, w, tagValueAPI{ID: "value-id", TagKeyID: "key-id", Value: "production"})
				case req.Method == http.MethodGet && req.URL.Path == "/api/v1/workspaces/current/tag-keys/key-id/tag-values/value-id":
					writeJSON(t, w, tagValueAPI{ID: "value-id", TagKeyID: "key-id", Value: "production"})
				case req.Method == http.MethodPatch && req.URL.Path == "/api/v1/workspaces/current/tag-keys/key-id/tag-values/value-id":
					writeJSON(t, w, tagValueAPI{ID: "value-id", TagKeyID: "key-id", Value: "staging"})
				case req.Method == http.MethodDelete && req.URL.Path == "/api/v1/workspaces/current/tag-keys/key-id/tag-values/value-id":
					w.WriteHeader(http.StatusNoContent)
				case req.Method == http.MethodPost && req.URL.Path == "/api/v1/workspaces/current/taggings":
					writeJSON(t, w, taggingAPI{ID: "tagging-id", TagValueID: "value-id", ResourceType: "project", ResourceID: "project-id"})
				case req.Method == http.MethodGet && req.URL.Path == "/api/v1/workspaces/current/tags/resource":
					writeJSON(t, w, []tagKeyWithTaggingsAPI{{Values: []tagValueWithTaggingsAPI{{Taggings: []taggingAPI{{ID: "tagging-id", TagValueID: "value-id", ResourceType: "project", ResourceID: "project-id"}}}}}})
				case req.Method == http.MethodDelete && req.URL.Path == "/api/v1/workspaces/current/taggings/tagging-id":
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
				}
			}), option.WithTenantID("provider-workspace"))
			workspace := types.StringNull()
			if tc.workspace != "" {
				workspace = types.StringValue(tc.workspace)
			}
			keyResource := &TagKeyResource{client: client}
			keyPlan := tagKeyResourceModel{WorkspaceID: workspace, Key: types.StringValue("Environment")}
			key, err := keyResource.createTagKey(context.Background(), keyPlan, workspace)
			if err != nil || !key.WorkspaceID.Equal(workspace) {
				t.Fatalf("createTagKey() = %#v, %v", key, err)
			}
			if key, err = keyResource.readTagKey(context.Background(), "key-id", workspace); err != nil || !key.WorkspaceID.Equal(workspace) {
				t.Fatalf("readTagKey() = %#v, %v", key, err)
			}
			if key, err = keyResource.updateTagKey(context.Background(), "key-id", keyPlan, workspace); err != nil || !key.WorkspaceID.Equal(workspace) {
				t.Fatalf("updateTagKey() = %#v, %v", key, err)
			}
			valueResource := &TagValueResource{client: client}
			valuePlan := tagValueResourceModel{WorkspaceID: workspace, TagKeyID: types.StringValue("key-id"), Value: types.StringValue("production")}
			value, err := valueResource.createTagValue(context.Background(), valuePlan, workspace)
			if err != nil || !value.WorkspaceID.Equal(workspace) {
				t.Fatalf("createTagValue() = %#v, %v", value, err)
			}
			if value, err = valueResource.readTagValue(context.Background(), "key-id", "value-id", workspace); err != nil || !value.WorkspaceID.Equal(workspace) {
				t.Fatalf("readTagValue() = %#v, %v", value, err)
			}
			if value, err = valueResource.updateTagValue(context.Background(), "key-id", "value-id", valuePlan, workspace); err != nil || !value.WorkspaceID.Equal(workspace) {
				t.Fatalf("updateTagValue() = %#v, %v", value, err)
			}
			taggingResource := &TaggingResource{client: client}
			taggingPlan := taggingResourceModel{WorkspaceID: workspace, TagValueID: types.StringValue("value-id"), ResourceType: types.StringValue("project"), ResourceID: types.StringValue("project-id")}
			tagging, err := taggingResource.createTagging(context.Background(), taggingPlan)
			if err != nil || !tagging.WorkspaceID.Equal(workspace) {
				t.Fatalf("createTagging() = %#v, %v", tagging, err)
			}
			if tagging, err = taggingResource.readTagging(context.Background(), tagging); err != nil || !tagging.WorkspaceID.Equal(workspace) {
				t.Fatalf("readTagging() = %#v, %v", tagging, err)
			}
			if err := taggingResource.deleteTagging(context.Background(), "tagging-id", workspace); err != nil {
				t.Fatalf("deleteTagging() error = %v", err)
			}
			if err := valueResource.deleteTagValue(context.Background(), "key-id", "value-id", workspace); err != nil {
				t.Fatalf("deleteTagValue() error = %v", err)
			}
			if err := keyResource.deleteTagKey(context.Background(), "key-id", workspace); err != nil {
				t.Fatalf("deleteTagKey() error = %v", err)
			}
		})
	}
}

func TestCompositeTagResourceImports(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, legacy, scoped string
		resource             resource.ResourceWithImportState
		schema               func(*resource.SchemaResponse)
		attributes           map[string]string
	}{
		{name: "tag key", legacy: "key-id", scoped: "workspace-id/key-id", resource: &TagKeyResource{}, schema: func(resp *resource.SchemaResponse) { (&TagKeyResource{}).Schema(ctx, resource.SchemaRequest{}, resp) }, attributes: map[string]string{"id": "key-id"}},
		{name: "tag value", legacy: "key-id/value-id", scoped: "workspace-id/key-id/value-id", resource: &TagValueResource{}, schema: func(resp *resource.SchemaResponse) { (&TagValueResource{}).Schema(ctx, resource.SchemaRequest{}, resp) }, attributes: map[string]string{"id": "value-id", "tag_key_id": "key-id"}},
		{name: "tagging", legacy: "tagging-id/value-id/project/project-id", scoped: "workspace-id/tagging-id/value-id/project/project-id", resource: &TaggingResource{}, schema: func(resp *resource.SchemaResponse) { (&TaggingResource{}).Schema(ctx, resource.SchemaRequest{}, resp) }, attributes: map[string]string{"id": "tagging-id", "tag_value_id": "value-id", "resource_type": "project", "resource_id": "project-id"}},
	}
	for _, tc := range cases {
		for _, importID := range []string{tc.legacy, tc.scoped} {
			t.Run(tc.name+"_"+strings.ReplaceAll(importID, "/", "_"), func(t *testing.T) {
				var schemaResp resource.SchemaResponse
				tc.schema(&schemaResp)
				resp := resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)}}
				tc.resource.ImportState(ctx, resource.ImportStateRequest{ID: importID}, &resp)
				var workspace types.String
				resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("workspace_id"), &workspace)...)
				wantWorkspace := ""
				if importID == tc.scoped {
					wantWorkspace = "workspace-id"
				}
				if workspace.ValueString() != wantWorkspace {
					t.Fatalf("workspace_id = %q, want %q", workspace.ValueString(), wantWorkspace)
				}
				for name, want := range tc.attributes {
					var got types.String
					resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root(name), &got)...)
					if got.ValueString() != want {
						t.Fatalf("%s = %q, want %q", name, got.ValueString(), want)
					}
				}
				if resp.Diagnostics.HasError() {
					t.Fatalf("ImportState(%q) diagnostics = %v", importID, resp.Diagnostics)
				}
			})
		}
	}
}

func TestTagCreatesDoNotRetry(t *testing.T) {
	counts := map[string]int{}
	client := newTagTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		counts[req.URL.Path]++
		http.Error(w, "temporary failure", http.StatusInternalServerError)
	}))

	if _, err := (&TagKeyResource{client: client}).createTagKey(context.Background(), tagKeyResourceModel{Key: types.StringValue("Environment")}); err == nil {
		t.Fatal("createTagKey() error = nil")
	}
	if _, err := (&TagValueResource{client: client}).createTagValue(context.Background(), tagValueResourceModel{TagKeyID: types.StringValue("key-id"), Value: types.StringValue("production")}); err == nil {
		t.Fatal("createTagValue() error = nil")
	}
	if _, err := (&TaggingResource{client: client}).createTagging(context.Background(), taggingResourceModel{TagValueID: types.StringValue("value-id"), ResourceType: types.StringValue("project"), ResourceID: types.StringValue("project-id")}); err == nil {
		t.Fatal("createTagging() error = nil")
	}

	for _, path := range []string{
		"/api/v1/workspaces/current/tag-keys",
		"/api/v1/workspaces/current/tag-keys/key-id/tag-values",
		"/api/v1/workspaces/current/taggings",
	} {
		if counts[path] != 1 {
			t.Fatalf("requests to %s = %d, want 1", path, counts[path])
		}
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
	return newTagTestClientWithOptions(t, handler)
}

func newTagTestClientWithOptions(t *testing.T, handler http.Handler, opts ...option.RequestOption) *langsmith.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return langsmith.NewClient(append([]option.RequestOption{option.WithBaseURL(server.URL), option.WithAPIKey("test-key")}, opts...)...)
}

func tagResourceSchema(t *testing.T, r *TagResource) resource.SchemaResponse {
	t.Helper()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema() diagnostics = %v", resp.Diagnostics)
	}
	return resp
}

func stateForTag(t *testing.T, schema resource.SchemaResponse, model tagResourceModel) tfsdk.State {
	t.Helper()
	state := tfsdk.State{Schema: schema.Schema}
	if diagnostics := state.Set(context.Background(), &model); diagnostics.HasError() {
		t.Fatalf("State.Set() diagnostics = %v", diagnostics)
	}
	return state
}

func decodeJSON(t *testing.T, req *http.Request, value any) {
	t.Helper()
	if err := json.NewDecoder(req.Body).Decode(value); err != nil {
		t.Fatalf("decode request: %v", err)
	}
}
