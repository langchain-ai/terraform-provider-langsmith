package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestWorkspaceDataSourceModelFromListResponse(t *testing.T) {
	createdAt := time.Date(2026, 5, 21, 1, 2, 3, 0, time.UTC)
	model := workspaceDataSourceModelFromListResponse(langsmith.WorkspaceListResponse{
		ID:             "workspace-id",
		CreatedAt:      createdAt,
		DisplayName:    "Demo Workspace",
		IsDeleted:      false,
		IsPersonal:     false,
		DataPlaneURL:   "https://demo.example.com",
		OrganizationID: "org-id",
		TenantHandle:   "demo-workspace",
	})

	if model.ID.ValueString() != "workspace-id" {
		t.Fatalf("ID = %q", model.ID.ValueString())
	}
	if model.DisplayName.ValueString() != "Demo Workspace" {
		t.Fatalf("DisplayName = %q", model.DisplayName.ValueString())
	}
	if model.TenantHandle.ValueString() != "demo-workspace" {
		t.Fatalf("TenantHandle = %q", model.TenantHandle.ValueString())
	}
	if model.CreatedAt.ValueString() != "2026-05-21T01:02:03Z" {
		t.Fatalf("CreatedAt = %q", model.CreatedAt.ValueString())
	}
}

func TestWorkspaceDataSourceLookupByDisplayName(t *testing.T) {
	source := newWorkspaceDataSourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/workspaces" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got, want := r.URL.Query().Get("include_deleted"), "true"; got != want {
			t.Fatalf("include_deleted = %q, want %q", got, want)
		}
		writeJSON(t, w, []langsmith.WorkspaceListResponse{
			{ID: "other-id", DisplayName: "Other Workspace"},
			{ID: "workspace-id", DisplayName: "Demo Workspace", TenantHandle: "demo-workspace"},
		})
	}))

	workspace, err := source.lookupWorkspace(context.Background(), "", "Demo Workspace")
	if err != nil {
		t.Fatalf("lookupWorkspace returned error: %v", err)
	}
	if workspace.ID != "workspace-id" {
		t.Fatalf("ID = %q, want workspace-id", workspace.ID)
	}
}

func TestWorkspaceDataSourceLookupErrorsOnMultipleDisplayNameMatches(t *testing.T) {
	source := newWorkspaceDataSourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []langsmith.WorkspaceListResponse{
			{ID: "workspace-one", DisplayName: "Demo Workspace"},
			{ID: "workspace-two", DisplayName: "Demo Workspace"},
		})
	}))

	_, err := source.lookupWorkspace(context.Background(), "", "Demo Workspace")
	if err == nil || !strings.Contains(err.Error(), "multiple active workspaces") {
		t.Fatalf("lookupWorkspace error = %v, want multiple active workspaces", err)
	}
}

func TestWorkspaceDataSourceLookupIgnoresDeletedWorkspaces(t *testing.T) {
	source := newWorkspaceDataSourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []langsmith.WorkspaceListResponse{
			{ID: "deleted-id", DisplayName: "Demo Workspace", IsDeleted: true},
			{ID: "workspace-id", DisplayName: "Demo Workspace"},
		})
	}))

	workspace, err := source.lookupWorkspace(context.Background(), "", "Demo Workspace")
	if err != nil {
		t.Fatalf("lookupWorkspace returned error: %v", err)
	}
	if workspace.ID != "workspace-id" {
		t.Fatalf("ID = %q, want workspace-id", workspace.ID)
	}
}

func newWorkspaceDataSourceWithServer(t *testing.T, handler http.Handler) *WorkspaceDataSource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &WorkspaceDataSource{
		client: langsmith.NewClient(
			option.WithBaseURL(server.URL),
			option.WithAPIKey("test-key"),
		),
	}
}
