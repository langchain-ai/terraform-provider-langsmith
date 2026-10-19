package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestAgentEnvironmentDataSourceRead(t *testing.T) {
	const environmentID = "12345678-1234-1234-1234-123456789abc"
	for _, workspace := range []types.String{types.StringNull(), types.StringValue("override-workspace")} {
		for _, test := range []struct {
			name      string
			id        string
			path      string
			status    int
			body      string
			wantError string
		}{
			{name: "found", id: environmentID, path: environmentID, status: http.StatusOK, body: `{"id":"` + environmentID + `","agent_id":"my-agent","environment":"STAGING","tracer_session_id":"project-id","tracer_session_name":"Staging"}`},
			{name: "escaped path", id: "environment/with space", path: "environment%2Fwith%20space", status: http.StatusNotFound, body: `{"detail":"missing"}`, wantError: "404"},
			{name: "not found", id: environmentID, path: environmentID, status: http.StatusNotFound, body: `{"detail":"missing"}`, wantError: "404"},
			{name: "read error", id: environmentID, path: environmentID, status: http.StatusInternalServerError, body: `{"detail":"read failed"}`, wantError: "500"},
			{name: "absent result", id: environmentID, path: environmentID, status: http.StatusOK, body: `{}`, wantError: "did not return an agent environment ID"},
		} {
			t.Run(workspace.String()+"/"+test.name, func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					if r.Method != http.MethodGet || r.URL.RequestURI() != "/api/v1/agent-environments/"+test.path {
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
				source := &AgentEnvironmentDataSource{}
				client := langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("test-key"), option.WithTenantID("provider-workspace"), option.WithMaxRetries(0))
				var configured datasource.ConfigureResponse
				source.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: client}, &configured)
				if configured.Diagnostics.HasError() {
					t.Fatal(configured.Diagnostics)
				}
				response := readAgentDataSourceTest(t, source, agentEnvironmentDataSourceModel{ID: types.StringValue(test.id), WorkspaceID: workspace})
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
				var got agentEnvironmentDataSourceModel
				if diagnostics := response.State.Get(context.Background(), &got); diagnostics.HasError() {
					t.Fatal(diagnostics)
				}
				want := agentEnvironmentDataSourceModel{
					ID: types.StringValue(environmentID), WorkspaceID: workspace, AgentID: types.StringValue("my-agent"), Environment: types.StringValue("STAGING"), TracerSessionID: types.StringValue("project-id"), TracerSessionName: types.StringValue("Staging"),
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("state = %#v, want %#v", got, want)
				}
			})
		}
	}
}

func TestAgentDataSourcesMetadata(t *testing.T) {
	for _, test := range []struct {
		source datasource.DataSource
		name   string
	}{
		{NewAgentDataSource(), "langsmith_agent"},
		{NewAgentEnvironmentDataSource(), "langsmith_agent_environment"},
	} {
		var response datasource.MetadataResponse
		test.source.Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "langsmith"}, &response)
		if response.TypeName != test.name {
			t.Fatalf("name = %q, want %q", response.TypeName, test.name)
		}
	}
}
