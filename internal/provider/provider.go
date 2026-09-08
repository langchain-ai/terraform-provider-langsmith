package provider

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
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
	APIKey          types.String `tfsdk:"api_key"`
	APIURL          types.String `tfsdk:"api_url"`
	ControlPlaneURL types.String `tfsdk:"control_plane_url"`
	WorkspaceID     types.String `tfsdk:"workspace_id"`
	Profile         types.String `tfsdk:"profile"`
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
			"control_plane_url": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "LangSmith control-plane API URL. Defaults to `LANGSMITH_CONTROL_PLANE_URL`, then `https://api.host.langchain.com`.",
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
	if config.ControlPlaneURL.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("control_plane_url"), "Unknown Control Plane URL", "The provider cannot configure the control-plane client with an unknown URL.")
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
	controlPlaneURL, err := resolveControlPlaneURL(stringConfig(config.ControlPlaneURL))
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("control_plane_url"), "Invalid Control Plane URL", err.Error())
		return
	}

	var opts []option.RequestOption
	if profileName != "" {
		opts = append(opts, langsmith.WithProfile(profileName))
	}
	if endpoint := resolveAPIURL(apiURL); endpoint != "" {
		opts = append(opts, option.WithBaseURL(endpoint))
	}
	if workspaceID != "" {
		opts = append(opts, option.WithTenantID(workspaceID))
	}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}

	controlPlaneOpts := append([]option.RequestOption{}, opts...)
	controlPlaneOpts = append(controlPlaneOpts, option.WithBaseURL(controlPlaneURL))
	data := &providerData{
		LangSmithClient:    langsmith.NewClient(opts...),
		ControlPlaneClient: langsmith.NewClient(controlPlaneOpts...),
	}
	resp.DataSourceData = data
	resp.ResourceData = data
}

func (p *LangSmithProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewInfoDataSource,
		NewProjectDataSource,
		NewOrgDataSource,
		NewWorkspaceDataSource,
		NewWorkspaceSecretsDataSource,
		NewOrgRoleDataSource,
		NewWorkspaceRoleDataSource,
		NewPermissionsDataSource,
		NewDeploymentRevisionDataSource,
		NewDeploymentRevisionsDataSource,
	}
}

func (p *LangSmithProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewAlertRuleResource,
		NewAccessPolicyResource,
		NewAccessPolicyAttachmentResource,
		NewEvaluatorResource,
		NewRunRuleResource,
		NewTagResource,
		NewTagKeyResource,
		NewTagValueResource,
		NewTaggingResource,
		NewOrgMembershipResource,
		NewSandboxRegistryResource,
		NewServiceKeyResource,
		NewWorkspaceMembershipResource,
		NewWorkspaceRoleResource,
		NewWorkspaceSecretResource,
		NewWorkspaceResource,
		NewGatewayPolicyResource,
		NewModelConfigurationResource,
		NewDeploymentResource,
	}
}

// resolveAPIURL returns the normalized base URL to configure the SDK client
// with, or "" to leave the SDK's own resolution (profile, then default) alone.
//
// LANGSMITH_ENDPOINT is read here rather than left to the SDK because the SDK
// passes it through unnormalized, and provider options are applied after the
// SDK's env defaults — so normalizing it here is what makes it take effect.
func resolveAPIURL(configured string) string {
	raw := configured
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("LANGSMITH_ENDPOINT"))
	}
	if raw == "" {
		return ""
	}
	return normalizeAPIURL(raw)
}

// normalizeAPIURL strips a trailing "/api/v1" so the SDK's relative request
// paths resolve against the origin. Self-hosted installs are documented with
// an endpoint of https://<host>/api/v1, which would otherwise double the
// prefix into https://<host>/api/v1/api/v1/....
//
// Kept identical to normalizeConfigURL in langsmith-go so the CLI, the SDK and
// this provider all agree on what a normalized endpoint looks like.
func normalizeAPIURL(raw string) string {
	u := strings.TrimRight(strings.TrimSpace(raw), "/")
	return strings.TrimSuffix(u, "/api/v1")
}

const defaultControlPlaneURL = "https://api.host.langchain.com"

func resolveControlPlaneURL(configured string) (string, error) {
	raw := configured
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("LANGSMITH_CONTROL_PLANE_URL"))
	}
	if raw == "" {
		raw = defaultControlPlaneURL
	}

	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !u.IsAbs() || u.Host == "" {
		return "", fmt.Errorf("must be an absolute URL")
	}
	if u.User != nil {
		return "", fmt.Errorf("must not include user information")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", fmt.Errorf("must not include a query string")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return "", fmt.Errorf("must not include a fragment")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(u.Hostname())) {
		return "", fmt.Errorf("must use HTTPS, or HTTP only for a loopback host")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func stringConfig(value types.String) string {
	if value.IsNull() || value.IsUnknown() {
		return ""
	}
	return strings.TrimSpace(value.ValueString())
}
