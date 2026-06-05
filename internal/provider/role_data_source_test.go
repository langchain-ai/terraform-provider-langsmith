package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
)

func TestRoleDataSourceMetadataNames(t *testing.T) {
	tests := []struct {
		name     string
		source   datasource.DataSource
		expected string
	}{
		{name: "organization", source: NewOrgRoleDataSource(), expected: "langsmith_org_role"},
		{name: "workspace", source: NewWorkspaceRoleDataSource(), expected: "langsmith_workspace_role"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp datasource.MetadataResponse
			tt.source.Metadata(context.Background(), datasource.MetadataRequest{
				ProviderTypeName: "langsmith",
			}, &resp)

			if resp.TypeName != tt.expected {
				t.Fatalf("TypeName = %q, want %q", resp.TypeName, tt.expected)
			}
		})
	}
}

func TestScopedRoleDataSourceSchemaHidesAccessScope(t *testing.T) {
	source := NewWorkspaceRoleDataSource()
	var resp datasource.SchemaResponse
	source.Schema(context.Background(), datasource.SchemaRequest{}, &resp)

	if _, ok := resp.Schema.Attributes["access_scope"]; ok {
		t.Fatalf("access_scope should be implied by the scoped role data source")
	}
}
