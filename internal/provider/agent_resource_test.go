package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/defaults"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func agentTestSchema(t *testing.T) schema.Schema {
	t.Helper()
	var response resource.SchemaResponse
	NewAgentResource().Schema(context.Background(), resource.SchemaRequest{}, &response)
	if response.Diagnostics.HasError() {
		t.Fatal(response.Diagnostics)
	}
	return response.Schema
}

func agentTestState(t *testing.T, model agentResourceModel) tfsdk.State {
	t.Helper()
	state := tfsdk.State{Schema: agentTestSchema(t)}
	if diagnostics := state.Set(context.Background(), &model); diagnostics.HasError() {
		t.Fatal(diagnostics)
	}
	return state
}

func agentTestModel(t *testing.T, state tfsdk.State) agentResourceModel {
	t.Helper()
	var model agentResourceModel
	if diagnostics := state.Get(context.Background(), &model); diagnostics.HasError() {
		t.Fatal(diagnostics)
	}
	return model
}

func agentTestPlan(t *testing.T, model agentResourceModel) tfsdk.Plan {
	t.Helper()
	state := agentTestState(t, model)
	return tfsdk.Plan(state)
}

func newAgentTestResource(t *testing.T, handler http.HandlerFunc) *AgentResource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	r := &AgentResource{}
	var response resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: langsmith.NewClient(
		option.WithBaseURL(server.URL), option.WithAPIKey("offline-key"), option.WithTenantID("provider-workspace"), option.WithMaxRetries(0),
	)}, &response)
	if response.Diagnostics.HasError() {
		t.Fatal(response.Diagnostics)
	}
	return r
}

func agentTestInput(workspace types.String) agentResourceModel {
	return agentResourceModel{AgentID: types.StringValue("my-agent"), WorkspaceID: workspace, Name: types.StringValue("My Agent"), SourceType: types.StringValue("EXTERNAL"), AgentType: types.StringValue("CUSTOM"), Environments: types.MapUnknown(types.ObjectType{AttrTypes: agentEnvironmentAttributeTypes})}
}

func assertAgentTestPayload(t *testing.T, req *http.Request, want map[string]any) {
	t.Helper()
	var got map[string]any
	if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
		t.Errorf("decode payload: %v", err)
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s payload = %#v, want %#v", req.Method, got, want)
	}
}

func TestAgentResourceFrameworkCRUD(t *testing.T) {
	ctx := context.Background()
	for _, workspace := range []types.String{types.StringNull(), types.StringValue("override-workspace")} {
		t.Run(workspace.String(), func(t *testing.T) {
			selected := "provider-workspace"
			if !workspace.IsNull() {
				selected = workspace.ValueString()
			}
			var requests []string
			api := agentAPI{ID: "my-agent", Name: "My Agent", SourceType: "EXTERNAL", AgentType: "CUSTOM", CreatedAt: "2026-01-01T00:00:00Z", Environments: []agentEnvironmentAPI{
				{ID: "local-env", Environment: "LOCAL", TracerSessionID: "local-project", TracerSessionName: "Local"},
				{ID: "prod-env", Environment: "PRODUCTION", TracerSessionID: "prod-project", TracerSessionName: "Production"},
			}}
			r := newAgentTestResource(t, func(w http.ResponseWriter, req *http.Request) {
				requests = append(requests, req.Method+" "+req.URL.RequestURI())
				if got := req.Header.Get("X-Tenant-Id"); got != selected {
					t.Errorf("workspace = %q, want %q", got, selected)
				}
				wantPath := "/api/v1/agents/my-agent"
				if req.Method == http.MethodPost {
					wantPath = "/api/v1/agents"
				}
				if req.URL.RequestURI() != wantPath {
					t.Errorf("path = %q, want %q", req.URL.RequestURI(), wantPath)
				}
				switch req.Method {
				case http.MethodPost:
					assertAgentTestPayload(t, req, map[string]any{"id": "my-agent", "name": "My Agent", "source_type": "EXTERNAL", "agent_type": "CUSTOM"})
				case http.MethodPatch:
					assertAgentTestPayload(t, req, map[string]any{"name": "Renamed", "agent_type": "CODING"})
					api.Name, api.AgentType = "Renamed", "CODING"
				case http.MethodGet:
				case http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
					return
				default:
					t.Errorf("unexpected method: %s", req.Method)
					http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
					return
				}
				writeJSON(t, w, api)
			})
			input := agentTestInput(workspace)
			created := resource.CreateResponse{State: tfsdk.State{Schema: agentTestSchema(t)}}
			r.Create(ctx, resource.CreateRequest{Plan: agentTestPlan(t, input)}, &created)
			if created.Diagnostics.HasError() {
				t.Fatal(created.Diagnostics)
			}
			got := agentTestModel(t, created.State)
			want := input
			want.ID, want.CreatedAt = types.StringValue("my-agent"), types.StringValue(api.CreatedAt)
			want.Environments = types.MapValueMust(types.ObjectType{AttrTypes: agentEnvironmentAttributeTypes}, map[string]attr.Value{
				"LOCAL":      types.ObjectValueMust(agentEnvironmentAttributeTypes, map[string]attr.Value{"id": types.StringValue("local-env"), "tracer_session_id": types.StringValue("local-project"), "tracer_session_name": types.StringValue("Local")}),
				"PRODUCTION": types.ObjectValueMust(agentEnvironmentAttributeTypes, map[string]attr.Value{"id": types.StringValue("prod-env"), "tracer_session_id": types.StringValue("prod-project"), "tracer_session_name": types.StringValue("Production")}),
			})
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("created state = %#v, want %#v", got, want)
			}

			api.Environments = api.Environments[1:]
			api.Name = "External rename"
			read := resource.ReadResponse{State: created.State}
			r.Read(ctx, resource.ReadRequest{State: created.State}, &read)
			if read.Diagnostics.HasError() {
				t.Fatal(read.Diagnostics)
			}
			got = agentTestModel(t, read.State)
			if got.Name.ValueString() != "External rename" || len(got.Environments.Elements()) != 1 || got.Environments.Elements()["PRODUCTION"] == nil || !got.WorkspaceID.Equal(workspace) {
				t.Fatalf("refreshed state = %#v", got)
			}

			got.Name, got.AgentType = types.StringValue("Renamed"), types.StringValue("CODING")
			updated := resource.UpdateResponse{State: read.State}
			r.Update(ctx, resource.UpdateRequest{Plan: agentTestPlan(t, got), State: read.State}, &updated)
			if updated.Diagnostics.HasError() {
				t.Fatal(updated.Diagnostics)
			}
			got = agentTestModel(t, updated.State)
			if got.Name.ValueString() != "Renamed" || got.AgentType.ValueString() != "CODING" || !got.WorkspaceID.Equal(workspace) {
				t.Fatalf("updated state = %#v", got)
			}
			deleted := resource.DeleteResponse{State: updated.State}
			r.Delete(ctx, resource.DeleteRequest{State: updated.State}, &deleted)
			if deleted.Diagnostics.HasError() {
				t.Fatal(deleted.Diagnostics)
			}
			wantRequests := []string{"POST /api/v1/agents", "GET /api/v1/agents/my-agent", "PATCH /api/v1/agents/my-agent", "DELETE /api/v1/agents/my-agent"}
			if !reflect.DeepEqual(requests, wantRequests) {
				t.Fatalf("requests = %v, want %v", requests, wantRequests)
			}

			selected = "provider-workspace"
			_, err := r.readAgent(ctx, agentTestInput(types.StringNull()))
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAgentResourceFrameworkHTTPErrors(t *testing.T) {
	ctx := context.Background()
	for _, workspace := range []types.String{types.StringNull(), types.StringValue("override-workspace")} {
		for _, status := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusInternalServerError} {
			for _, operation := range []string{"create", "read", "update", "delete"} {
				t.Run(fmt.Sprintf("%s/%d/%s", workspace.String(), status, operation), func(t *testing.T) {
					requests := 0
					methods := map[string]string{"create": "POST", "read": "GET", "update": "PATCH", "delete": "DELETE"}
					r := newAgentTestResource(t, func(w http.ResponseWriter, req *http.Request) {
						requests++
						wantPath := "/api/v1/agents/my-agent"
						if operation == "create" {
							wantPath = "/api/v1/agents"
						}
						if req.Method != methods[operation] || req.URL.RequestURI() != wantPath {
							t.Errorf("request = %s %s", req.Method, req.URL.RequestURI())
						}
						selected := "provider-workspace"
						if !workspace.IsNull() {
							selected = workspace.ValueString()
						}
						if req.Header.Get("X-Tenant-Id") != selected {
							t.Errorf("workspace = %q, want %q", req.Header.Get("X-Tenant-Id"), selected)
						}
						http.Error(w, "contract failure", status)
					})
					input := agentTestInput(workspace)
					input.ID = input.AgentID
					state := agentTestState(t, input)
					var diagnostics diag.Diagnostics
					switch operation {
					case "create":
						response := resource.CreateResponse{State: state}
						r.Create(ctx, resource.CreateRequest{Plan: agentTestPlan(t, input)}, &response)
						diagnostics = response.Diagnostics
					case "read":
						response := resource.ReadResponse{State: state}
						r.Read(ctx, resource.ReadRequest{State: state}, &response)
						diagnostics = response.Diagnostics
						if status == http.StatusNotFound {
							if !response.State.Raw.IsNull() {
								t.Fatal("404 read did not remove resource")
							}
						} else if !response.State.Raw.Equal(state.Raw) {
							t.Fatal("failed read modified prior state")
						}
					case "update":
						response := resource.UpdateResponse{State: state}
						r.Update(ctx, resource.UpdateRequest{Plan: agentTestPlan(t, input), State: state}, &response)
						diagnostics = response.Diagnostics
					case "delete":
						response := resource.DeleteResponse{State: state}
						r.Delete(ctx, resource.DeleteRequest{State: state}, &response)
						diagnostics = response.Diagnostics
					}
					if requests != 1 {
						t.Fatalf("requests = %d, want 1", requests)
					}
					if status == http.StatusNotFound && (operation == "read" || operation == "delete") {
						if diagnostics.HasError() {
							t.Fatal(diagnostics)
						}
					} else if !diagnostics.HasError() || !strings.Contains(diagnostics.Errors()[0].Detail(), fmt.Sprint(status)) {
						t.Fatalf("diagnostics = %v, want HTTP %d", diagnostics, status)
					}
				})
			}
		}
	}
}

func TestAgentResourceImport(t *testing.T) {
	ctx := context.Background()
	for _, id := range []string{"my-agent", "override-workspace/my-agent"} {
		t.Run(id, func(t *testing.T) {
			workspace := types.StringNull()
			selected := "provider-workspace"
			if strings.Contains(id, "/") {
				workspace = types.StringValue("override-workspace")
				selected = "override-workspace"
			}
			requests := 0
			r := newAgentTestResource(t, func(w http.ResponseWriter, req *http.Request) {
				requests++
				if req.Method != "GET" || req.URL.RequestURI() != "/api/v1/agents/my-agent" || req.Header.Get("X-Tenant-Id") != selected {
					t.Errorf("import read = %s %s, workspace %q", req.Method, req.URL.RequestURI(), req.Header.Get("X-Tenant-Id"))
				}
				writeJSON(t, w, agentAPI{ID: "my-agent", Name: "Imported", SourceType: "EXTERNAL", AgentType: "CUSTOM"})
			})
			s := agentTestSchema(t)
			response := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}}
			r.ImportState(ctx, resource.ImportStateRequest{ID: id}, &response)
			if response.Diagnostics.HasError() {
				t.Fatal(response.Diagnostics)
			}
			imported := agentTestModel(t, response.State)
			if imported.ID.ValueString() != "my-agent" || imported.AgentID.ValueString() != "my-agent" || !imported.WorkspaceID.Equal(workspace) {
				t.Fatalf("imported = %#v", imported)
			}
			read := resource.ReadResponse{State: response.State}
			r.Read(ctx, resource.ReadRequest{State: response.State}, &read)
			if read.Diagnostics.HasError() {
				t.Fatal(read.Diagnostics)
			}
			got := agentTestModel(t, read.State)
			if requests != 1 || got.Name.ValueString() != "Imported" || !got.WorkspaceID.Equal(workspace) {
				t.Fatalf("read = %#v; requests = %d", got, requests)
			}
		})
	}
	for _, id := range []string{"", "/my-agent", "ws/", "ws/a/b", "My-agent", "9agent", "agent-", "agent_name", " agent", "agent ", strings.Repeat("a", 64), "ws/Bad"} {
		t.Run("invalid/"+id, func(t *testing.T) {
			s := agentTestSchema(t)
			response := resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}}
			(&AgentResource{}).ImportState(ctx, resource.ImportStateRequest{ID: id}, &response)
			if !response.Diagnostics.HasError() || response.Diagnostics.Errors()[0].Summary() != "Invalid Agent Import ID" {
				t.Fatalf("diagnostics = %v", response.Diagnostics)
			}
		})
	}
}

func agentTestStringAttribute(t *testing.T, s schema.Schema, name string) schema.StringAttribute {
	t.Helper()
	attribute, ok := s.Attributes[name].(schema.StringAttribute)
	if !ok {
		t.Fatalf("%s attribute = %T, want schema.StringAttribute", name, s.Attributes[name])
	}
	return attribute
}

func TestAgentResourceSchema(t *testing.T) {
	ctx := context.Background()
	s := agentTestSchema(t)
	for _, name := range []string{"agent_id", "source_type"} {
		if !s.Attributes[name].IsRequired() {
			t.Errorf("%s must be required", name)
		}
	}
	for _, name := range []string{"id", "created_at", "environments"} {
		if !s.Attributes[name].IsComputed() || s.Attributes[name].IsOptional() {
			t.Errorf("%s must be computed-only", name)
		}
	}
	for _, name := range []string{"name", "agent_type"} {
		if !s.Attributes[name].IsOptional() || !s.Attributes[name].IsComputed() {
			t.Errorf("%s must be optional+computed", name)
		}
	}
	if !s.Attributes["workspace_id"].IsOptional() {
		t.Fatal("workspace_id must be optional")
	}
	env, ok := s.Attributes["environments"].(schema.MapNestedAttribute)
	if !ok {
		t.Fatalf("environments attribute = %T, want schema.MapNestedAttribute", s.Attributes["environments"])
	}
	if len(env.NestedObject.Attributes) != 3 {
		t.Fatalf("environment attributes = %v", env.NestedObject.Attributes)
	}
	for _, name := range []string{"id", "tracer_session_id", "tracer_session_name"} {
		if !env.NestedObject.Attributes[name].IsComputed() {
			t.Errorf("environment %s must be computed", name)
		}
	}
	defaultResponse := defaults.StringResponse{}
	agentTestStringAttribute(t, s, "agent_type").Default.DefaultString(ctx, defaults.StringRequest{}, &defaultResponse)
	if defaultResponse.Diagnostics.HasError() || defaultResponse.PlanValue.ValueString() != "CUSTOM" {
		t.Fatalf("default = %#v", defaultResponse)
	}

	for _, name := range []string{"agent_id", "workspace_id", "source_type"} {
		attribute := agentTestStringAttribute(t, s, name)
		if len(attribute.PlanModifiers) == 0 {
			t.Fatalf("%s missing replacement modifier", name)
		}
		for _, changed := range []bool{false, true} {
			before, after := agentTestInput(types.StringValue("old")), agentTestInput(types.StringValue("old"))
			old, next := types.StringValue("old"), types.StringValue("old")
			if changed {
				next = types.StringValue("new")
			}
			state, plan := agentTestState(t, before), agentTestPlan(t, after)
			for _, modifier := range attribute.PlanModifiers {
				response := planmodifier.StringResponse{PlanValue: next}
				modifier.PlanModifyString(ctx, planmodifier.StringRequest{Path: path.Root(name), State: state, Plan: plan, StateValue: old, PlanValue: next, ConfigValue: next}, &response)
				if response.Diagnostics.HasError() || response.RequiresReplace != changed {
					t.Fatalf("%s changed=%v response=%#v", name, changed, response)
				}
			}
		}
	}
	for _, name := range []string{"name", "agent_type"} {
		if len(agentTestStringAttribute(t, s, name).PlanModifiers) != 0 {
			t.Errorf("%s should update in place", name)
		}
	}
	for name, tests := range map[string]map[string]bool{
		"agent_id":    {"a": true, "a-1": true, strings.Repeat("a", 63): true, "": false, "A": false, "1a": false, "a-": false, "a_b": false, strings.Repeat("a", 64): false},
		"name":        {"A": true, "A name": true, strings.Repeat("a", 255): true, "": false, " leading": false, "trailing ": false, "\nname": false, "\u00a0name": false, "name\u2003": false, "\vname": false, "名字": true, strings.Repeat("a", 256): false},
		"source_type": {"LSD": true, "MDA": true, "EXTERNAL": true, "FLEET": true, "external": false, "": false},
		"agent_type":  {"CUSTOM": true, "CODING": true, "custom": false, "": false},
	} {
		for value, valid := range tests {
			t.Run(name+"/"+value, func(t *testing.T) {
				var diagnostics diag.Diagnostics
				for _, v := range agentTestStringAttribute(t, s, name).Validators {
					response := validator.StringResponse{}
					v.ValidateString(ctx, validator.StringRequest{Path: path.Root(name), ConfigValue: types.StringValue(value)}, &response)
					diagnostics.Append(response.Diagnostics...)
				}
				if diagnostics.HasError() == valid {
					t.Fatalf("valid=%v diagnostics=%v", valid, diagnostics)
				}
			})
		}
	}
}

func TestAgentResourceMappingAndOptionalName(t *testing.T) {
	ctx := context.Background()
	for _, name := range []types.String{types.StringNull(), types.StringUnknown()} {
		t.Run(name.String(), func(t *testing.T) {
			r := newAgentTestResource(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != "POST" || req.URL.RequestURI() != "/api/v1/agents" {
					t.Errorf("request = %s %s", req.Method, req.URL.RequestURI())
				}
				assertAgentTestPayload(t, req, map[string]any{"id": "my-agent", "source_type": "EXTERNAL", "agent_type": "CUSTOM"})
				writeJSON(t, w, agentAPI{ID: "my-agent", Name: "my-agent", SourceType: "EXTERNAL", AgentType: "CUSTOM"})
			})
			input := agentTestInput(types.StringNull())
			input.Name = name
			response := resource.CreateResponse{State: tfsdk.State{Schema: agentTestSchema(t)}}
			r.Create(ctx, resource.CreateRequest{Plan: agentTestPlan(t, input)}, &response)
			if response.Diagnostics.HasError() {
				t.Fatal(response.Diagnostics)
			}
			got := agentTestModel(t, response.State)
			if got.Name.ValueString() != "my-agent" || !got.CreatedAt.IsNull() || got.Environments.IsNull() || got.Environments.IsUnknown() || len(got.Environments.Elements()) != 0 {
				t.Fatalf("mapped state = %#v", got)
			}
		})
	}
	if _, err := agentModelFromAPI(agentAPI{}, agentResourceModel{}); err == nil || !strings.Contains(err.Error(), "did not return an agent ID") {
		t.Fatalf("empty result error = %v", err)
	}
}

func TestAgentResourceDeleteIdempotent(t *testing.T) {
	ctx := context.Background()
	for _, workspace := range []types.String{types.StringNull(), types.StringValue("override-workspace")} {
		t.Run(workspace.String(), func(t *testing.T) {
			requests := 0
			r := newAgentTestResource(t, func(w http.ResponseWriter, req *http.Request) {
				requests++
				selected := "provider-workspace"
				if !workspace.IsNull() {
					selected = workspace.ValueString()
				}
				if req.Method != "DELETE" || req.URL.RequestURI() != "/api/v1/agents/my-agent" || req.Header.Get("X-Tenant-Id") != selected {
					t.Errorf("delete = %s %s, workspace %q", req.Method, req.URL.RequestURI(), req.Header.Get("X-Tenant-Id"))
				}
				if requests == 1 {
					w.WriteHeader(http.StatusNoContent)
				} else {
					http.NotFound(w, req)
				}
			})
			state := agentTestState(t, agentTestInput(workspace))
			for i := 0; i < 2; i++ {
				response := resource.DeleteResponse{State: state}
				r.Delete(ctx, resource.DeleteRequest{State: state}, &response)
				if response.Diagnostics.HasError() {
					t.Fatal(response.Diagnostics)
				}
			}
			if requests != 2 {
				t.Fatalf("delete requests = %d, want 2", requests)
			}
		})
	}
}

func TestAgentResourceMetadataAndConfigure(t *testing.T) {
	ctx := context.Background()
	r := &AgentResource{}
	var metadata resource.MetadataResponse
	r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "langsmith"}, &metadata)
	if metadata.TypeName != "langsmith_agent" {
		t.Fatalf("type = %q", metadata.TypeName)
	}
	var empty, wrong resource.ConfigureResponse
	r.Configure(ctx, resource.ConfigureRequest{}, &empty)
	if empty.Diagnostics.HasError() {
		t.Fatal(empty.Diagnostics)
	}
	r.Configure(ctx, resource.ConfigureRequest{ProviderData: "wrong"}, &wrong)
	if !wrong.Diagnostics.HasError() || wrong.Diagnostics.Errors()[0].Summary() != "Unexpected Resource Configure Type" {
		t.Fatalf("diagnostics = %v", wrong.Diagnostics)
	}
}
