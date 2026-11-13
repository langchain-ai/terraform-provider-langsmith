package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/langchain-ai/langsmith-go/option"
)

func managedToolTestState(t *testing.T, r resource.Resource, model any) tfsdk.State {
	t.Helper()
	var response resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &response)
	state := tfsdk.State{Schema: response.Schema}
	if diags := state.Set(context.Background(), model); diags.HasError() {
		t.Fatalf("set state: %v", diags)
	}
	return state
}

// mustAs asserts a value's type, failing the test instead of panicking.
func mustAs[T any](t *testing.T, value any) T {
	t.Helper()
	typed, ok := value.(T)
	if !ok {
		t.Fatalf("got %T, want %T", value, typed)
	}
	return typed
}

func TestManagedToolProfileLifecycle(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider workspace", true: "resource workspace"}[override], func(t *testing.T) {
			ctx := context.Background()
			model := managedToolProfileModel{Name: types.StringValue("Robin")}
			wantWorkspace, importID := "provider-workspace", "profile-id"
			if override {
				model.WorkspaceID = types.StringValue("resource-workspace")
				wantWorkspace, importID = "resource-workspace", "resource-workspace/profile-id"
			}
			name := "Robin"
			var calls []string
			r := &managedToolProfileResource{client: newTagTestClientWithOptions(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if got := req.Header.Get("X-Tenant-Id"); got != wantWorkspace {
					t.Errorf("workspace = %q, want %q", got, wantWorkspace)
				}
				calls = append(calls, req.Method+" "+req.URL.Path)
				switch req.Method {
				case http.MethodPost, http.MethodPatch:
					var payload map[string]string
					decodeJSON(t, req, &payload)
					if len(payload) != 1 || payload["name"] == "" {
						t.Errorf("payload = %v", payload)
					}
					name = payload["name"]
					writeJSON(t, w, managedToolProfileAPI{ID: "profile-id", Name: name})
				case http.MethodGet:
					writeJSON(t, w, managedToolProfileAPI{ID: "profile-id", Name: name})
				case http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
				}
			}), option.WithTenantID("provider-workspace"))}
			state := managedToolTestState(t, r, &model)

			created := resource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan(state)}, &created)
			if created.Diagnostics.HasError() {
				t.Fatal(created.Diagnostics)
			}

			renamed := model
			renamed.ID = types.StringValue("profile-id")
			renamed.Name = types.StringValue("Robin bot")
			plan := managedToolTestState(t, r, &renamed)
			updated := resource.UpdateResponse{State: created.State}
			r.Update(ctx, resource.UpdateRequest{Plan: tfsdk.Plan(plan), State: created.State}, &updated)
			if updated.Diagnostics.HasError() {
				t.Fatal(updated.Diagnostics)
			}

			imported := resource.ImportStateResponse{State: tfsdk.State{Schema: state.Schema, Raw: tftypes.NewValue(state.Raw.Type(), nil)}}
			r.ImportState(ctx, resource.ImportStateRequest{ID: importID}, &imported)
			if imported.Diagnostics.HasError() {
				t.Fatal(imported.Diagnostics)
			}
			read := resource.ReadResponse{State: imported.State}
			r.Read(ctx, resource.ReadRequest{State: imported.State}, &read)
			if read.Diagnostics.HasError() {
				t.Fatal(read.Diagnostics)
			}
			var actual managedToolProfileModel
			if diags := read.State.Get(ctx, &actual); diags.HasError() {
				t.Fatal(diags)
			}
			if actual.ID.ValueString() != "profile-id" || actual.Name.ValueString() != "Robin bot" || !actual.WorkspaceID.Equal(model.WorkspaceID) {
				t.Fatalf("read = %#v", actual)
			}

			deleted := resource.DeleteResponse{State: read.State}
			r.Delete(ctx, resource.DeleteRequest{State: read.State}, &deleted)
			if deleted.Diagnostics.HasError() {
				t.Fatal(deleted.Diagnostics)
			}
			want := "POST /v1/managed-tools/profiles,PATCH /v1/managed-tools/profiles/profile-id,GET /v1/managed-tools/profiles/profile-id,DELETE /v1/managed-tools/profiles/profile-id"
			if got := strings.Join(calls, ","); got != want {
				t.Fatalf("calls = %s", got)
			}
		})
	}
}

func TestManagedToolProfileReadNotFoundRemovesState(t *testing.T) {
	r := &managedToolProfileResource{client: newTagTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"not found"}`))
	}))}
	state := managedToolTestState(t, r, &managedToolProfileModel{ID: types.StringValue("profile-id"), Name: types.StringValue("Robin")})
	response := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &response)
	if response.Diagnostics.HasError() || !response.State.Raw.IsNull() {
		t.Fatalf("diagnostics=%v removed=%t", response.Diagnostics, response.State.Raw.IsNull())
	}
}

func TestSplitWorkspaceImportID(t *testing.T) {
	for _, tc := range []struct {
		raw, workspace, id string
		ok                 bool
	}{
		{raw: "id", id: "id", ok: true},
		{raw: "ws/id", workspace: "ws", id: "id", ok: true},
		{raw: ""},
		{raw: "/id"},
		{raw: "ws/"},
		{raw: "ws/id/extra"},
	} {
		workspace, id, ok := splitWorkspaceImportID(tc.raw)
		if workspace != tc.workspace || id != tc.id || ok != tc.ok {
			t.Errorf("splitWorkspaceImportID(%q) = %q, %q, %t", tc.raw, workspace, id, ok)
		}
	}
}

func managedToolProfileCredentialTestModel() managedToolProfileCredentialModel {
	return managedToolProfileCredentialModel{
		ProfileID: types.StringValue("profile-id"),
		ServerID:  types.StringValue("server-id"),
		Fields:    types.MapValueMust(types.StringType, map[string]attr.Value{"Authorization": types.StringValue("Bearer first")}),
	}
}

func TestManagedToolProfileCredentialLifecycle(t *testing.T) {
	ctx := context.Background()
	var calls, bodies []string
	r := &managedToolProfileCredentialResource{client: newTagTestClientWithOptions(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if got := req.Header.Get("X-Tenant-Id"); got != "resource-workspace" {
			t.Errorf("workspace = %q", got)
		}
		calls = append(calls, req.Method+" "+req.URL.Path)
		switch req.Method {
		case http.MethodPut:
			var payload struct {
				Fields map[string]string `json:"fields"`
			}
			decodeJSON(t, req, &payload)
			bodies = append(bodies, payload.Fields["Authorization"])
			writeJSON(t, w, map[string]any{"id": "server-id"})
		case http.MethodGet:
			writeJSON(t, w, managedToolProfileAPI{ID: "profile-id", Name: "Robin"})
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	}), option.WithTenantID("provider-workspace"))}
	model := managedToolProfileCredentialTestModel()
	model.WorkspaceID = types.StringValue("resource-workspace")
	state := managedToolTestState(t, r, &model)

	created := resource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan(state)}, &created)
	if created.Diagnostics.HasError() {
		t.Fatal(created.Diagnostics)
	}
	var createdModel managedToolProfileCredentialModel
	if diags := created.State.Get(ctx, &createdModel); diags.HasError() {
		t.Fatal(diags)
	}
	if createdModel.ID.ValueString() != "profile-id/server-id" {
		t.Fatalf("id = %s", createdModel.ID)
	}

	read := resource.ReadResponse{State: created.State}
	r.Read(ctx, resource.ReadRequest{State: created.State}, &read)
	if read.Diagnostics.HasError() || read.State.Raw.IsNull() {
		t.Fatalf("read diagnostics=%v removed=%t", read.Diagnostics, read.State.Raw.IsNull())
	}
	var readModel managedToolProfileCredentialModel
	if diags := read.State.Get(ctx, &readModel); diags.HasError() {
		t.Fatal(diags)
	}
	if !readModel.Fields.Equal(model.Fields) {
		t.Fatal("read must keep configured fields")
	}

	rotated := createdModel
	rotated.Fields = types.MapValueMust(types.StringType, map[string]attr.Value{"Authorization": types.StringValue("Bearer second")})
	plan := managedToolTestState(t, r, &rotated)
	updated := resource.UpdateResponse{State: read.State}
	r.Update(ctx, resource.UpdateRequest{Plan: tfsdk.Plan(plan), State: read.State}, &updated)
	if updated.Diagnostics.HasError() {
		t.Fatal(updated.Diagnostics)
	}

	deleted := resource.DeleteResponse{State: updated.State}
	r.Delete(ctx, resource.DeleteRequest{State: updated.State}, &deleted)
	if deleted.Diagnostics.HasError() {
		t.Fatal(deleted.Diagnostics)
	}
	path := "/v1/managed-tools/profiles/profile-id/servers/server-id/credential"
	want := "PUT " + path + ",GET /v1/managed-tools/profiles/profile-id,PUT " + path + ",DELETE " + path
	if got := strings.Join(calls, ","); got != want {
		t.Fatalf("calls = %s", got)
	}
	if strings.Join(bodies, ",") != "Bearer first,Bearer second" {
		t.Fatalf("bodies = %v", bodies)
	}
}

func TestManagedToolProfileCredentialErrorsSuppressResponse(t *testing.T) {
	r := &managedToolProfileCredentialResource{client: newTagTestClientWithOptions(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"detail":"rejected Bearer first"}`))
	}), option.WithMaxRetries(0))}
	model := managedToolProfileCredentialTestModel()
	state := managedToolTestState(t, r, &model)
	created := resource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan(state)}, &created)
	if !created.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	detail := created.Diagnostics.Errors()[0].Detail()
	if strings.Contains(detail, "Bearer first") || !strings.Contains(detail, "HTTP 409") {
		t.Fatalf("detail = %q", detail)
	}
}

func TestManagedToolProfileCredentialMissingProfileOrCredential(t *testing.T) {
	r := &managedToolProfileCredentialResource{client: newTagTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"not found"}`))
	}))}
	model := managedToolProfileCredentialTestModel()
	model.ID = types.StringValue("profile-id/server-id")
	state := managedToolTestState(t, r, &model)
	read := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &read)
	if read.Diagnostics.HasError() || !read.State.Raw.IsNull() {
		t.Fatalf("read diagnostics=%v removed=%t", read.Diagnostics, read.State.Raw.IsNull())
	}
	deleted := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleted)
	if deleted.Diagnostics.HasError() {
		t.Fatal(deleted.Diagnostics)
	}
}

func TestManagedToolProfileCredentialImport(t *testing.T) {
	ctx := context.Background()
	r := &managedToolProfileCredentialResource{}
	state := managedToolTestState(t, r, &managedToolProfileCredentialModel{Fields: types.MapNull(types.StringType)})
	for _, tc := range []struct {
		id, workspace string
		ok            bool
	}{
		{id: "profile-id/server-id", ok: true},
		{id: "ws/profile-id/server-id", workspace: "ws", ok: true},
		{id: "profile-id"},
		{id: "profile-id/"},
		{id: "a/b/c/d"},
	} {
		t.Run(strings.ReplaceAll(tc.id, "/", "_"), func(t *testing.T) {
			response := resource.ImportStateResponse{State: tfsdk.State{Schema: state.Schema, Raw: tftypes.NewValue(state.Raw.Type(), nil)}}
			r.ImportState(ctx, resource.ImportStateRequest{ID: tc.id}, &response)
			if response.Diagnostics.HasError() == tc.ok {
				t.Fatalf("diagnostics = %v", response.Diagnostics)
			}
			if !tc.ok {
				return
			}
			var imported managedToolProfileCredentialModel
			if diags := response.State.Get(ctx, &imported); diags.HasError() {
				t.Fatal(diags)
			}
			if imported.ProfileID.ValueString() != "profile-id" || imported.ServerID.ValueString() != "server-id" || imported.ID.ValueString() != "profile-id/server-id" || imported.WorkspaceID.ValueString() != tc.workspace {
				t.Fatalf("imported = %#v", imported)
			}
		})
	}
}
