package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	frameworkprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

var _ frameworkprovider.Provider = &LangSmithProvider{}

func New(version string) func() frameworkprovider.Provider {
	return func() frameworkprovider.Provider {
		return &LangSmithProvider{version: version}
	}
}

type LangSmithProvider struct {
	version string
}

type providerModel struct {
	APIKey      types.String `tfsdk:"api_key"`
	APIURL      types.String `tfsdk:"api_url"`
	WorkspaceID types.String `tfsdk:"workspace_id"`
	Profile     types.String `tfsdk:"profile"`
}

func (p *LangSmithProvider) Metadata(ctx context.Context, req frameworkprovider.MetadataRequest, resp *frameworkprovider.MetadataResponse) {
	resp.TypeName = "langsmith"
	resp.Version = p.version
}

func (p *LangSmithProvider) Schema(ctx context.Context, req frameworkprovider.SchemaRequest, resp *frameworkprovider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Terraform provider for managing LangSmith resources.",
		Attributes: map[string]schema.Attribute{
			"api_key": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "LangSmith API key. Prefer SDK environment/profile configuration.",
			},
			"api_url": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "LangSmith API URL. Prefer SDK environment/profile configuration.",
			},
			"workspace_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "LangSmith workspace ID. Prefer SDK environment/profile configuration.",
			},
			"profile": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "LangSmith profile name. Prefer `LANGSMITH_PROFILE` unless this Terraform root must select one explicitly.",
			},
		},
	}
}

func (p *LangSmithProvider) Configure(ctx context.Context, req frameworkprovider.ConfigureRequest, resp *frameworkprovider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.APIKey.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Unknown API Key", "The provider cannot configure the LangSmith client with an unknown API key.")
	}
	if config.APIURL.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("api_url"), "Unknown API URL", "The provider cannot configure the LangSmith client with an unknown API URL.")
	}
	if config.WorkspaceID.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("workspace_id"), "Unknown Workspace ID", "The provider cannot configure the LangSmith client with an unknown workspace ID.")
	}
	if config.Profile.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("profile"), "Unknown Profile", "The provider cannot configure the LangSmith client with an unknown CLI profile.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	apiKey := stringConfig(config.APIKey)
	apiURL := stringConfig(config.APIURL)
	workspaceID := stringConfig(config.WorkspaceID)
	profileName := stringConfig(config.Profile)

	var opts []option.RequestOption
	if profileName != "" {
		opts = append(opts, langsmith.WithProfile(profileName))
	}
	if apiURL != "" {
		opts = append(opts, option.WithBaseURL(apiURL))
	}
	if workspaceID != "" {
		opts = append(opts, option.WithTenantID(workspaceID))
	}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}

	client := langsmith.NewClient(opts...)
	resp.DataSourceData = client
	resp.ResourceData = client
}

func (p *LangSmithProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewInfoDataSource,
		NewProjectDataSource,
		NewOrgDataSource,
		NewWorkspaceDataSource,
		NewOrgRoleDataSource,
		NewWorkspaceRoleDataSource,
		NewPermissionsDataSource,
	}
}

func (p *LangSmithProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewAlertRuleResource,
		NewEvaluatorResource,
		NewRunRuleResource,
		NewOrgMembershipResource,
		NewServiceKeyResource,
		NewWorkspaceMembershipResource,
		NewWorkspaceRoleResource,
		NewWorkspaceResource,
	}
}

func stringConfig(value types.String) string {
	if value.IsNull() || value.IsUnknown() {
		return ""
	}
	return strings.TrimSpace(value.ValueString())
}
