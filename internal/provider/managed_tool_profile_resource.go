package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

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

const managedToolProfilesPath = "v1/managed-tools/profiles"

var (
	_ resource.Resource                = &managedToolProfileResource{}
	_ resource.ResourceWithConfigure   = &managedToolProfileResource{}
	_ resource.ResourceWithImportState = &managedToolProfileResource{}
)

type managedToolProfileResource struct {
	client *langsmith.Client
}

type managedToolProfileModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	WorkspaceID types.String `tfsdk:"workspace_id"`
}

type managedToolProfileAPI struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func NewManagedToolProfileResource() resource.Resource {
	return &managedToolProfileResource{}
}

func managedToolProfilePath(id string) string {
	return managedToolProfilesPath + "/" + url.PathEscape(id)
}

func (r *managedToolProfileResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_managed_tool_profile"
}

func (r *managedToolProfileResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a LangSmith Managed Tools profile: a named identity whose tool credentials an admin configures for an agent or other non-user caller. Callers select it with the `X-LangSmith-Profile` header. Requires a user-owned API key with `workspaces:credentials:manage`. Deleting a profile deletes its tool credentials.",
		Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{Computed: true, MarkdownDescription: "Profile ID.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":         schema.StringAttribute{Required: true, MarkdownDescription: "Profile name, unique within the workspace.", Validators: []validator.String{stringvalidator.LengthBetween(1, 256), stringvalidator.RegexMatches(regexp.MustCompile(`^\S(?:[\s\S]*\S)?$`), "must be nonblank without leading or trailing whitespace")}},
			"workspace_id": schema.StringAttribute{Optional: true, MarkdownDescription: "Workspace that owns this profile. Defaults to the provider workspace. Use `workspace_id/profile_id` when importing into a workspace other than the provider default.", Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^\S+$`), "must be nonblank without whitespace")}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		},
	}
}

func (r *managedToolProfileResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *managedToolProfileResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan managedToolProfileModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var profile managedToolProfileAPI
	body := map[string]string{"name": plan.Name.ValueString()}
	if err := r.client.Post(ctx, managedToolProfilesPath, body, &profile, workspaceOpts(plan.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Unable to Create Managed Tool Profile", err.Error())
		return
	}
	next, err := managedToolProfileModelFromAPI(profile, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Created Managed Tool Profile", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *managedToolProfileResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state managedToolProfileModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var profile managedToolProfileAPI
	if err := r.client.Get(ctx, managedToolProfilePath(state.ID.ValueString()), nil, &profile, workspaceOpts(state.WorkspaceID)...); err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read Managed Tool Profile", err.Error())
		return
	}
	next, err := managedToolProfileModelFromAPI(profile, state)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Managed Tool Profile", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *managedToolProfileResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state managedToolProfileModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var profile managedToolProfileAPI
	body := map[string]string{"name": plan.Name.ValueString()}
	if err := r.client.Patch(ctx, managedToolProfilePath(state.ID.ValueString()), body, &profile, workspaceOpts(state.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Unable to Update Managed Tool Profile", err.Error())
		return
	}
	next, err := managedToolProfileModelFromAPI(profile, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Updated Managed Tool Profile", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *managedToolProfileResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state managedToolProfileModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Delete(ctx, managedToolProfilePath(state.ID.ValueString()), nil, nil, workspaceOpts(state.WorkspaceID)...); err != nil && !isLangSmithNotFound(err) {
		resp.Diagnostics.AddError("Unable to Delete Managed Tool Profile", err.Error())
	}
}

func (r *managedToolProfileResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	workspaceID, id, ok := splitWorkspaceImportID(req.ID)
	if !ok {
		resp.Diagnostics.AddError("Invalid Managed Tool Profile Import ID", "Use profile_id for the provider workspace, or workspace_id/profile_id for an explicit workspace.")
		return
	}
	if workspaceID != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("workspace_id"), workspaceID)...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

func managedToolProfileModelFromAPI(profile managedToolProfileAPI, previous managedToolProfileModel) (managedToolProfileModel, error) {
	if profile.ID == "" {
		return previous, errors.New("LangSmith returned a profile without an ID")
	}
	next := previous
	next.ID = types.StringValue(profile.ID)
	next.Name = types.StringValue(profile.Name)
	return next, nil
}

// splitWorkspaceImportID parses `id` or `workspace_id/id`. The workspace is
// empty when the ID omits it.
func splitWorkspaceImportID(raw string) (workspaceID, id string, ok bool) {
	parts := strings.Split(raw, "/")
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return "", "", false
		}
	}
	switch len(parts) {
	case 1:
		return "", parts[0], true
	case 2:
		return parts[0], parts[1], true
	default:
		return "", "", false
	}
}
