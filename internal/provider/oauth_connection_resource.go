package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var (
	_ resource.Resource                   = &oauthConnectionResource{}
	_ resource.ResourceWithConfigure      = &oauthConnectionResource{}
	_ resource.ResourceWithImportState    = &oauthConnectionResource{}
	_ resource.ResourceWithValidateConfig = &oauthConnectionResource{}
)

const oauthConnectionsPath = "v1/agent-auth/connections"
const oauthProvidersPath = "v1/agent-auth/oauth-providers"

type oauthConnectionResource struct {
	client *langsmith.Client
}

type oauthConnectionModel struct {
	ID                      types.String `tfsdk:"id"`
	WorkspaceID             types.String `tfsdk:"workspace_id"`
	Slug                    types.String `tfsdk:"slug"`
	DisplayName             types.String `tfsdk:"display_name"`
	OAuthProviderID         types.String `tfsdk:"oauth_provider_id"`
	ClientID                types.String `tfsdk:"client_id"`
	ClientSecretEnv         types.String `tfsdk:"client_secret_env"`
	ClientSecretVersion     types.String `tfsdk:"client_secret_version"`
	AuthorizationURL        types.String `tfsdk:"authorization_url"`
	TokenURL                types.String `tfsdk:"token_url"`
	TokenEndpointAuthMethod types.String `tfsdk:"token_endpoint_auth_method"`
	DefaultScopes           types.Set    `tfsdk:"default_scopes"`
	AllowedScopes           types.Set    `tfsdk:"allowed_scopes"`
}

type oauthConnectionAPI struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Credential  struct {
		Kind            string `json:"kind"`
		OAuthProviderID string `json:"oauth_provider_id"`
	} `json:"credential"`
}

type oauthProviderAPI struct {
	ClientID                string   `json:"client_id"`
	AuthorizationURL        string   `json:"authorization_url"`
	TokenURL                string   `json:"token_url"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantType               string   `json:"grant_type"`
	DefaultScopes           []string `json:"default_scopes"`
	AllowedScopes           []string `json:"allowed_scopes"`
}

func NewOAuthConnectionResource() resource.Resource {
	return &oauthConnectionResource{}
}

func (r *oauthConnectionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_oauth_connection"
}

func (r *oauthConnectionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	required := func(description string) schema.StringAttribute {
		return schema.StringAttribute{Required: true, MarkdownDescription: description, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}, PlanModifiers: replace}
	}
	inPlace := func(description string) schema.StringAttribute {
		attribute := required(description)
		attribute.PlanModifiers = nil
		return attribute
	}
	scopes := func(description string) schema.SetAttribute {
		return schema.SetAttribute{
			Optional: true, Computed: true, ElementType: types.StringType,
			Default:             setdefault.StaticValue(types.SetValueMust(types.StringType, nil)),
			MarkdownDescription: description,
		}
	}
	authMethod := inPlace("OAuth token endpoint authentication method. Updates the owned provider in place.")
	authMethod.Validators = []validator.String{stringvalidator.OneOf("client_secret_basic", "client_secret_post", "none")}
	displayName := inPlace("Connection display name. Updates in place without renaming the OAuth provider.")
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a workspace OAuth2 connection and its owned authorization-code OAuth app. This does not authorize a user or store user tokens. Register the LangSmith callback URL with your OAuth app before use. Provider settings update the owned provider in place: users keep their authorizations, and changes to token settings or the client secret make LangSmith refresh existing tokens. Changing `client_id` also updates in place, but refresh tokens issued to the old client do not work with the new one, so users must authorize again. Changing `slug` or `workspace_id` replaces the connection and deletes its linked credentials; do not use `create_before_destroy` with an unchanged slug.\n\n" +
			"Client secrets are read locally from `client_secret_env` on creation and whenever `client_secret_env` or `client_secret_version` changes, and are never stored in Terraform state. Set `client_secret_version` to a nonsecret revision and change it after updating the environment variable to rotate the secret. Changing an environment variable alone is not detected.\n\n" +
			"Import with `<connection_id>` or `<workspace_id>/<connection_id>`. Public provider configuration is recovered on refresh, but `client_secret_env` and `client_secret_version` cannot be recovered; configuring them after import sends the secret on the next apply. The API does not expose secret values, so secret-value drift cannot be detected.",
		Attributes: map[string]schema.Attribute{
			"id":                         schema.StringAttribute{Computed: true, Description: "Connection ID.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"oauth_provider_id":          schema.StringAttribute{Computed: true, Description: "ID of the OAuth provider owned by this connection.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"workspace_id":               schema.StringAttribute{Optional: true, Description: "Workspace owning the connection. Defaults to the provider workspace. Changes require replacement.", PlanModifiers: replace},
			"slug":                       required("Connection slug, usable as a managed tool server's agent_auth_connection_slug. Changes require replacement."),
			"display_name":               displayName,
			"client_id":                  inPlace("OAuth app client ID. Updates the owned provider in place; users must authorize again, since their refresh tokens belong to the old client."),
			"client_secret_env":          schema.StringAttribute{Optional: true, MarkdownDescription: "Name of the local environment variable containing the OAuth app secret, not the secret itself. Required for confidential client authentication. Changing it sends the named variable's value as the new secret.", Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"client_secret_version":      schema.StringAttribute{Optional: true, MarkdownDescription: "Nonsecret rotation revision. Change this after updating the environment secret to send the new secret in place. The revision, but not the secret, is stored in state.", Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"authorization_url":          inPlace("OAuth authorization endpoint URL. Updates the owned provider in place."),
			"token_url":                  inPlace("OAuth token endpoint URL. Updates the owned provider in place."),
			"token_endpoint_auth_method": authMethod,
			"default_scopes":             scopes("Default OAuth scopes. Defaults to an empty set. Updates in place."),
			"allowed_scopes":             scopes("Allowed OAuth scopes. Defaults to an empty set. Updates in place."),
		},
	}
}

func (r *oauthConnectionResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config oauthConnectionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() || config.TokenEndpointAuthMethod.IsUnknown() || config.TokenEndpointAuthMethod.IsNull() || config.ClientSecretEnv.IsUnknown() {
		return
	}
	if config.TokenEndpointAuthMethod.ValueString() == "none" {
		if !config.ClientSecretEnv.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("client_secret_env"), "Invalid OAuth Secret Reference", errOAuthPublicClientSecret.Error())
		}
		return
	}
	if config.ClientSecretEnv.IsNull() || config.ClientSecretEnv.ValueString() == "" {
		resp.Diagnostics.AddAttributeError(path.Root("client_secret_env"), "Missing OAuth Secret Reference", errOAuthSecretEnv.Error())
	}
}

func (r *oauthConnectionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*langsmith.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", fmt.Sprintf("Expected *langsmith.Client, got %T", req.ProviderData))
		return
	}
	r.client = client
}

func (r *oauthConnectionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan oauthConnectionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.createOAuthConnection(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create OAuth Connection", oauthConnectionError(err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *oauthConnectionResource) createOAuthConnection(ctx context.Context, plan oauthConnectionModel) (oauthConnectionModel, error) {
	provider := map[string]any{
		"client_id":                  plan.ClientID.ValueString(),
		"authorization_url":          plan.AuthorizationURL.ValueString(),
		"token_url":                  plan.TokenURL.ValueString(),
		"token_endpoint_auth_method": plan.TokenEndpointAuthMethod.ValueString(),
		"grant_type":                 "authorization_code",
	}
	if plan.TokenEndpointAuthMethod.ValueString() != "none" {
		secret, ok := os.LookupEnv(plan.ClientSecretEnv.ValueString())
		if plan.ClientSecretEnv.IsNull() || !ok || secret == "" {
			return plan, errOAuthSecretEnv
		}
		provider["client_secret"] = secret
	} else if !plan.ClientSecretEnv.IsNull() {
		return plan, errOAuthPublicClientSecret
	}
	var defaultScopes, allowedScopes []string
	if diags := plan.DefaultScopes.ElementsAs(ctx, &defaultScopes, false); diags.HasError() {
		return plan, errors.New("invalid scopes")
	}
	if diags := plan.AllowedScopes.ElementsAs(ctx, &allowedScopes, false); diags.HasError() {
		return plan, errors.New("invalid scopes")
	}
	provider["default_scopes"] = defaultScopes
	provider["allowed_scopes"] = allowedScopes
	payload := map[string]any{
		"slug": plan.Slug.ValueString(), "display_name": plan.DisplayName.ValueString(),
		"credential": map[string]any{"kind": "oauth2", "provider": provider},
	}
	var result oauthConnectionAPI
	if err := r.client.Post(ctx, oauthConnectionsPath, payload, &result, workspaceOpts(plan.WorkspaceID)...); err != nil {
		return plan, err
	}
	return oauthConnectionFromAPI(result, plan)
}

func (r *oauthConnectionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state oauthConnectionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.readOAuthConnection(ctx, state)
	if err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read OAuth Connection", oauthConnectionError(err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *oauthConnectionResource) readOAuthConnection(ctx context.Context, state oauthConnectionModel) (oauthConnectionModel, error) {
	var result oauthConnectionAPI
	if err := r.client.Get(ctx, oauthConnectionPath(state.ID.ValueString()), nil, &result, workspaceOpts(state.WorkspaceID)...); err != nil {
		return state, err
	}
	model, err := oauthConnectionFromAPI(result, state)
	if err != nil {
		return state, err
	}
	var provider oauthProviderAPI
	if err := r.client.Get(ctx, oauthProvidersPath+"/"+url.PathEscape(model.OAuthProviderID.ValueString()), nil, &provider, workspaceOpts(state.WorkspaceID)...); err != nil {
		if isLangSmithNotFound(err) {
			return state, errOAuthProviderMissing
		}
		return state, err
	}
	if provider.GrantType != "authorization_code" {
		return state, errOAuthGrantType
	}
	model.ClientID = types.StringValue(provider.ClientID)
	model.AuthorizationURL = types.StringValue(provider.AuthorizationURL)
	model.TokenURL = types.StringValue(provider.TokenURL)
	model.TokenEndpointAuthMethod = types.StringValue(provider.TokenEndpointAuthMethod)
	model.DefaultScopes = oauthScopeSet(provider.DefaultScopes)
	model.AllowedScopes = oauthScopeSet(provider.AllowedScopes)
	return model, nil
}

func (r *oauthConnectionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan oauthConnectionModel
	var state oauthConnectionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	plan.OAuthProviderID = state.OAuthProviderID
	model, err := r.updateOAuthConnection(ctx, plan, state)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update OAuth Connection", oauthConnectionError(err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

// updateOAuthConnection patches the owned provider and then the connection's
// display name. LangSmith keeps users' authorizations across provider changes
// and only expires their access tokens when token settings or the secret change.
func (r *oauthConnectionResource) updateOAuthConnection(ctx context.Context, plan, state oauthConnectionModel) (oauthConnectionModel, error) {
	provider, err := oauthProviderPatch(ctx, plan, state)
	if err != nil {
		return plan, err
	}
	providerPath := oauthProvidersPath + "/" + url.PathEscape(plan.OAuthProviderID.ValueString())
	if err := r.client.Patch(ctx, providerPath, provider, nil, workspaceOpts(plan.WorkspaceID)...); err != nil {
		if isLangSmithNotFound(err) {
			return plan, errOAuthProviderMissing
		}
		return plan, err
	}
	if plan.DisplayName.Equal(state.DisplayName) {
		return plan, nil
	}
	var result oauthConnectionAPI
	payload := map[string]string{"display_name": plan.DisplayName.ValueString()}
	if err := r.client.Patch(ctx, oauthConnectionPath(plan.ID.ValueString()), payload, &result, workspaceOpts(plan.WorkspaceID)...); err != nil {
		return plan, err
	}
	return oauthConnectionFromAPI(result, plan)
}

// oauthProviderPatch builds the provider update. Unchanged public settings are
// resent; LangSmith ignores values that match. The secret is read from the
// environment only when the reference or revision changes or the client
// becomes confidential, and is cleared when the client becomes public.
func oauthProviderPatch(ctx context.Context, plan, state oauthConnectionModel) (map[string]any, error) {
	var defaultScopes, allowedScopes []string
	if diags := plan.DefaultScopes.ElementsAs(ctx, &defaultScopes, false); diags.HasError() {
		return nil, errors.New("invalid scopes")
	}
	if diags := plan.AllowedScopes.ElementsAs(ctx, &allowedScopes, false); diags.HasError() {
		return nil, errors.New("invalid scopes")
	}
	if defaultScopes == nil {
		defaultScopes = []string{}
	}
	if allowedScopes == nil {
		allowedScopes = []string{}
	}
	provider := map[string]any{
		"client_id":                  plan.ClientID.ValueString(),
		"authorization_url":          plan.AuthorizationURL.ValueString(),
		"token_url":                  plan.TokenURL.ValueString(),
		"token_endpoint_auth_method": plan.TokenEndpointAuthMethod.ValueString(),
		"default_scopes":             defaultScopes,
		"allowed_scopes":             allowedScopes,
	}
	public := plan.TokenEndpointAuthMethod.ValueString() == "none"
	switch {
	case public && !plan.ClientSecretEnv.IsNull():
		return nil, errOAuthPublicClientSecret
	case public && state.TokenEndpointAuthMethod.ValueString() != "none":
		provider["client_secret"] = nil
	case !public && (state.TokenEndpointAuthMethod.ValueString() == "none" || !plan.ClientSecretEnv.Equal(state.ClientSecretEnv) || !plan.ClientSecretVersion.Equal(state.ClientSecretVersion)):
		secret, ok := os.LookupEnv(plan.ClientSecretEnv.ValueString())
		if plan.ClientSecretEnv.IsNull() || !ok || secret == "" {
			return nil, errOAuthSecretEnv
		}
		provider["client_secret"] = secret
	}
	return provider, nil
}

func (r *oauthConnectionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state oauthConnectionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.deleteOAuthConnection(ctx, state); err != nil {
		resp.Diagnostics.AddError("Unable to Delete OAuth Connection", oauthConnectionError(err))
	}
}

func (r *oauthConnectionResource) deleteOAuthConnection(ctx context.Context, state oauthConnectionModel) error {
	err := r.client.Delete(ctx, oauthConnectionPath(state.ID.ValueString()), nil, nil, workspaceOpts(state.WorkspaceID)...)
	if isLangSmithNotFound(err) {
		return nil
	}
	return err
}

func (r *oauthConnectionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") {
		resp.Diagnostics.AddError("Invalid OAuth Connection Import ID", "Use <connection_id> or <workspace_id>/<connection_id>.")
		return
	}
	if len(parts) == 2 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("workspace_id"), parts[0])...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[len(parts)-1])...)
}

func oauthScopeSet(scopes []string) types.Set {
	values := make([]attr.Value, len(scopes))
	for i, scope := range scopes {
		values[i] = types.StringValue(scope)
	}
	return types.SetValueMust(types.StringType, values)
}

func oauthConnectionPath(id string) string {
	return oauthConnectionsPath + "/" + url.PathEscape(id)
}

func oauthConnectionFromAPI(result oauthConnectionAPI, previous oauthConnectionModel) (oauthConnectionModel, error) {
	if result.ID == "" || result.Slug == "" || result.Credential.Kind != "oauth2" || result.Credential.OAuthProviderID == "" {
		return previous, errors.New("invalid OAuth connection response")
	}
	previous.ID = types.StringValue(result.ID)
	previous.Slug = types.StringValue(result.Slug)
	previous.DisplayName = types.StringValue(result.DisplayName)
	previous.OAuthProviderID = types.StringValue(result.Credential.OAuthProviderID)
	return previous, nil
}

var errOAuthSecretEnv = errors.New("client_secret_env must name a nonempty environment variable for confidential client authentication")
var errOAuthPublicClientSecret = errors.New("client_secret_env must be omitted when token_endpoint_auth_method is none")
var errOAuthGrantType = errors.New("only authorization_code OAuth providers are supported by this resource")
var errOAuthProviderMissing = errors.New("the connection's OAuth provider was not found; verify access or explicitly replace the connection")

func oauthConnectionError(err error) string {
	for _, safe := range []error{errOAuthSecretEnv, errOAuthPublicClientSecret, errOAuthGrantType, errOAuthProviderMissing} {
		if errors.Is(err, safe) {
			return safe.Error()
		}
	}
	var apiErr *langsmith.Error
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("LangSmith OAuth request failed (HTTP %d). Response details are suppressed to protect credentials.", apiErr.StatusCode)
	}
	return "LangSmith OAuth request failed. Details are suppressed to protect credentials; verify the configuration and connectivity."
}
