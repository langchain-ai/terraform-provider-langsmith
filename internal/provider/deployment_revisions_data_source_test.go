package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestDeploymentRevisionsDataSourceMetadata(t *testing.T) {
	var response datasource.MetadataResponse
	(&DeploymentRevisionsDataSource{}).Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "langsmith"}, &response)
	if response.TypeName != "langsmith_deployment_revisions" {
		t.Fatalf("type name = %q", response.TypeName)
	}
}

func TestDeploymentRevisionsDataSourceQueryAndMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/control/v2/deployments/deployment-id/revisions?limit=25&offset=50&status=DEPLOYED%2CDEPLOY_FAILED" {
			t.Fatalf("request = %s %s", r.Method, r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resources":[{"id":"revision-id","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:01:00Z","status":"DEPLOYED","source":"image","source_revision_config":{"image_uri":"image:v1","tracked_packages":["agent"]}}],"offset":75}`))
	}))
	defer server.Close()

	source := &DeploymentRevisionsDataSource{client: langsmith.NewClient(option.WithBaseURL(server.URL+"/control"), option.WithAPIKey("test-key"))}
	result, err := source.listRevisions(context.Background(), deploymentRevisionsDataSourceModel{
		DeploymentID: types.StringValue("deployment-id"),
		Limit:        types.Int64Value(25),
		Offset:       types.Int64Value(50),
		Status:       types.StringValue("DEPLOYED,DEPLOY_FAILED"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Offset != 75 || len(result.Resources) != 1 {
		t.Fatalf("result = %#v", result)
	}
	model, diagnostics := deploymentRevisionModelFromAPI(context.Background(), result.Resources[0])
	if diagnostics.HasError() {
		t.Fatalf("mapping diagnostics = %v", diagnostics)
	}
	if model.ID != types.StringValue("revision-id") || model.Status != types.StringValue("DEPLOYED") || model.SourceRevisionConfig.ImageURI != types.StringValue("image:v1") || model.SourceRevisionConfig.TrackedPackages.Elements()[0] != types.StringValue("agent") {
		t.Fatalf("revision model = %#v", model)
	}
}
