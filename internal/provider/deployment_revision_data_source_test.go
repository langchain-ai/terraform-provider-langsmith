package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestDeploymentRevisionDataSourceMetadata(t *testing.T) {
	var response datasource.MetadataResponse
	(&DeploymentRevisionDataSource{}).Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "langsmith"}, &response)
	if response.TypeName != "langsmith_deployment_revision" {
		t.Fatalf("type name = %q", response.TypeName)
	}
}

func TestDeploymentRevisionDataSourcePathAndMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/control/v2/deployments/deployment-id/revisions/revision-id" {
			t.Fatalf("request = %s %s", r.Method, r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"revision-id","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:01:00Z","status":"DEPLOYED","source":"github","source_revision_config":{"repo_ref":"main","langgraph_config_path":"langgraph.json","image_uri":"image:v1","source_tarball_path":"source.tgz","repo_commit_sha":"abc123","deepagents_version":"1.2.3","tracked_packages":["agent","shared"]}}`))
	}))
	defer server.Close()

	source := &DeploymentRevisionDataSource{client: langsmith.NewClient(option.WithBaseURL(server.URL+"/control"), option.WithAPIKey("test-key"))}
	revision, err := source.readRevision(context.Background(), "deployment-id", "revision-id")
	if err != nil {
		t.Fatal(err)
	}
	model, diagnostics := deploymentRevisionModelFromAPI(context.Background(), revision)
	if diagnostics.HasError() {
		t.Fatalf("mapping diagnostics = %v", diagnostics)
	}
	if model.ID != types.StringValue("revision-id") || model.CreatedAt != types.StringValue("2025-01-01T00:00:00Z") || model.UpdatedAt != types.StringValue("2025-01-01T00:01:00Z") || model.Status != types.StringValue("DEPLOYED") || model.Source != types.StringValue("github") {
		t.Fatalf("revision model = %#v", model)
	}
	config := model.SourceRevisionConfig
	if config.RepoRef != types.StringValue("main") || config.LangGraphConfigPath != types.StringValue("langgraph.json") || config.ImageURI != types.StringValue("image:v1") || config.SourceTarballPath != types.StringValue("source.tgz") || config.RepoCommitSHA != types.StringValue("abc123") || config.DeepAgentsVersion != types.StringValue("1.2.3") {
		t.Fatalf("source revision config = %#v", config)
	}
	var packages []string
	if diagnostics := config.TrackedPackages.ElementsAs(context.Background(), &packages, false); diagnostics.HasError() {
		t.Fatalf("tracked package diagnostics = %v", diagnostics)
	}
	if !reflect.DeepEqual(packages, []string{"agent", "shared"}) {
		t.Fatalf("tracked packages = %#v", packages)
	}
}
