package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestWorkspaceNewParamsFromModel(t *testing.T) {
	params := workspaceNewParamsFromModel(workspaceResourceModel{
		ID:           types.StringValue("workspace-id"),
		DisplayName:  types.StringValue("Dev Workspace"),
		TenantHandle: types.StringValue("dev-workspace"),
	})

	if params.DisplayName.Value != "Dev Workspace" {
		t.Fatalf("DisplayName = %q", params.DisplayName.Value)
	}
	if params.ID.Value != "workspace-id" {
		t.Fatalf("ID = %q", params.ID.Value)
	}
	if params.TenantHandle.Value != "dev-workspace" {
		t.Fatalf("TenantHandle = %q", params.TenantHandle.Value)
	}
}

func TestWorkspaceModelFromListResponse(t *testing.T) {
	createdAt := time.Date(2026, 5, 21, 1, 2, 3, 0, time.UTC)
	model := workspaceModelFromListResponse(langsmith.WorkspaceListResponse{
		ID:             "workspace-id",
		CreatedAt:      createdAt,
		DisplayName:    "Dev Workspace",
		IsDeleted:      false,
		IsPersonal:     false,
		DataPlaneURL:   "https://dev.example.com",
		OrganizationID: "org-id",
		TenantHandle:   "dev-workspace",
	}, workspaceResourceModel{})

	if model.ID.ValueString() != "workspace-id" {
		t.Fatalf("ID = %q", model.ID.ValueString())
	}
	if model.DisplayName.ValueString() != "Dev Workspace" {
		t.Fatalf("DisplayName = %q", model.DisplayName.ValueString())
	}
	if model.CreatedAt.ValueString() != "2026-05-21T01:02:03Z" {
		t.Fatalf("CreatedAt = %q", model.CreatedAt.ValueString())
	}
	if model.DataPlaneURL.ValueString() != "https://dev.example.com" {
		t.Fatalf("DataPlaneURL = %q", model.DataPlaneURL.ValueString())
	}
}

func TestWorkspaceTenantHandleSchemaUsesStateForUnknown(t *testing.T) {
	resourceUnderTest := &WorkspaceResource{}
	var resp resource.SchemaResponse
	resourceUnderTest.Schema(context.Background(), resource.SchemaRequest{}, &resp)

	attribute, ok := resp.Schema.Attributes["tenant_handle"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("tenant_handle attribute type = %T, want schema.StringAttribute", resp.Schema.Attributes["tenant_handle"])
	}
	if !hasStringPlanModifier(attribute.PlanModifiers, "useStateForUnknown") {
		t.Fatalf("tenant_handle plan modifiers = %#v, want UseStateForUnknown", attribute.PlanModifiers)
	}
}

func TestWorkspaceReadTreatsDeletedWorkspaceAsNotFound(t *testing.T) {
	resource := newWorkspaceResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/workspaces" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got, want := r.URL.Query().Get("include_deleted"), "true"; got != want {
			t.Fatalf("include_deleted = %q, want %q", got, want)
		}
		writeJSON(t, w, []langsmith.WorkspaceListResponse{{
			ID:          "workspace-id",
			DisplayName: "Dev Workspace",
			IsDeleted:   true,
		}})
	}))

	_, err := resource.readWorkspace(context.Background(), "workspace-id", workspaceResourceModel{})
	if !errors.Is(err, errWorkspaceNotFound) {
		t.Fatalf("readWorkspace error = %v, want errWorkspaceNotFound", err)
	}
}

func TestWorkspaceReadFindsActiveWorkspace(t *testing.T) {
	resource := newWorkspaceResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/workspaces" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		writeJSON(t, w, []langsmith.WorkspaceListResponse{{
			ID:          "workspace-id",
			DisplayName: "Dev Workspace",
			IsDeleted:   false,
		}})
	}))

	model, err := resource.readWorkspace(context.Background(), "workspace-id", workspaceResourceModel{})
	if err != nil {
		t.Fatalf("readWorkspace returned error: %v", err)
	}
	if model.ID.ValueString() != "workspace-id" {
		t.Fatalf("ID = %q, want workspace-id", model.ID.ValueString())
	}
}

func TestWorkspaceResourceUpdatePatchesDisplayNameAndReadsWorkspace(t *testing.T) {
	requests := make([]string, 0, 2)
	createdAt := time.Date(2026, 5, 22, 1, 2, 3, 0, time.UTC)
	resource := newWorkspaceResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/workspaces/workspace-id":
			if got, want := r.Header.Get("X-Tenant-Id"), "workspace-id"; got != want {
				t.Fatalf("X-Tenant-Id = %q, want %q", got, want)
			}
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode update payload: %v", err)
			}
			if payload["display_name"] != "Renamed Workspace" {
				t.Fatalf("payload.display_name = %q, want Renamed Workspace", payload["display_name"])
			}
			writeJSON(t, w, langsmith.WorkspaceUpdateResponse{ID: "workspace-id"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces":
			if got, want := r.Header.Get("X-Tenant-Id"), "configured-workspace-id"; got != want {
				t.Fatalf("X-Tenant-Id = %q, want %q", got, want)
			}
			writeJSON(t, w, []langsmith.WorkspaceListResponse{{
				ID:             "workspace-id",
				CreatedAt:      createdAt,
				DisplayName:    "Renamed Workspace",
				DataPlaneURL:   "https://dev.example.com",
				OrganizationID: "org-id",
				TenantHandle:   "dev-workspace",
			}})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}), option.WithTenantID("configured-workspace-id"))

	model, err := resource.updateWorkspace(context.Background(), "workspace-id", workspaceResourceModel{
		DisplayName: types.StringValue("Renamed Workspace"),
	})
	if err != nil {
		t.Fatalf("updateWorkspace returned error: %v", err)
	}
	if model.CreatedAt.ValueString() != "2026-05-22T01:02:03Z" {
		t.Fatalf("CreatedAt = %q, want API read timestamp", model.CreatedAt.ValueString())
	}
	if !reflect.DeepEqual(requests, []string{
		"PATCH /api/v1/workspaces/workspace-id",
		"GET /api/v1/workspaces",
	}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestWorkspaceResourceDeleteUsesWorkspaceTenant(t *testing.T) {
	resource := newWorkspaceResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodDelete; got != want {
			t.Fatalf("method = %q, want %q", got, want)
		}
		if got, want := r.URL.Path, "/api/v1/workspaces/workspace-id"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("X-Tenant-Id"), "workspace-id"; got != want {
			t.Fatalf("X-Tenant-Id = %q, want %q", got, want)
		}
		writeJSON(t, w, map[string]string{"id": "workspace-id"})
	}), option.WithTenantID("configured-workspace-id"))

	if err := resource.deleteWorkspace(context.Background(), "workspace-id"); err != nil {
		t.Fatalf("deleteWorkspace returned error: %v", err)
	}
}

func TestWorkspaceResourceDeleteReturnsNotFound(t *testing.T) {
	resource := newWorkspaceResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"workspace not found"}`, http.StatusNotFound)
	}))

	err := resource.deleteWorkspace(context.Background(), "workspace-id")
	if !isLangSmithNotFound(err) {
		t.Fatalf("deleteWorkspace error = %v, want not found", err)
	}
}

func newWorkspaceResourceWithServer(t *testing.T, handler http.Handler, opts ...option.RequestOption) *WorkspaceResource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	clientOptions := []option.RequestOption{
		option.WithBaseURL(server.URL),
		option.WithAPIKey("test-key"),
	}
	clientOptions = append(clientOptions, opts...)

	return &WorkspaceResource{
		client: langsmith.NewClient(clientOptions...),
	}
}

func hasStringPlanModifier(modifiers []planmodifier.String, expectedName string) bool {
	for _, modifier := range modifiers {
		if strings.Contains(fmt.Sprintf("%T", modifier), expectedName) {
			return true
		}
	}
	return false
}
