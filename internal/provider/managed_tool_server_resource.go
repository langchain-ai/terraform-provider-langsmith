package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

const managedToolServersPath = "v1/managed-tools/servers"

var (
	_ resource.Resource                   = &managedToolServerResource{}
	_ resource.ResourceWithConfigure      = &managedToolServerResource{}
	_ resource.ResourceWithImportState    = &managedToolServerResource{}
	_ resource.ResourceWithValidateConfig = &managedToolServerResource{}
)

type managedToolServerResource struct {
	client *langsmith.Client
}

type managedToolServerModel struct {
	ID                      types.String `tfsdk:"id"`
	Name                    types.String `tfsdk:"name"`
	UpstreamURL             types.String `tfsdk:"upstream_url"`
	ConnectionType          types.String `tfsdk:"connection_type"`
	AgentAuthConnectionSlug types.String `tfsdk:"agent_auth_connection_slug"`
	SecretHeaders           types.Set    `tfsdk:"secret_headers"`
	WorkspaceID             types.String `tfsdk:"workspace_id"`
	URL                     types.String `tfsdk:"url"`
	Enabled                 types.Bool   `tfsdk:"enabled"`
	OAuthSetup              types.Object `tfsdk:"oauth_setup"`
}

type managedToolServerAPI struct {
	ID          string                         `json:"id"`
	Name        string                         `json:"name"`
	UpstreamURL string                         `json:"upstream_url"`
	Owner       string                         `json:"owner"`
	Scope       string                         `json:"scope"`
	URL         string                         `json:"url"`
	Enabled     bool                           `json:"enabled"`
	Connection  managedToolServerConnectionAPI `json:"connection"`
}

type managedToolServerConnectionAPI struct {
	Type                    string                          `json:"type"`
	AgentAuthConnectionSlug *string                         `json:"agent_auth_connection_slug,omitempty"`
	Fields                  []managedToolServerSecretField  `json:"fields,omitempty"`
	OAuthSetup              *managedToolServerOAuthSetupAPI `json:"oauth_setup,omitempty"`
}

// managedToolServerSecretField declares one header a caller supplies for a
// secret server. LangSmith requires the field name to equal the header name.
type managedToolServerSecretField struct {
	Name      string                           `json:"name"`
	Injection managedToolServerSecretInjection `json:"injection"`
}

type managedToolServerSecretInjection struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type managedToolServerOAuthSetupAPI struct {
	ProviderSlug            string   `json:"provider_slug"`
	CallbackURL             string   `json:"callback_url"`
	AuthorizationURL        string   `json:"authorization_url"`
	TokenURL                string   `json:"token_url"`
	RevocationURL           *string  `json:"revocation_url"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scopes                  []string `json:"scopes"`
	PKCE                    bool     `json:"pkce"`
	OAuthProviderID         *string  `json:"oauth_provider_id"`
}

var managedToolServerOAuthSetupTypes = map[string]attr.Type{
	"provider_slug":              types.StringType,
	"callback_url":               types.StringType,
	"authorization_url":          types.StringType,
	"token_url":                  types.StringType,
	"revocation_url":             types.StringType,
	"token_endpoint_auth_method": types.StringType,
	"scopes":                     types.SetType{ElemType: types.StringType},
	"pkce":                       types.BoolType,
	"oauth_provider_id":          types.StringType,
}

func NewManagedToolServerResource() resource.Resource {
	return &managedToolServerResource{}
}

func (r *managedToolServerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_managed_tool_server"
}

func (r *managedToolServerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Registers a workspace-scoped MCP server with LangSmith Managed Tools. Requires a user-owned API key with the appropriate workspace permissions; service-account and service keys cannot create or delete servers. Supports public, OAuth, and secret (API key header) servers without storing upstream credentials; store secret values with `langsmith_managed_tool_profile_credential`. All configurable attributes require replacement; the API does not support changing workspace server enablement. OAuth scopes belong to the OAuth connection, not the server.",
		Attributes: map[string]schema.Attribute{
			"id":                         schema.StringAttribute{Computed: true, MarkdownDescription: "Registered server ID.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":                       schema.StringAttribute{Required: true, MarkdownDescription: "Server name, unique in the workspace ignoring case. Letters and digits, separated by single hyphens or underscores; LangSmith rewrites other names, which Terraform would then replace on every apply. Kit tool names are prefixed with this name.", Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^[A-Za-z0-9]+(?:[-_][A-Za-z0-9]+)*$`), "must be letters and digits separated by single hyphens or underscores, the form LangSmith stores")}, PlanModifiers: replace},
			"upstream_url":               schema.StringAttribute{Required: true, MarkdownDescription: "HTTPS Streamable HTTP MCP endpoint. LangSmith validates the upstream server; Terraform never contacts it directly.", Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^https://\S+$`), "must be an HTTPS URL")}, PlanModifiers: replace},
			"connection_type":            schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("none"), MarkdownDescription: "Upstream authentication: `none` (default), `oauth`, or `secret` (caller-supplied headers declared in `secret_headers`).", Validators: []validator.String{stringvalidator.OneOf("none", "oauth", "secret")}, PlanModifiers: replace},
			"agent_auth_connection_slug": schema.StringAttribute{Optional: true, MarkdownDescription: "Existing Agent Auth OAuth connection slug. Only valid with `connection_type = \"oauth\"`. Omit for automatic OAuth discovery and dynamic client registration where supported.", Validators: []validator.String{stringvalidator.LengthBetween(1, 100), stringvalidator.RegexMatches(regexp.MustCompile(`^\S+$`), "must not contain whitespace")}, PlanModifiers: replace},
			"secret_headers":             schema.SetAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Upstream HTTP header names each caller supplies, such as `Authorization`. Required with `connection_type = \"secret\"` and invalid otherwise. Credential values include any prefix, such as `Bearer `.", Validators: []validator.Set{setvalidator.SizeAtLeast(1), setvalidator.ValueStringsAre(stringvalidator.RegexMatches(regexp.MustCompile(`^[!#$%&'*+.^_`+"`"+`|~0-9A-Za-z-]+$`), "must be an HTTP header name"))}, PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()}},
			"workspace_id":               schema.StringAttribute{Optional: true, MarkdownDescription: "Workspace that owns this server. Defaults to the provider workspace. Use `workspace_id/server_id` when importing into a workspace other than the provider default.", Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^\S+$`), "must be nonblank without whitespace")}, PlanModifiers: replace},
			"url":                        schema.StringAttribute{Computed: true, MarkdownDescription: "Managed Tools MCP endpoint returned by LangSmith; may be a relative path."},
			"enabled":                    schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the server is enabled. Workspace servers are always enabled; the API cannot change this setting."},
			"oauth_setup": schema.SingleNestedAttribute{Computed: true, MarkdownDescription: "Non-secret OAuth registration metadata, when returned by LangSmith. Null for public servers and connections without setup metadata. Register an OAuth app manually using the callback URL if dynamic client registration is unavailable.", Attributes: map[string]schema.Attribute{
				"provider_slug":              schema.StringAttribute{Computed: true, MarkdownDescription: "Discovered OAuth provider slug."},
				"callback_url":               schema.StringAttribute{Computed: true, MarkdownDescription: "Redirect URI to register with the upstream OAuth provider."},
				"authorization_url":          schema.StringAttribute{Computed: true, MarkdownDescription: "OAuth authorization endpoint."},
				"token_url":                  schema.StringAttribute{Computed: true, MarkdownDescription: "OAuth token endpoint, not a token."},
				"revocation_url":             schema.StringAttribute{Computed: true, MarkdownDescription: "Optional OAuth revocation endpoint."},
				"token_endpoint_auth_method": schema.StringAttribute{Computed: true, MarkdownDescription: "OAuth client authentication method."},
				"scopes":                     schema.SetAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Discovered OAuth scopes."},
				"pkce":                       schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether PKCE is enabled."},
				"oauth_provider_id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Registered workspace OAuth app ID, when present."},
			}},
		},
	}
}

func (r *managedToolServerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *managedToolServerResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config managedToolServerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !config.AgentAuthConnectionSlug.IsNull() && !config.ConnectionType.IsUnknown() && config.ConnectionType.ValueString() != "oauth" {
		resp.Diagnostics.AddAttributeError(path.Root("agent_auth_connection_slug"), "OAuth connection required", "agent_auth_connection_slug requires connection_type = \"oauth\".")
	}
	if config.ConnectionType.IsUnknown() || config.SecretHeaders.IsUnknown() {
		return
	}
	secret := config.ConnectionType.ValueString() == "secret"
	if secret && config.SecretHeaders.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("secret_headers"), "Secret headers required", "connection_type = \"secret\" requires secret_headers.")
	}
	if !secret && !config.SecretHeaders.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("secret_headers"), "Secret connection required", "secret_headers requires connection_type = \"secret\".")
	}
}

func (r *managedToolServerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan managedToolServerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := struct {
		Name        string                         `json:"name"`
		UpstreamURL string                         `json:"upstream_url"`
		Scope       string                         `json:"scope"`
		Connection  managedToolServerConnectionAPI `json:"connection"`
	}{plan.Name.ValueString(), plan.UpstreamURL.ValueString(), "workspace", managedToolServerConnectionAPI{Type: plan.ConnectionType.ValueString(), AgentAuthConnectionSlug: plan.AgentAuthConnectionSlug.ValueStringPointer()}}
	if !plan.SecretHeaders.IsNull() {
		var headers []string
		resp.Diagnostics.Append(plan.SecretHeaders.ElementsAs(ctx, &headers, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		slices.Sort(headers)
		for _, header := range headers {
			body.Connection.Fields = append(body.Connection.Fields, managedToolServerSecretField{Name: header, Injection: managedToolServerSecretInjection{Type: "header", Name: header}})
		}
	}
	var server managedToolServerAPI
	if err := r.client.Post(ctx, managedToolServersPath, body, &server, workspaceOpts(plan.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Unable to Create Managed Tool Server", err.Error())
		return
	}
	next, err := managedToolServerModelFromAPI(ctx, server, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Created Managed Tool Server", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *managedToolServerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state managedToolServerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	server, found, err := r.findServer(ctx, state)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Managed Tool Server", err.Error())
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	next, err := managedToolServerModelFromAPI(ctx, server, state)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Managed Tool Server", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *managedToolServerResource) findServer(ctx context.Context, state managedToolServerModel) (managedToolServerAPI, bool, error) {
	cursor := ""
	seen := map[string]bool{}
	for {
		opts := append(workspaceOpts(state.WorkspaceID), option.WithQuery("page_size", "100"))
		if cursor != "" {
			opts = append(opts, option.WithQuery("cursor", cursor))
		}
		var page struct {
			Items      []managedToolServerAPI `json:"items"`
			NextCursor *string                `json:"next_cursor"`
		}
		if err := r.client.Get(ctx, managedToolServersPath, nil, &page, opts...); err != nil {
			return managedToolServerAPI{}, false, err
		}
		if page.Items == nil {
			return managedToolServerAPI{}, false, errors.New("LangSmith returned a server page without an items array")
		}
		for _, server := range page.Items {
			if server.ID == state.ID.ValueString() {
				return server, true, nil
			}
		}
		if page.NextCursor == nil {
			return managedToolServerAPI{}, false, nil
		}
		cursor = *page.NextCursor
		if cursor == "" || seen[cursor] {
			return managedToolServerAPI{}, false, errors.New("LangSmith returned an empty or repeated pagination cursor")
		}
		seen[cursor] = true
	}
}

func managedToolServerModelFromAPI(ctx context.Context, server managedToolServerAPI, previous managedToolServerModel) (managedToolServerModel, error) {
	if server.ID == "" || server.Owner != "workspace" || server.Scope != "workspace" {
		return previous, errors.New("the API response is not a registered workspace-scoped managed tool server")
	}
	if server.Connection.Type != "none" && server.Connection.Type != "oauth" && server.Connection.Type != "secret" {
		return previous, fmt.Errorf("unsupported managed tool server connection type %q", server.Connection.Type)
	}
	next := previous
	next.ID = types.StringValue(server.ID)
	next.Name = types.StringValue(server.Name)
	next.UpstreamURL = types.StringValue(server.UpstreamURL)
	next.ConnectionType = types.StringValue(server.Connection.Type)
	next.AgentAuthConnectionSlug = nullableStringPointer(server.Connection.AgentAuthConnectionSlug)
	next.SecretHeaders = types.SetNull(types.StringType)
	if server.Connection.Type == "secret" {
		headers := make([]attr.Value, 0, len(server.Connection.Fields))
		for _, field := range server.Connection.Fields {
			headers = append(headers, types.StringValue(field.Name))
		}
		value, diags := types.SetValue(types.StringType, headers)
		if diags.HasError() {
			return previous, fmt.Errorf("decode secret headers: %v", diags)
		}
		next.SecretHeaders = value
	}
	next.URL = types.StringValue(server.URL)
	next.Enabled = types.BoolValue(server.Enabled)
	next.OAuthSetup = types.ObjectNull(managedToolServerOAuthSetupTypes)
	if setup := server.Connection.OAuthSetup; setup != nil {
		scopes, diags := types.SetValueFrom(ctx, types.StringType, setup.Scopes)
		if diags.HasError() {
			return previous, fmt.Errorf("decode OAuth scopes: %v", diags)
		}
		if scopes.IsNull() {
			scopes = types.SetValueMust(types.StringType, []attr.Value{})
		}
		value, diags := types.ObjectValue(managedToolServerOAuthSetupTypes, map[string]attr.Value{
			"provider_slug":              types.StringValue(setup.ProviderSlug),
			"callback_url":               types.StringValue(setup.CallbackURL),
			"authorization_url":          types.StringValue(setup.AuthorizationURL),
			"token_url":                  types.StringValue(setup.TokenURL),
			"revocation_url":             nullableStringPointer(setup.RevocationURL),
			"token_endpoint_auth_method": types.StringValue(setup.TokenEndpointAuthMethod),
			"scopes":                     scopes,
			"pkce":                       types.BoolValue(setup.PKCE),
			"oauth_provider_id":          nullableStringPointer(setup.OAuthProviderID),
		})
		if diags.HasError() {
			return previous, fmt.Errorf("decode OAuth setup: %v", diags)
		}
		next.OAuthSetup = value
	}
	return next, nil
}

func (r *managedToolServerResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Managed Tool Server Requires Replacement", "The API does not support updating workspace servers. Change a configured attribute to replace the server.")
}

func (r *managedToolServerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state managedToolServerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Delete(ctx, managedToolServersPath+"/"+url.PathEscape(state.ID.ValueString()), nil, nil, workspaceOpts(state.WorkspaceID)...); err != nil && !isLangSmithNotFound(err) {
		resp.Diagnostics.AddError("Unable to Delete Managed Tool Server", err.Error())
	}
}

func (r *managedToolServerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) > 2 || strings.TrimSpace(parts[0]) == "" || (len(parts) == 2 && strings.TrimSpace(parts[1]) == "") {
		resp.Diagnostics.AddError("Invalid Managed Tool Server Import ID", "Use server_id for the provider workspace, or workspace_id/server_id for an explicit workspace.")
		return
	}
	id := parts[0]
	if len(parts) == 2 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("workspace_id"), parts[0])...)
		id = parts[1]
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
