package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

const authServersPath = "v1/agent-auth/oauth-providers"

func authServerResourcePath(id string) string  { return authServersPath + "/" + id }
func NewAuthServerResource() resource.Resource { return &AuthServerResource{} }

type AuthServerResource struct{ client *langsmith.Client }
type authServerResourceModel struct {
	ID                      types.String `tfsdk:"id"`
	WorkspaceID             types.String `tfsdk:"workspace_id"`
	Slug                    types.String `tfsdk:"slug"`
	DisplayName             types.String `tfsdk:"display_name"`
	ClientID                types.String `tfsdk:"client_id"`
	ClientSecret            types.String `tfsdk:"client_secret"`
	AuthorizationURL        types.String `tfsdk:"authorization_url"`
	TokenURL                types.String `tfsdk:"token_url"`
	RevocationURL           types.String `tfsdk:"revocation_url"`
	TokenEndpointAuthMethod types.String `tfsdk:"token_endpoint_auth_method"`
	GrantType               types.String `tfsdk:"grant_type"`
	AuthorizationParams     types.Map    `tfsdk:"authorization_params"`
	TokenParams             types.Map    `tfsdk:"token_params"`
	TokenRequestHeaders     types.Map    `tfsdk:"token_request_headers"`
	DefaultScopes           types.List   `tfsdk:"default_scopes"`
	AllowedScopes           types.List   `tfsdk:"allowed_scopes"`
	HasClientSecret         types.Bool   `tfsdk:"has_client_secret"`
	ConfigVersion           types.Int64  `tfsdk:"config_version"`
	CreatedAt               types.String `tfsdk:"created_at"`
	UpdatedAt               types.String `tfsdk:"updated_at"`
}
type authServerAPI struct {
	ID                      string            `json:"id"`
	Slug                    string            `json:"slug"`
	DisplayName             string            `json:"display_name"`
	ClientID                string            `json:"client_id"`
	AuthorizationURL        string            `json:"authorization_url"`
	TokenURL                string            `json:"token_url"`
	RevocationURL           string            `json:"revocation_url"`
	TokenEndpointAuthMethod string            `json:"token_endpoint_auth_method"`
	GrantType               string            `json:"grant_type"`
	AuthorizationParams     map[string]string `json:"authorization_params"`
	TokenParams             map[string]string `json:"token_params"`
	DefaultScopes           []string          `json:"default_scopes"`
	AllowedScopes           []string          `json:"allowed_scopes"`
	HasClientSecret         bool              `json:"has_client_secret"`
	ConfigVersion           int64             `json:"config_version"`
	CreatedAt               string            `json:"created_at"`
	UpdatedAt               string            `json:"updated_at"`
}

func (r *AuthServerResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_auth_server"
}
func (r *AuthServerResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manages a workspace-scoped OAuth auth server. Import with `<id>` or `<workspace_id>/<id>`. Client secrets and token request headers are never returned by the API and cannot be recovered on import. They remain in sensitive Terraform state; use encrypted remote state. Deleting an auth server also deletes its connections.", Attributes: map[string]schema.Attribute{
		"id":                         schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Id."},
		"workspace_id":               schema.StringAttribute{Optional: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, MarkdownDescription: "Owning workspace ID. Defaults to the provider workspace; changing it replaces the resource."},
		"slug":                       schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`), "Use lowercase letters, digits, and hyphens (1-100 characters).")}, MarkdownDescription: "Unique workspace slug. Changing it replaces the resource."},
		"display_name":               schema.StringAttribute{Required: true, MarkdownDescription: "Display name."},
		"client_id":                  schema.StringAttribute{Required: true, MarkdownDescription: "Client id."},
		"client_secret":              schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Write-only OAuth client secret. Removing it clears the stored secret."},
		"authorization_url":          schema.StringAttribute{Optional: true, MarkdownDescription: "Authorization url."},
		"token_url":                  schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Token URL. Required except for GitHub Apps, where it defaults to https://api.github.com."},
		"revocation_url":             schema.StringAttribute{Optional: true, MarkdownDescription: "Revocation url."},
		"token_endpoint_auth_method": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.OneOf("none", "client_secret_basic", "client_secret_post", "github_app_jwt")}, MarkdownDescription: "Token endpoint auth method."},
		"grant_type":                 schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("authorization_code"), Validators: []validator.String{stringvalidator.OneOf("authorization_code", "client_credentials", "github_app")}, MarkdownDescription: "Grant type."},
		"authorization_params":       schema.MapAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Authorization params."},
		"token_params":               schema.MapAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Token params."},
		"token_request_headers":      schema.MapAttribute{Optional: true, ElementType: types.StringType, Sensitive: true, MarkdownDescription: "Write-only token and revocation request headers. Removing them clears stored headers."},
		"default_scopes":             schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType, MarkdownDescription: "Default scopes. GitHub Apps default to allowed scopes; use an explicit empty list to disable default scopes."},
		"allowed_scopes":             schema.ListAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Allowed scopes."},
		"has_client_secret":          schema.BoolAttribute{Computed: true, MarkdownDescription: "Has client secret."},
		"config_version":             schema.Int64Attribute{Computed: true, MarkdownDescription: "Config version."},
		"created_at":                 schema.StringAttribute{Computed: true, MarkdownDescription: "Created at."},
		"updated_at":                 schema.StringAttribute{Computed: true, MarkdownDescription: "Updated at."},
	}}
}
func (r *AuthServerResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *AuthServerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan authServerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	model, err := r.createAuthServer(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Auth Server", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *AuthServerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state authServerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	model, err := r.readAuthServer(ctx, state.ID.ValueString(), state, state.WorkspaceID)
	if err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Auth Server", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *AuthServerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan authServerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state authServerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	currentID := firstNonEmpty(state.ID.ValueString(), plan.ID.ValueString())
	if currentID == "" {
		resp.Diagnostics.AddError("Unable to Update LangSmith Auth Server", "Missing auth server ID in plan and state.")
		return
	}

	model, err := r.updateAuthServer(ctx, currentID, plan, plan.WorkspaceID, state)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Auth Server", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *AuthServerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state authServerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.deleteAuthServer(ctx, state.ID.ValueString(), state.WorkspaceID); err != nil {
		resp.Diagnostics.AddError("Unable to Delete LangSmith Auth Server", err.Error())
		return
	}
}

func (r *AuthServerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) == 2 {
		if parts[0] == "" || parts[1] == "" {
			resp.Diagnostics.AddError("Invalid Auth Server Import ID", "Use <id> or <workspace_id>/<id>.")
			return
		}
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("workspace_id"), parts[0])...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
		return
	}
	if parts[0] == "" {
		resp.Diagnostics.AddError("Invalid Auth Server Import ID", "Use <id> or <workspace_id>/<id>.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0])...)
}

func (r *AuthServerResource) createAuthServer(ctx context.Context, plan authServerResourceModel) (authServerResourceModel, error) {
	var result authServerAPI
	if err := r.client.Post(ctx, authServersPath, authServerPayloadFromModel(ctx, plan, false), &result, workspaceOpts(plan.WorkspaceID)...); err != nil {
		return authServerResourceModel{}, err
	}
	if result.ID == "" {
		return authServerResourceModel{}, errors.New("LangSmith did not return an auth server ID")
	}
	return authServerModelFromAPI(result, plan), nil
}

func (r *AuthServerResource) readAuthServer(ctx context.Context, id string, previous authServerResourceModel, workspaceID ...types.String) (authServerResourceModel, error) {
	var result authServerAPI
	if err := r.client.Get(ctx, authServerResourcePath(id), nil, &result, workspaceOpts(authServerWorkspaceID(workspaceID))...); err != nil {
		return authServerResourceModel{}, err
	}
	return authServerModelFromAPI(result, previous), nil
}

func (r *AuthServerResource) updateAuthServer(ctx context.Context, currentID string, plan authServerResourceModel, workspaceID types.String, previous authServerResourceModel) (authServerResourceModel, error) {
	var result authServerAPI
	payload := authServerPayloadFromModel(ctx, plan, true)
	if plan.ClientSecret.IsNull() && previous.ClientSecret.IsNull() {
		delete(payload, "client_secret")
	}
	if plan.TokenRequestHeaders.IsNull() && previous.TokenRequestHeaders.IsNull() {
		delete(payload, "token_request_headers")
	}
	if err := r.client.Patch(ctx, authServerResourcePath(currentID), payload, &result, workspaceOpts(workspaceID)...); err != nil {
		return authServerResourceModel{}, err
	}
	return authServerModelFromAPI(result, plan), nil
}

func (r *AuthServerResource) deleteAuthServer(ctx context.Context, id string, workspaceID ...types.String) error {
	if err := r.client.Delete(ctx, authServerResourcePath(id), nil, nil, workspaceOpts(authServerWorkspaceID(workspaceID))...); err != nil {
		if isLangSmithNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

func authServerWorkspaceID(ids []types.String) types.String {
	if len(ids) == 0 {
		return types.StringNull()
	}
	return ids[0]
}
func authServerPayloadFromModel(ctx context.Context, m authServerResourceModel, update bool) map[string]interface{} {
	p := map[string]interface{}{}
	if !update {
		p["slug"] = m.Slug.ValueString()
	}
	p["display_name"] = m.DisplayName.ValueString()
	p["client_id"] = m.ClientID.ValueString()
	if m.ClientSecret.IsNull() {
		if update {
			p["client_secret"] = nil
		}
	} else {
		p["client_secret"] = m.ClientSecret.ValueString()
	}
	if m.AuthorizationURL.IsNull() {
		if update {
			p["authorization_url"] = nil
		}
	} else {
		p["authorization_url"] = m.AuthorizationURL.ValueString()
	}
	if !m.TokenURL.IsNull() && !m.TokenURL.IsUnknown() {
		p["token_url"] = m.TokenURL.ValueString()
	}
	if m.RevocationURL.IsNull() {
		if update {
			p["revocation_url"] = nil
		}
	} else {
		p["revocation_url"] = m.RevocationURL.ValueString()
	}
	p["token_endpoint_auth_method"] = m.TokenEndpointAuthMethod.ValueString()
	p["grant_type"] = m.GrantType.ValueString()
	{
		value := map[string]string{}
		if !m.AuthorizationParams.IsNull() {
			m.AuthorizationParams.ElementsAs(ctx, &value, false)
		}
		p["authorization_params"] = value
	}
	{
		value := map[string]string{}
		if !m.TokenParams.IsNull() {
			m.TokenParams.ElementsAs(ctx, &value, false)
		}
		p["token_params"] = value
	}
	{
		value := map[string]string{}
		if !m.TokenRequestHeaders.IsNull() {
			m.TokenRequestHeaders.ElementsAs(ctx, &value, false)
		}
		p["token_request_headers"] = value
	}
	if !m.DefaultScopes.IsNull() && !m.DefaultScopes.IsUnknown() {
		value := []string{}
		m.DefaultScopes.ElementsAs(ctx, &value, false)
		p["default_scopes"] = value
	}
	{
		value := []string{}
		if !m.AllowedScopes.IsNull() {
			m.AllowedScopes.ElementsAs(ctx, &value, false)
		}
		p["allowed_scopes"] = value
	}
	return p
}
func authServerModelFromAPI(api authServerAPI, previous authServerResourceModel) authServerResourceModel {
	next := previous
	next.ID = types.StringValue(api.ID)
	next.Slug = types.StringValue(api.Slug)
	next.DisplayName = types.StringValue(api.DisplayName)
	next.ClientID = types.StringValue(api.ClientID)
	next.AuthorizationURL = nullableString(api.AuthorizationURL)
	next.TokenURL = types.StringValue(api.TokenURL)
	next.RevocationURL = nullableString(api.RevocationURL)
	next.TokenEndpointAuthMethod = types.StringValue(api.TokenEndpointAuthMethod)
	next.GrantType = types.StringValue(api.GrantType)
	if len(api.AuthorizationParams) == 0 && previous.AuthorizationParams.IsNull() {
		next.AuthorizationParams = types.MapNull(types.StringType)
	} else {
		next.AuthorizationParams, _ = types.MapValueFrom(context.Background(), types.StringType, nonNilAuthServerMap(api.AuthorizationParams))
	}
	if len(api.TokenParams) == 0 && previous.TokenParams.IsNull() {
		next.TokenParams = types.MapNull(types.StringType)
	} else {
		next.TokenParams, _ = types.MapValueFrom(context.Background(), types.StringType, nonNilAuthServerMap(api.TokenParams))
	}
	defaultScopes := api.DefaultScopes
	if defaultScopes == nil {
		defaultScopes = []string{}
	}
	next.DefaultScopes, _ = types.ListValueFrom(context.Background(), types.StringType, defaultScopes)
	if len(api.AllowedScopes) == 0 && previous.AllowedScopes.IsNull() {
		next.AllowedScopes = types.ListNull(types.StringType)
	} else {
		next.AllowedScopes, _ = types.ListValueFrom(context.Background(), types.StringType, api.AllowedScopes)
	}
	next.HasClientSecret = types.BoolValue(api.HasClientSecret)
	next.ConfigVersion = types.Int64Value(api.ConfigVersion)
	next.CreatedAt = types.StringValue(api.CreatedAt)
	next.UpdatedAt = types.StringValue(api.UpdatedAt)
	return next
}

func nonNilAuthServerMap(value map[string]string) map[string]string {
	if value == nil {
		return map[string]string{}
	}
	return value
}
