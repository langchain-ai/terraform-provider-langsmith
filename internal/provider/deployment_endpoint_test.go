package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	frameworkprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestDeploymentUsesMainAPIEndpoint(t *testing.T) {
	for _, endpointSource := range []string{"argument", "environment"} {
		t.Run(endpointSource, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet {
					t.Errorf("unexpected request method: %s", req.Method)
				}
				if req.Header.Get("X-Api-Key") != "endpoint-test-key" || req.Header.Get("X-Tenant-Id") != "endpoint-test-workspace" {
					t.Error("deployment request did not use the configured authentication and workspace")
				}
				w.Header().Set("Content-Type", "application/json")
				switch req.URL.Path {
				case "/langsmith/v2/deployments/" + testDeploymentID:
					_, _ = w.Write([]byte(deploymentResponseJSON("READY")))
				case "/langsmith/v2/deployments/" + testDeploymentID + "/revisions/" + testRevisionID:
					_, _ = fmt.Fprintf(w, `{"id":%q,"status":"DEPLOYED"}`, testRevisionID)
				case "/langsmith/v2/deployments/" + testDeploymentID + "/revisions":
					_, _ = fmt.Fprintf(w, `{"resources":[{"id":%q,"status":"DEPLOYED"}],"offset":1}`, testRevisionID)
				default:
					t.Errorf("deployment request used wrong endpoint: %s", req.URL.Path)
					http.NotFound(w, req)
				}
			}))
			defer server.Close()
			// A legacy override must not route requests away from the main API.
			t.Setenv("LANGSMITH_CONTROL_PLANE_URL", server.URL+"/legacy-control")
			t.Setenv("LANGSMITH_ENDPOINT", server.URL+"/langsmith/api/v1")
			ctx := context.Background()
			p := &LangSmithProvider{}
			var schemaResponse frameworkprovider.SchemaResponse
			p.Schema(ctx, frameworkprovider.SchemaRequest{}, &schemaResponse)
			values := map[string]tftypes.Value{}
			for name := range schemaResponse.Schema.Attributes {
				values[name] = tftypes.NewValue(tftypes.String, nil)
			}
			values["api_key"] = tftypes.NewValue(tftypes.String, "endpoint-test-key")
			values["workspace_id"] = tftypes.NewValue(tftypes.String, "endpoint-test-workspace")
			if endpointSource == "argument" {
				values["api_url"] = tftypes.NewValue(tftypes.String, server.URL+"/langsmith/api/v1")
				t.Setenv("LANGSMITH_ENDPOINT", server.URL+"/wrong-environment")
			}
			var response frameworkprovider.ConfigureResponse
			p.Configure(ctx, frameworkprovider.ConfigureRequest{Config: tfsdk.Config{
				Schema: schemaResponse.Schema,
				Raw:    tftypes.NewValue(schemaResponse.Schema.Type().TerraformType(ctx), values),
			}}, &response)
			if response.Diagnostics.HasError() {
				t.Fatalf("provider configuration diagnostics: %v", response.Diagnostics)
			}

			r := &DeploymentResource{}
			var resourceResponse resource.ConfigureResponse
			r.Configure(ctx, resource.ConfigureRequest{ProviderData: response.ResourceData}, &resourceResponse)
			if resourceResponse.Diagnostics.HasError() {
				t.Fatalf("resource configuration diagnostics: %v", resourceResponse.Diagnostics)
			}
			if model, err := r.read(ctx, testDeploymentID, testDeploymentModel()); err != nil || model.ID.ValueString() != testDeploymentID {
				t.Errorf("read deployment: ID = %s, error = %v", model.ID, err)
			}

			revision := &DeploymentRevisionDataSource{}
			revisions := &DeploymentRevisionsDataSource{}
			for _, dataSource := range []datasource.DataSourceWithConfigure{revision, revisions} {
				var dataResponse datasource.ConfigureResponse
				dataSource.Configure(ctx, datasource.ConfigureRequest{ProviderData: response.DataSourceData}, &dataResponse)
				if dataResponse.Diagnostics.HasError() {
					t.Fatalf("data source configuration diagnostics: %v", dataResponse.Diagnostics)
				}
			}
			if model, err := revision.readRevision(ctx, testDeploymentID, testRevisionID); err != nil || model.ID != testRevisionID {
				t.Errorf("read revision: ID = %s, error = %v", model.ID, err)
			}
			if result, err := revisions.listRevisions(ctx, deploymentRevisionsDataSourceModel{DeploymentID: types.StringValue(testDeploymentID)}); err != nil || len(result.Resources) != 1 {
				t.Errorf("list revisions: count = %d, error = %v", len(result.Resources), err)
			}
		})
	}
}
