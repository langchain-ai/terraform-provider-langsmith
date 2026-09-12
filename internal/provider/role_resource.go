package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	frameworkvalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var (
	_ resource.Resource                = &WorkspaceRoleResource{}
	_ resource.ResourceWithImportState = &WorkspaceRoleResource{}
)

func NewWorkspaceRoleResource() resource.Resource {
	return &WorkspaceRoleResource{}
}

type WorkspaceRoleResource struct {
	client *langsmith.Client
}

type roleResourceModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	DisplayName    types.String `tfsdk:"display_name"`
	Description    types.String `tfsdk:"description"`
	OrganizationID types.String `tfsdk:"organization_id"`
	Permissions    []string     `tfsdk:"permissions"`
}

func (r *WorkspaceRoleResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workspace_role"
}

func (r *WorkspaceRoleResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a custom LangSmith workspace role.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Role ID.",
			},
			"name": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Generated role name.",
			},
			"display_name": schema.StringAttribute{
				Required:            true,
				Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
				MarkdownDescription: "Role display name.",
			},
			"description": schema.StringAttribute{
				Required:            true,
				Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
				MarkdownDescription: "Role description.",
			},
			"organization_id": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Organization ID for this custom role.",
			},
			"permissions": schema.ListAttribute{
				Required:            true,
				ElementType:         types.StringType,
				Validators:          []frameworkvalidator.List{nonEmptyListValidator{}},
				MarkdownDescription: "Permissions assigned to this role.",
			},
		},
	}
}

func (r *WorkspaceRoleResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *WorkspaceRoleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan roleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !validateRoleResourceRequiredFields(plan, &resp.Diagnostics) {
		return
	}

	role, err := r.createRole(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Workspace Role", err.Error())
		return
	}
	next := roleModelFromAPI(role)
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *WorkspaceRoleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state roleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	role, err := r.readRole(ctx, state.ID.ValueString())
	if err != nil {
		if isLangSmithNotFound(err) || errors.Is(err, errRoleNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Workspace Role", err.Error())
		return
	}
	next := roleModelFromAPI(role)
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *WorkspaceRoleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan roleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state roleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !validateRoleResourceRequiredFields(plan, &resp.Diagnostics) {
		return
	}

	roleID := firstNonEmpty(state.ID.ValueString(), plan.ID.ValueString())
	if roleID == "" {
		resp.Diagnostics.AddError("Unable to Update LangSmith Workspace Role", "Missing role ID in plan and state.")
		return
	}

	role, err := r.updateRole(ctx, roleID, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Workspace Role", err.Error())
		return
	}
	next := roleModelFromAPI(role)
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *WorkspaceRoleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state roleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.deleteRole(ctx, state.ID.ValueString()); err != nil {
		if isLangSmithNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to Delete LangSmith Workspace Role", err.Error())
		return
	}
}

func (r *WorkspaceRoleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *WorkspaceRoleResource) createRole(ctx context.Context, plan roleResourceModel) (roleAPI, error) {
	var result roleAPI
	if err := r.client.Post(ctx, "api/v1/orgs/current/roles", rolePayloadFromModel(plan), &result); err != nil {
		return roleAPI{}, err
	}
	if result.ID == "" {
		return roleAPI{}, errors.New("LangSmith did not return a role ID")
	}
	return r.readRole(ctx, result.ID)
}

func (r *WorkspaceRoleResource) updateRole(ctx context.Context, roleID string, plan roleResourceModel) (roleAPI, error) {
	var result roleAPI
	if err := r.client.Patch(ctx, roleResourcePath(roleID), rolePayloadFromModel(plan), &result); err != nil {
		return roleAPI{}, err
	}
	return r.readRole(ctx, roleID)
}

func (r *WorkspaceRoleResource) deleteRole(ctx context.Context, roleID string) error {
	return r.client.Delete(ctx, roleResourcePath(roleID), nil, nil)
}

func (r *WorkspaceRoleResource) readRole(ctx context.Context, roleID string) (roleAPI, error) {
	roles, err := listRoles(ctx, r.client)
	if err != nil {
		return roleAPI{}, err
	}
	return findRoleByID(roles, roleID, accessScopeWorkspace)
}

func rolePayloadFromModel(data roleResourceModel) rolePayload {
	return rolePayload{
		DisplayName: data.DisplayName.ValueString(),
		Description: data.Description.ValueString(),
		Permissions: data.Permissions,
	}
}

func roleModelFromAPI(role roleAPI) roleResourceModel {
	return roleResourceModel{
		ID:             types.StringValue(role.ID),
		Name:           nullableString(role.Name),
		DisplayName:    types.StringValue(role.DisplayName),
		Description:    types.StringValue(role.Description),
		OrganizationID: nullableString(role.OrganizationID),
		Permissions:    coalesceStringSlice(role.Permissions),
	}
}

func coalesceStringSlice(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func validateRoleResourceRequiredFields(data roleResourceModel, diagnostics interface {
	AddAttributeError(attributePath path.Path, summary string, detail string)
}) bool {
	ok := true
	if strings.TrimSpace(data.DisplayName.ValueString()) == "" {
		diagnostics.AddAttributeError(path.Root("display_name"), "Invalid Role Display Name", "display_name must not be empty.")
		ok = false
	}
	if strings.TrimSpace(data.Description.ValueString()) == "" {
		diagnostics.AddAttributeError(path.Root("description"), "Invalid Role Description", "description must not be empty.")
		ok = false
	}
	if len(data.Permissions) == 0 {
		diagnostics.AddAttributeError(path.Root("permissions"), "Invalid Role Permissions", "permissions must contain at least one permission.")
		ok = false
	}
	return ok
}

func roleResourcePath(roleID string) string {
	return fmt.Sprintf("api/v1/orgs/current/roles/%s", roleID)
}

type nonEmptyStringValidator struct{}

func (v nonEmptyStringValidator) Description(ctx context.Context) string {
	return "value must not be empty"
}

func (v nonEmptyStringValidator) MarkdownDescription(ctx context.Context) string {
	return "value must not be empty"
}

func (v nonEmptyStringValidator) ValidateString(ctx context.Context, req frameworkvalidator.StringRequest, resp *frameworkvalidator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if strings.TrimSpace(req.ConfigValue.ValueString()) == "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Empty String", "Value must not be empty.")
	}
}

type nonEmptyListValidator struct{}

func (v nonEmptyListValidator) Description(ctx context.Context) string {
	return "list must contain at least one element"
}

func (v nonEmptyListValidator) MarkdownDescription(ctx context.Context) string {
	return "list must contain at least one element"
}

func (v nonEmptyListValidator) ValidateList(ctx context.Context, req frameworkvalidator.ListRequest, resp *frameworkvalidator.ListResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if len(req.ConfigValue.Elements()) == 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Empty List", "List must contain at least one element.")
	}
}
