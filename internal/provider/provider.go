package provider

import (
	"context"
	"fmt"
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
				MarkdownDescription: "LangSmith control-plane API URL, used by `langsmith_deployment` and the deployment revision data sources. Defaults to `LANGSMITH_CONTROL_PLANE_URL`, then to whatever `api_url` implies: `https://api.host.langchain.com` for LangSmith SaaS, or `<api_url origin>/api-host` for a self-hosted install. Set it explicitly when selecting a self-hosted install through `profile`, because the provider cannot read a profile's endpoint.",
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
	apiURL := resolveAPIURL(stringConfig(config.APIURL))
	workspaceID := stringConfig(config.WorkspaceID)
	profileName := stringConfig(config.Profile)
	controlPlaneURL, err := resolveControlPlaneURL(stringConfig(config.ControlPlaneURL), apiURL)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("control_plane_url"), "Invalid Control Plane URL", err.Error())
		return
	}

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

// Normalize explicit endpoints before SDK options override environment defaults.
// An empty URL leaves profile/default resolution to the SDK.
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

// Match the SDK's normalization to avoid doubling /api/v1 on self-hosted URLs.
func normalizeAPIURL(raw string) string {
	u := strings.TrimRight(strings.TrimSpace(raw), "/")
	return strings.TrimSuffix(u, "/api/v1")
}

const (
	defaultControlPlaneURL     = "https://api.host.langchain.com"
	selfHostedControlPlanePath = "/api-host"
)

var saasControlPlanes = map[string]string{
	"api.smith.langchain.com":    defaultControlPlaneURL,
	"eu.api.smith.langchain.com": "https://eu.api.host.langchain.com",
}

// Explicit control-plane settings override derivation from api_url.
// Derive self-hosted URLs locally to avoid sending their credentials to SaaS.
// Profile endpoints are opaque to the provider and need an explicit override.
func resolveControlPlaneURL(configured, apiURL string) (string, error) {
	if raw := strings.TrimSpace(configured); raw != "" {
		return validateControlPlaneURL(raw, "the control_plane_url argument")
	}
	if raw := strings.TrimSpace(os.Getenv("LANGSMITH_CONTROL_PLANE_URL")); raw != "" {
		return validateControlPlaneURL(raw, "LANGSMITH_CONTROL_PLANE_URL")
	}
	return controlPlaneURLForAPIURL(apiURL), nil
}

func controlPlaneURLForAPIURL(apiURL string) string {
	if apiURL == "" {
		return defaultControlPlaneURL
	}
	u, err := url.Parse(apiURL)
	if err != nil || u.Host == "" {
		return defaultControlPlaneURL
	}
	if saas, ok := saasControlPlanes[strings.ToLower(u.Hostname())]; ok {
		return saas
	}
	u.Path = strings.TrimRight(u.Path, "/") + selfHostedControlPlanePath
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// Self-hosted installs may use HTTP when TLS terminates elsewhere.
func validateControlPlaneURL(raw, source string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return "", fmt.Errorf("%q from %s must be an absolute URL, such as https://api.host.langchain.com or https://langsmith.example.com/api-host", raw, source)
	}
	if u.User != nil {
		return "", fmt.Errorf("%q from %s must not include user information", raw, source)
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", fmt.Errorf("%q from %s must not include a query string", raw, source)
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return "", fmt.Errorf("%q from %s must not include a fragment", raw, source)
	}
	if scheme := strings.ToLower(u.Scheme); scheme != "https" && scheme != "http" {
		return "", fmt.Errorf("%q from %s must use HTTPS, or HTTP for an install that does not terminate TLS", raw, source)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func stringConfig(value types.String) string {
	if value.IsNull() || value.IsUnknown() {
		return ""
	}
	return strings.TrimSpace(value.ValueString())
}
