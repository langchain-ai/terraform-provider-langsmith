package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/langchain-ai/langsmith-go/option"
)

func managedToolKitTool(serverID, builtin, name, exposed string) attr.Value {
	return types.ObjectValueMust(managedToolKitToolTypes, map[string]attr.Value{
		"server_id":          nullableString(serverID),
		"builtin_server_key": nullableString(builtin),
		"tool_name":          types.StringValue(name),
		"exposed_name":       nullableString(exposed),
	})
}

func managedToolKitTestModel(tools ...attr.Value) managedToolKitModel {
	if len(tools) == 0 {
		tools = []attr.Value{managedToolKitTool("server-b", "", "search", ""), managedToolKitTool("server-a", "", "list", "List things")}
	}
	return managedToolKitModel{
		Name:             types.StringValue("Robin"),
		Description:      types.StringValue(""),
		ConnectionPolicy: types.StringValue("ALLOW_PARTIAL"),
		Tools:            types.SetValueMust(types.ObjectType{AttrTypes: managedToolKitToolTypes}, tools),
	}
}

func TestManagedToolKitLifecycle(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider workspace", true: "resource workspace"}[override], func(t *testing.T) {
			ctx := context.Background()
			model := managedToolKitTestModel()
			wantWorkspace, importID := "provider-workspace", "kit-id"
			if override {
				model.WorkspaceID = types.StringValue("resource-workspace")
				wantWorkspace, importID = "resource-workspace", "resource-workspace/kit-id"
			}
			kit := managedToolKitAPI{ID: "kit-id", Name: "Robin", ConnectionPolicy: "ALLOW_PARTIAL", Scope: "workspace", MCPPath: "/v1/managed-tools/kits/kit-id/mcp"}
			var calls []string
			r := &managedToolKitResource{client: newTagTestClientWithOptions(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if got := req.Header.Get("X-Tenant-Id"); got != wantWorkspace {
					t.Errorf("workspace = %q, want %q", got, wantWorkspace)
				}
				calls = append(calls, req.Method+" "+req.URL.Path)
				switch req.Method {
				case http.MethodPost:
					var payload map[string]json.RawMessage
					decodeJSON(t, req, &payload)
					wantTools := `[{"server_id":"server-a","tool_name":"list","exposed_name":"List things"},{"server_id":"server-b","tool_name":"search"}]`
					if string(payload["scope"]) != `"workspace"` || string(payload["connection_policy"]) != `"ALLOW_PARTIAL"` || string(payload["tools"]) != wantTools {
						t.Errorf("create payload = %s", payload)
					}
					kit.Tools = []managedToolKitToolAPI{{ServerID: "server-b", ToolName: "search"}, {ServerID: "server-a", ToolName: "list", ExposedName: "List things"}}
					writeJSON(t, w, kit)
				case http.MethodPatch:
					var payload map[string]json.RawMessage
					decodeJSON(t, req, &payload)
					if _, ok := payload["scope"]; ok {
						t.Error("update must not send scope")
					}
					var tools []managedToolKitToolAPI
					if err := json.Unmarshal(payload["tools"], &tools); err != nil {
						t.Fatal(err)
					}
					if len(tools) != 3 || string(payload["connection_policy"]) != `"REQUIRE_ALL"` || string(payload["description"]) != `"Bot tools"` {
						t.Errorf("update payload = %s", payload)
					}
					kit.Tools, kit.ConnectionPolicy, kit.Description = tools, "REQUIRE_ALL", "Bot tools"
					writeJSON(t, w, kit)
				case http.MethodGet:
					writeJSON(t, w, kit)
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
			var createdModel managedToolKitModel
			if diags := created.State.Get(ctx, &createdModel); diags.HasError() {
				t.Fatal(diags)
			}
			if !createdModel.Tools.Equal(model.Tools) || createdModel.MCPPath.ValueString() != kit.MCPPath {
				t.Fatalf("created = %#v", createdModel)
			}

			next := createdModel
			next.Description = types.StringValue("Bot tools")
			next.ConnectionPolicy = types.StringValue("REQUIRE_ALL")
			next.Tools = types.SetValueMust(types.ObjectType{AttrTypes: managedToolKitToolTypes}, append(model.Tools.Elements(), managedToolKitTool("", "github", "get_issue", "")))
			plan := managedToolTestState(t, r, &next)
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
			var actual managedToolKitModel
			if diags := read.State.Get(ctx, &actual); diags.HasError() {
				t.Fatal(diags)
			}
			if !actual.Tools.Equal(next.Tools) || actual.Description.ValueString() != "Bot tools" || !actual.WorkspaceID.Equal(model.WorkspaceID) {
				t.Fatalf("read = %#v", actual)
			}

			deleted := resource.DeleteResponse{State: read.State}
			r.Delete(ctx, resource.DeleteRequest{State: read.State}, &deleted)
			if deleted.Diagnostics.HasError() {
				t.Fatal(deleted.Diagnostics)
			}
			want := "POST /v1/managed-tools/kits,PATCH /v1/managed-tools/kits/kit-id,GET /v1/managed-tools/kits/kit-id,DELETE /v1/managed-tools/kits/kit-id"
			if got := strings.Join(calls, ","); got != want {
				t.Fatalf("calls = %s", got)
			}
		})
	}
}

func TestManagedToolKitReadFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body             string
		status                 int
		wantError, wantRemoved bool
	}{
		{name: "external deletion", status: 404, body: `{"detail":"not found"}`, wantRemoved: true},
		{name: "forbidden", status: 403, body: `{"detail":"forbidden"}`, wantError: true},
		{name: "personal kit rejected", body: `{"id":"kit-id","scope":"personal","tools":[]}`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &managedToolKitResource{client: newTagTestClientWithOptions(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write([]byte(tc.body))
			}), option.WithMaxRetries(0))}
			model := managedToolKitTestModel()
			model.ID = types.StringValue("kit-id")
			state := managedToolTestState(t, r, &model)
			response := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &response)
			if response.Diagnostics.HasError() != tc.wantError || response.State.Raw.IsNull() != tc.wantRemoved {
				t.Fatalf("diagnostics=%v removed=%t", response.Diagnostics, response.State.Raw.IsNull())
			}
		})
	}
}

func TestManagedToolKitRejectsDuplicateTools(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tools     []attr.Value
		wantError bool
	}{
		{name: "same tool, different label", tools: []attr.Value{managedToolKitTool("server-a", "", "list", ""), managedToolKitTool("server-a", "", "list", "Other")}, wantError: true},
		{name: "same name, different servers", tools: []attr.Value{managedToolKitTool("server-a", "", "list", ""), managedToolKitTool("server-b", "", "list", "")}},
		{name: "same name, server and builtin", tools: []attr.Value{managedToolKitTool("server-a", "", "list", ""), managedToolKitTool("", "server-a", "list", "")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &managedToolKitResource{}
			model := managedToolKitTestModel(tc.tools...)
			state := managedToolTestState(t, r, &model)
			response := resource.ValidateConfigResponse{}
			r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: tfsdk.Config(state)}, &response)
			if response.Diagnostics.HasError() != tc.wantError {
				t.Fatalf("diagnostics = %v", response.Diagnostics)
			}
		})
	}
}

func TestManagedToolKitProtocolPlanning(t *testing.T) {
	ctx := context.Background()
	server, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatal(err)
	}
	r := &managedToolKitResource{}
	prior := managedToolKitTestModel()
	prior.ID = types.StringValue("kit-id")
	prior.MCPPath = types.StringValue("/v1/managed-tools/kits/kit-id/mcp")
	priorState := managedToolTestState(t, r, &prior)
	dynamic := func(state tfsdk.State) *tfprotov6.DynamicValue {
		value, err := tfprotov6.NewDynamicValue(state.Raw.Type(), state.Raw)
		if err != nil {
			t.Fatal(err)
		}
		return &value
	}
	config := prior
	config.ID = types.StringNull()
	config.MCPPath = types.StringNull()
	config.Description = types.StringNull()
	config.ConnectionPolicy = types.StringNull()
	response, err := server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{
		TypeName:         "langsmith_managed_tool_kit",
		PriorState:       dynamic(priorState),
		ProposedNewState: dynamic(priorState),
		Config:           dynamic(managedToolTestState(t, r, &config)),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range response.Diagnostics {
		if diagnostic.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("plan diagnostic: %s: %s", diagnostic.Summary, diagnostic.Detail)
		}
	}
	planned, err := response.PlannedState.Unmarshal(priorState.Raw.Type())
	if err != nil {
		t.Fatal(err)
	}
	if !planned.Equal(priorState.Raw) || len(response.RequiresReplace) != 0 {
		t.Fatalf("no-op plan changed state: %s; replacement=%v", planned, response.RequiresReplace)
	}
}
