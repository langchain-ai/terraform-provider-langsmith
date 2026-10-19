package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func readAgentDataSourceTest(t *testing.T, source datasource.DataSource, model any) datasource.ReadResponse {
	t.Helper()
	ctx := context.Background()
	var schemaResponse datasource.SchemaResponse
	source.Schema(ctx, datasource.SchemaRequest{}, &schemaResponse)
	if schemaResponse.Diagnostics.HasError() {
		t.Fatal(schemaResponse.Diagnostics)
	}
	state := tfsdk.State{Schema: schemaResponse.Schema}
	if diagnostics := state.Set(ctx, model); diagnostics.HasError() {
		t.Fatal(diagnostics)
	}
	response := datasource.ReadResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	source.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResponse.Schema, Raw: state.Raw}}, &response)
	return response
}

func TestAgentDataSourceRead(t *testing.T) {
	for _, workspace := range []types.String{types.StringNull(), types.StringValue("override-workspace")} {
		for _, test := range []struct {
			name      string
			status    int
			body      string
			wantError string
		}{
			{name: "found", status: http.StatusOK, body: `{"id":"my-agent","name":"My Agent","source_type":"EXTERNAL","agent_type":"CUSTOM","created_at":"2026-01-01T00:00:00Z","environments":[{"id":"env-id","environment":"PRODUCTION","tracer_session_id":"project-id","tracer_session_name":"Production"}]}`},
			{name: "not found", status: http.StatusNotFound, body: `{"detail":"agent missing"}`, wantError: "404"},
			{name: "read error", status: http.StatusInternalServerError, body: `{"detail":"read failed"}`, wantError: "500"},
			{name: "absent result", status: http.StatusOK, body: `{}`, wantError: "did not return an agent ID"},
		} {
			t.Run(workspace.String()+"/"+test.name, func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					if r.Method != http.MethodGet || r.URL.RequestURI() != "/api/v1/agents/my-agent" {
						t.Errorf("request = %s %s", r.Method, r.URL.RequestURI())
					}
					wantWorkspace := "provider-workspace"
					if !workspace.IsNull() {
						wantWorkspace = workspace.ValueString()
					}
					if got := r.Header.Get("X-Tenant-Id"); got != wantWorkspace {
						t.Errorf("workspace = %q, want %q", got, wantWorkspace)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(test.status)
					_, _ = w.Write([]byte(test.body))
				}))
				defer server.Close()
				source := &AgentDataSource{}
				client := langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("test-key"), option.WithTenantID("provider-workspace"), option.WithMaxRetries(0))
				var configured datasource.ConfigureResponse
				source.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: client}, &configured)
				if configured.Diagnostics.HasError() {
					t.Fatal(configured.Diagnostics)
				}
				response := readAgentDataSourceTest(t, source, agentResourceModel{AgentID: types.StringValue("my-agent"), WorkspaceID: workspace, Environments: types.MapNull(types.ObjectType{AttrTypes: agentEnvironmentAttributeTypes})})
				if requests != 1 {
					t.Fatalf("requests = %d, want 1", requests)
				}
				if test.wantError != "" {
					if !response.Diagnostics.HasError() || !strings.Contains(response.Diagnostics.Errors()[0].Detail(), test.wantError) {
						t.Fatalf("diagnostics = %v, want %q", response.Diagnostics, test.wantError)
					}
					return
				}
				if response.Diagnostics.HasError() {
					t.Fatal(response.Diagnostics)
				}
				var got agentResourceModel
				if diagnostics := response.State.Get(context.Background(), &got); diagnostics.HasError() {
					t.Fatal(diagnostics)
				}
				environment := types.ObjectValueMust(agentEnvironmentAttributeTypes, map[string]attr.Value{
					"id": types.StringValue("env-id"), "tracer_session_id": types.StringValue("project-id"), "tracer_session_name": types.StringValue("Production"),
				})
				want := agentResourceModel{
					ID: types.StringValue("my-agent"), AgentID: types.StringValue("my-agent"), WorkspaceID: workspace,
					Name: types.StringValue("My Agent"), SourceType: types.StringValue("EXTERNAL"), AgentType: types.StringValue("CUSTOM"), CreatedAt: types.StringValue("2026-01-01T00:00:00Z"),
					Environments: types.MapValueMust(types.ObjectType{AttrTypes: agentEnvironmentAttributeTypes}, map[string]attr.Value{"PRODUCTION": environment}),
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("state = %#v, want %#v", got, want)
				}
			})
		}
	}
}

func TestAgentDataSourcesConfigure(t *testing.T) {
	for _, source := range []datasource.DataSource{NewAgentDataSource(), NewAgentEnvironmentDataSource()} {
		configured, ok := source.(datasource.DataSourceWithConfigure)
		if !ok {
			t.Fatalf("data source %T does not support configuration", source)
		}
		var nilResponse datasource.ConfigureResponse
		configured.Configure(context.Background(), datasource.ConfigureRequest{}, &nilResponse)
		if nilResponse.Diagnostics.HasError() {
			t.Fatal(nilResponse.Diagnostics)
		}
		var wrongResponse datasource.ConfigureResponse
		configured.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "wrong"}, &wrongResponse)
		if !wrongResponse.Diagnostics.HasError() || wrongResponse.Diagnostics.Errors()[0].Summary() != "Unexpected Data Source Configure Type" {
			t.Fatalf("diagnostics = %v", wrongResponse.Diagnostics)
		}
	}
}

func TestAgentDataSourcesUnconfiguredRead(t *testing.T) {
	for _, test := range []struct {
		source datasource.DataSource
		model  any
	}{
		{NewAgentDataSource(), agentResourceModel{AgentID: types.StringValue("my-agent"), Environments: types.MapNull(types.ObjectType{AttrTypes: agentEnvironmentAttributeTypes})}},
		{NewAgentEnvironmentDataSource(), agentEnvironmentDataSourceModel{ID: types.StringValue("environment-id")}},
	} {
		response := readAgentDataSourceTest(t, test.source, test.model)
		if !response.Diagnostics.HasError() || !strings.Contains(response.Diagnostics.Errors()[0].Detail(), "not configured") {
			t.Fatalf("diagnostics = %v", response.Diagnostics)
		}
	}
}
