package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var (
	_ resource.Resource                = &managedToolProfileCredentialResource{}
	_ resource.ResourceWithConfigure   = &managedToolProfileCredentialResource{}
	_ resource.ResourceWithImportState = &managedToolProfileCredentialResource{}
)

type managedToolProfileCredentialResource struct {
	client *langsmith.Client
}

type managedToolProfileCredentialModel struct {
	ID          types.String `tfsdk:"id"`
	ProfileID   types.String `tfsdk:"profile_id"`
	ServerID    types.String `tfsdk:"server_id"`
	WorkspaceID types.String `tfsdk:"workspace_id"`
	Fields      types.Map    `tfsdk:"fields"`
}

func NewManagedToolProfileCredentialResource() resource.Resource {
	return &managedToolProfileCredentialResource{}
}

func managedToolProfileCredentialPath(profileID, serverID string) string {
	return managedToolProfilePath(profileID) + "/servers/" + url.PathEscape(serverID) + "/credential"
}

func (r *managedToolProfileCredentialResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_managed_tool_profile_credential"
}

func (r *managedToolProfileCredentialResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	nonblank := []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^\S+$`), "must be nonblank without whitespace")}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Stores a Managed Tools profile's header credential for a secret-backed server (`connection_type = \"secret\"`). LangSmith never returns credential values, so Terraform cannot detect values changed outside Terraform; change `fields` to rewrite them. Requires a user-owned API key with `workspaces:credentials:manage` and `mcp-servers:invoke` for the server. A workspace credential on the server takes precedence over profile credentials.",
		Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{Computed: true, MarkdownDescription: "`profile_id/server_id`.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"profile_id":   schema.StringAttribute{Required: true, MarkdownDescription: "Profile that owns the credential.", Validators: nonblank, PlanModifiers: replace},
			"server_id":    schema.StringAttribute{Required: true, MarkdownDescription: "Secret-backed workspace server the credential authenticates to.", Validators: nonblank, PlanModifiers: replace},
			"workspace_id": schema.StringAttribute{Optional: true, MarkdownDescription: "Workspace that owns the profile and server. Defaults to the provider workspace. Use `workspace_id/profile_id/server_id` when importing into a workspace other than the provider default.", Validators: nonblank, PlanModifiers: replace},
			"fields": schema.MapAttribute{
				Required:            true,
				Sensitive:           true,
				ElementType:         types.StringType,
				MarkdownDescription: "Header values keyed by the server's declared `secret_headers`, including any prefix such as `Bearer `. Every declared header must be present. Values are written on create and change and never read back.",
				Validators: []validator.Map{
					mapvalidator.SizeAtLeast(1),
					mapvalidator.ValueStringsAre(stringvalidator.RegexMatches(regexp.MustCompile(`^[^\r\n\x00]*\S[^\r\n\x00]*$`), "must be nonblank without line breaks; wrap file-sourced values in trimspace()")),
				},
			},
		},
	}
}

func (r *managedToolProfileCredentialResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *managedToolProfileCredentialResource) put(ctx context.Context, plan *managedToolProfileCredentialModel) error {
	fields := map[string]string{}
	if diags := plan.Fields.ElementsAs(ctx, &fields, false); diags.HasError() {
		return errors.New("decode credential fields")
	}
	body := map[string]any{"fields": fields}
	if err := r.client.Put(ctx, managedToolProfileCredentialPath(plan.ProfileID.ValueString(), plan.ServerID.ValueString()), body, nil, workspaceOpts(plan.WorkspaceID)...); err != nil {
		return err
	}
	plan.ID = types.StringValue(plan.ProfileID.ValueString() + "/" + plan.ServerID.ValueString())
	return nil
}

func (r *managedToolProfileCredentialResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan managedToolProfileCredentialModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Unable to Store Managed Tool Profile Credential", managedToolCredentialError(err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read confirms the profile still exists. The API does not report whether a
// profile holds a credential for a server, and never returns its values.
func (r *managedToolProfileCredentialResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state managedToolProfileCredentialModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var profile managedToolProfileAPI
	if err := r.client.Get(ctx, managedToolProfilePath(state.ProfileID.ValueString()), nil, &profile, workspaceOpts(state.WorkspaceID)...); err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read Managed Tool Profile", err.Error())
		return
	}
	state.ID = types.StringValue(state.ProfileID.ValueString() + "/" + state.ServerID.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *managedToolProfileCredentialResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan managedToolProfileCredentialModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Unable to Store Managed Tool Profile Credential", managedToolCredentialError(err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *managedToolProfileCredentialResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state managedToolProfileCredentialModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Delete(ctx, managedToolProfileCredentialPath(state.ProfileID.ValueString(), state.ServerID.ValueString()), nil, nil, workspaceOpts(state.WorkspaceID)...); err != nil && !isLangSmithNotFound(err) {
		resp.Diagnostics.AddError("Unable to Delete Managed Tool Profile Credential", managedToolCredentialError(err))
	}
}

// ImportState accepts `profile_id/server_id` or
// `workspace_id/profile_id/server_id`. Imported credentials have no fields
// until the next apply rewrites them from configuration.
func (r *managedToolProfileCredentialResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	valid := len(parts) == 2 || len(parts) == 3
	for _, part := range parts {
		valid = valid && strings.TrimSpace(part) != ""
	}
	if !valid {
		resp.Diagnostics.AddError("Invalid Managed Tool Profile Credential Import ID", "Use profile_id/server_id for the provider workspace, or workspace_id/profile_id/server_id for an explicit workspace.")
		return
	}
	if len(parts) == 3 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("workspace_id"), parts[0])...)
		parts = parts[1:]
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("profile_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("server_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0]+"/"+parts[1])...)
}

// managedToolCredentialError omits response bodies, which can describe the
// rejected credential, and explains the common status codes instead.
func managedToolCredentialError(err error) string {
	var apiErr *langsmith.Error
	if !errors.As(err, &apiErr) {
		return "LangSmith credential request failed. Details are suppressed to protect credentials; verify the configuration and connectivity."
	}
	hint := ""
	switch apiErr.StatusCode {
	case 400:
		hint = " The fields must name exactly the server's declared secret headers."
	case 403:
		hint = " The API key must be user-owned and hold workspaces:credentials:manage and mcp-servers:invoke."
	case 404:
		hint = " The profile or server was not found in this workspace."
	case 409:
		hint = " The server may not use API key credentials, or a workspace credential already covers it."
	}
	return fmt.Sprintf("LangSmith credential request failed (HTTP %d). Response details are suppressed to protect credentials.%s", apiErr.StatusCode, hint)
}
