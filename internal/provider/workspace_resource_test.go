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

	if err := resource.deleteWorkspace(context.Background(), "workspace-id", ""); err != nil {
		t.Fatalf("deleteWorkspace returned error: %v", err)
	}
}

func TestWorkspaceResourceDeleteReturnsNotFound(t *testing.T) {
	resource := newWorkspaceResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"workspace not found"}`, http.StatusNotFound)
	}))

	err := resource.deleteWorkspace(context.Background(), "workspace-id", "")
	if !errors.Is(err, errWorkspaceNotFound) {
		t.Fatalf("deleteWorkspace error = %v, want not found", err)
	}
}

func TestWorkspaceResourceRoutesMutationsThroughDataPlane(t *testing.T) {
	var requests []string
	var dataPlaneURL string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/current/data-planes":
			if got, want := r.URL.Query().Get("view"), "SUMMARY"; got != want {
				t.Fatalf("view = %q, want %q", got, want)
			}
			writeJSON(t, w, map[string]any{"data_planes": []map[string]string{
				{"id": "other-dp", "api_url": "https://other.example.com"},
				{"id": "dp-id", "api_url": dataPlaneURL + "/api/v1"},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/dp/api/v1/workspaces":
			writeJSON(t, w, langsmith.WorkspaceNewResponse{ID: "workspace-id"})
		case r.Method == http.MethodPatch && r.URL.Path == "/dp/api/v1/workspaces/workspace-id":
			writeJSON(t, w, langsmith.WorkspaceUpdateResponse{ID: "workspace-id"})
		case r.Method == http.MethodDelete && r.URL.Path == "/dp/api/v1/workspaces/workspace-id":
			if got, want := r.Header.Get("X-Tenant-Id"), "workspace-id"; got != want {
				t.Fatalf("X-Tenant-Id = %q, want %q", got, want)
			}
			writeJSON(t, w, map[string]string{"id": "workspace-id"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces":
			writeJSON(t, w, []langsmith.WorkspaceListResponse{{
				ID:           "workspace-id",
				DisplayName:  "Renamed Workspace",
				DataPlaneURL: dataPlaneURL,
			}})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	dataPlaneURL = server.URL + "/dp"
	resourceUnderTest := &WorkspaceResource{client: langsmith.NewClient(
		option.WithBaseURL(server.URL),
		option.WithAPIKey("test-key"),
		option.WithHTTPClient(server.Client()),
	)}

	ctx := context.Background()
	var schemaResponse resource.SchemaResponse
	resourceUnderTest.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
	plan := tfsdk.Plan{Schema: schemaResponse.Schema}
	if diags := plan.Set(ctx, workspaceResourceModel{
		DisplayName: types.StringValue("Dev Workspace"),
		DataPlaneID: types.StringValue("dp-id"),
	}); diags.HasError() {
		t.Fatalf("set create plan: %v", diags)
	}
	createResponse := resource.CreateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	resourceUnderTest.Create(ctx, resource.CreateRequest{Plan: plan}, &createResponse)
	var created workspaceResourceModel
	createResponse.Diagnostics.Append(createResponse.State.Get(ctx, &created)...)
	if createResponse.Diagnostics.HasError() || created.ID.ValueString() != "workspace-id" || created.DataPlaneID.ValueString() != "dp-id" {
		t.Fatalf("Create() state = %#v, diagnostics = %v", created, createResponse.Diagnostics)
	}

	model, err := resourceUnderTest.updateWorkspace(ctx, "workspace-id", workspaceResourceModel{
		DisplayName: types.StringValue("Renamed Workspace"),
		DataPlaneID: types.StringValue("dp-id"),
	})
	if err != nil {
		t.Fatalf("updateWorkspace returned error: %v", err)
	}
	if model.DataPlaneID.ValueString() != "dp-id" {
		t.Fatalf("DataPlaneID = %q, want dp-id", model.DataPlaneID.ValueString())
	}
	if err := resourceUnderTest.deleteWorkspace(ctx, "workspace-id", "dp-id"); err != nil {
		t.Fatalf("deleteWorkspace returned error: %v", err)
	}
	imported, err := resourceUnderTest.readWorkspace(ctx, "workspace-id", workspaceResourceModel{})
	if err != nil {
		t.Fatalf("readWorkspace returned error: %v", err)
	}
	if imported.DataPlaneID.ValueString() != "dp-id" {
		t.Fatalf("imported DataPlaneID = %q, want dp-id", imported.DataPlaneID.ValueString())
	}
	if !reflect.DeepEqual(requests, []string{
		"GET /orgs/current/data-planes",
		"POST /dp/api/v1/workspaces",
		"GET /api/v1/workspaces",
		"GET /orgs/current/data-planes",
		"PATCH /dp/api/v1/workspaces/workspace-id",
		"GET /api/v1/workspaces",
		"GET /orgs/current/data-planes",
		"DELETE /dp/api/v1/workspaces/workspace-id",
		"GET /api/v1/workspaces",
		"GET /orgs/current/data-planes",
	}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestWorkspaceImportReadPreservesStateOnDataPlaneLookupFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{name: "forbidden", status: http.StatusForbidden},
		{name: "not found", status: http.StatusNotFound},
		{name: "unavailable", status: http.StatusServiceUnavailable},
		{name: "no matching data plane", status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			allowDiscovery := false
			resourceUnderTest := newWorkspaceResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				switch r.URL.Path {
				case "/api/v1/workspaces":
					writeJSON(t, w, []langsmith.WorkspaceListResponse{{
						ID: "workspace-id", DataPlaneURL: "https://dp.example.com",
					}})
				case "/orgs/current/data-planes":
					if allowDiscovery {
						writeJSON(t, w, map[string]any{"data_planes": []map[string]string{{"id": "dp-id", "api_url": "https://dp.example.com"}}})
					} else if tc.status == http.StatusOK {
						writeJSON(t, w, map[string]any{"data_planes": []map[string]string{}})
					} else {
						http.Error(w, `{"detail":"data plane lookup failed"}`, tc.status)
					}
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}), option.WithMaxRetries(0))
			ctx := context.Background()
			var schemaResponse resource.SchemaResponse
			resourceUnderTest.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
			importResponse := resource.ImportStateResponse{State: tfsdk.State{
				Schema: schemaResponse.Schema,
				Raw:    tftypes.NewValue(schemaResponse.Schema.Type().TerraformType(ctx), nil),
			}}
			resourceUnderTest.ImportState(ctx, resource.ImportStateRequest{ID: "workspace-id"}, &importResponse)
			if importResponse.Diagnostics.HasError() {
				t.Fatalf("ImportState() diagnostics = %v", importResponse.Diagnostics)
			}
			readResponse := resource.ReadResponse{State: importResponse.State}
			resourceUnderTest.Read(ctx, resource.ReadRequest{State: importResponse.State}, &readResponse)
			if tc.status == http.StatusForbidden {
				if readResponse.Diagnostics.HasError() || readResponse.Diagnostics.WarningsCount() != 1 {
					t.Fatalf("Read() diagnostics = %v, want one warning", readResponse.Diagnostics)
				}
				var refreshed workspaceResourceModel
				if diags := readResponse.State.Get(ctx, &refreshed); diags.HasError() {
					t.Fatalf("read state: %v", diags)
				}
				if refreshed.ID.ValueString() != "workspace-id" || refreshed.DataPlaneURL.ValueString() != "https://dp.example.com" || !refreshed.DataPlaneID.IsNull() {
					t.Fatalf("Read() state = %#v", refreshed)
				}
				requestsBeforeMutation := requests
				plan := tfsdk.Plan{Schema: schemaResponse.Schema}
				refreshed.DisplayName = types.StringValue("Renamed Workspace")
				refreshed.DataPlaneID = types.StringValue("configured-dp")
				if diags := plan.Set(ctx, refreshed); diags.HasError() {
					t.Fatalf("set update plan: %v", diags)
				}
				updateResponse := resource.UpdateResponse{State: readResponse.State}
				resourceUnderTest.Update(ctx, resource.UpdateRequest{State: readResponse.State, Plan: plan}, &updateResponse)
				deleteResponse := resource.DeleteResponse{State: readResponse.State}
				resourceUnderTest.Delete(ctx, resource.DeleteRequest{State: readResponse.State}, &deleteResponse)
				if !updateResponse.Diagnostics.HasError() || !deleteResponse.Diagnostics.HasError() || requests != requestsBeforeMutation {
					t.Fatalf("unresolved mutations must fail before sending requests: update=%v delete=%v requests=%d", updateResponse.Diagnostics, deleteResponse.Diagnostics, requests)
				}
				if !updateResponse.State.Raw.Equal(readResponse.State.Raw) || !deleteResponse.State.Raw.Equal(readResponse.State.Raw) {
					t.Fatal("unresolved mutation changed state")
				}
				allowDiscovery = true
				recovered := resource.ReadResponse{State: readResponse.State}
				resourceUnderTest.Read(ctx, resource.ReadRequest{State: readResponse.State}, &recovered)
				recovered.Diagnostics.Append(recovered.State.Get(ctx, &refreshed)...)
				if len(recovered.Diagnostics) != 0 || refreshed.DataPlaneID.ValueString() != "dp-id" {
					t.Fatalf("Read() after restoring permission: state=%#v diagnostics=%v", refreshed, recovered.Diagnostics)
				}
				return
			}
			if !readResponse.Diagnostics.HasError() {
				t.Fatal("Read() succeeded despite data plane lookup failure")
			}
			if !readResponse.State.Raw.Equal(importResponse.State.Raw) {
				t.Fatal("Read() changed imported state after data plane lookup failure")
			}
		})
	}
}

func TestWorkspaceDataPlaneReplacement(t *testing.T) {
	for _, tc := range []struct {
		name, previousID, previousURL string
		plannedID                     types.String
		wantReplace, wantError        bool
		unconfigured                  bool
	}{
		{name: "unresolved adoption", previousURL: "https://dp.example.com", plannedID: types.StringValue("dp-id"), wantError: true},
		{name: "unresolved unconfigured", previousURL: "https://dp.example.com", plannedID: types.StringUnknown(), unconfigured: true},
		{name: "unresolved configured unknown", previousURL: "https://dp.example.com", plannedID: types.StringUnknown(), wantError: true},
		{name: "resolved configured unknown", previousID: "dp-id", previousURL: "https://dp.example.com", plannedID: types.StringUnknown(), wantReplace: true},
		{name: "same plane", previousID: "dp-id", previousURL: "https://dp.example.com", plannedID: types.StringValue("dp-id")},
		{name: "different plane", previousID: "dp-id", previousURL: "https://dp.example.com", plannedID: types.StringValue("other-dp"), wantReplace: true},
		{name: "control plane to BYOC", plannedID: types.StringValue("dp-id"), wantReplace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var schemaResponse resource.SchemaResponse
			(&WorkspaceResource{}).Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
			state := tfsdk.State{Schema: schemaResponse.Schema}
			previousID := nullableString(tc.previousID)
			if diags := state.Set(ctx, workspaceResourceModel{
				ID: types.StringValue("workspace-id"), DataPlaneID: previousID, DataPlaneURL: nullableString(tc.previousURL),
			}); diags.HasError() {
				t.Fatalf("set state: %v", diags)
			}
			req := planmodifier.StringRequest{
				Path: path.Root("data_plane_id"), State: state, StateValue: previousID,
				Plan: tfsdk.Plan{Schema: schemaResponse.Schema, Raw: state.Raw}, PlanValue: tc.plannedID, ConfigValue: tc.plannedID,
			}
			if tc.unconfigured {
				req.ConfigValue = types.StringNull()
			}
			resp := planmodifier.StringResponse{PlanValue: tc.plannedID}
			attribute, ok := schemaResponse.Schema.Attributes["data_plane_id"].(schema.StringAttribute)
			if !ok {
				t.Fatalf("data_plane_id attribute type = %T, want schema.StringAttribute", schemaResponse.Schema.Attributes["data_plane_id"])
			}
			for _, modifier := range attribute.PlanModifiers {
				req.PlanValue = resp.PlanValue
				modifier.PlanModifyString(ctx, req, &resp)
			}
			if resp.RequiresReplace != tc.wantReplace || resp.Diagnostics.HasError() != tc.wantError {
				t.Fatalf("replacement=%t diagnostics=%v; want replacement=%t error=%t", resp.RequiresReplace, resp.Diagnostics, tc.wantReplace, tc.wantError)
			}
		})
	}
}

func TestWorkspaceDeleteReportsDataPlaneLookupNotFound(t *testing.T) {
	resourceUnderTest := newWorkspaceResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/orgs/current/data-planes" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		http.Error(w, `{"detail":"data plane lookup failed"}`, http.StatusNotFound)
	}))
	ctx := context.Background()
	var schemaResponse resource.SchemaResponse
	resourceUnderTest.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
	state := tfsdk.State{Schema: schemaResponse.Schema}
	if diags := state.Set(ctx, workspaceResourceModel{
		ID: types.StringValue("workspace-id"), DataPlaneID: types.StringValue("dp-id"),
	}); diags.HasError() {
		t.Fatalf("set state: %v", diags)
	}
	response := resource.DeleteResponse{State: state}
	resourceUnderTest.Delete(ctx, resource.DeleteRequest{State: state}, &response)
	if !response.Diagnostics.HasError() {
		t.Fatal("Delete() succeeded despite data plane lookup failure")
	}
	if !response.State.Raw.Equal(state.Raw) {
		t.Fatal("Delete() changed state after data plane lookup failure")
	}
}

func TestWorkspaceResourceRejectsUnknownDataPlane(t *testing.T) {
	resource := newWorkspaceResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"data_planes": []map[string]string{}})
	}))

	if _, err := resource.dataPlaneOptions(context.Background(), "dp-id"); err == nil {
		t.Fatal("dataPlaneOptions returned nil error for a missing data plane")
	}
}

func TestDataPlaneBaseURL(t *testing.T) {
	got, err := dataPlaneBaseURL("https://dp.example.com/api/v1/")
	if err != nil || got != "https://dp.example.com" {
		t.Fatalf("dataPlaneBaseURL = %q, %v; want https://dp.example.com", got, err)
	}
	for _, raw := range []string{
		"",
		"http://dp.example.com",
		"dp.example.com",
		"https://user:pass@dp.example.com",
		"https://dp.example.com?x=1",
		"https://dp.example.com#frag",
	} {
		if _, err := dataPlaneBaseURL(raw); err == nil {
			t.Fatalf("dataPlaneBaseURL(%q) returned nil error", raw)
		}
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
