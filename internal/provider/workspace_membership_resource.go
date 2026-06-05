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

const (
	workspaceMembershipActivePath  = "api/v1/workspaces/current/members/active"
	workspaceMembershipPendingPath = "api/v1/workspaces/current/members/pending"
	workspaceMembershipCreatePath  = "api/v1/workspaces/current/members"
)

var (
	_ resource.Resource                = &WorkspaceMembershipResource{}
	_ resource.ResourceWithImportState = &WorkspaceMembershipResource{}
)

func NewWorkspaceMembershipResource() resource.Resource {
	return &WorkspaceMembershipResource{}
}

type WorkspaceMembershipResource struct {
	client *langsmith.Client
}

type workspaceMembershipResourceModel struct {
	ID          types.String `tfsdk:"id"`
	WorkspaceID types.String `tfsdk:"workspace_id"`
	Email       types.String `tfsdk:"email"`
	RoleID      types.String `tfsdk:"role_id"`
	Status      types.String `tfsdk:"status"`
}

func (r *WorkspaceMembershipResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workspace_membership"
}

func (r *WorkspaceMembershipResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a LangSmith workspace membership for an accepted organization member.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Stable membership ID derived from workspace ID and normalized email.",
			},
			"workspace_id": schema.StringAttribute{
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
				MarkdownDescription: "Workspace ID.",
			},
			"email": schema.StringAttribute{
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
				MarkdownDescription: "User email address for the workspace membership. The user must have accepted organization membership before this resource can create workspace membership.",
			},
			"role_id": schema.StringAttribute{
				Required:            true,
				Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
				MarkdownDescription: "Workspace role ID to assign.",
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Membership status: `active` for accepted membership or `pending` when an existing pending workspace invite is returned by LangSmith.",
			},
		},
	}
}

func (r *WorkspaceMembershipResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *WorkspaceMembershipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan workspaceMembershipResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !validateWorkspaceMembershipRequiredFields(plan, &resp.Diagnostics) {
		return
	}

	next, err := r.ensureWorkspaceMembership(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Workspace Membership", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *WorkspaceMembershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state workspaceMembershipResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, ok, err := r.readWorkspaceMembership(ctx, state.WorkspaceID.ValueString(), state.Email.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read LangSmith Workspace Membership", err.Error())
		return
	}
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, workspaceMembershipModelFromReadResult(state.WorkspaceID.ValueString(), state.Email.ValueString(), result))...)
}

func (r *WorkspaceMembershipResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan workspaceMembershipResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !validateWorkspaceMembershipRequiredFields(plan, &resp.Diagnostics) {
		return
	}

	next, err := r.ensureWorkspaceMembership(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Workspace Membership", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *WorkspaceMembershipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state workspaceMembershipResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.deleteWorkspaceMembership(ctx, state); err != nil {
		if isLangSmithNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to Delete LangSmith Workspace Membership", err.Error())
		return
	}
}

func (r *WorkspaceMembershipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	workspaceID, email, err := parseWorkspaceMembershipImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), workspaceMembershipID(workspaceID, email))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("workspace_id"), workspaceID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("email"), email)...)
}

func (r *WorkspaceMembershipResource) ensureWorkspaceMembership(ctx context.Context, plan workspaceMembershipResourceModel) (workspaceMembershipResourceModel, error) {
	workspaceID := stringConfig(plan.WorkspaceID)
	email := normalizeMembershipEmail(plan.Email.ValueString())
	roleID := stringConfig(plan.RoleID)
	if err := validateRoleScope(ctx, r.client, roleID, accessScopeWorkspace); err != nil {
		return workspaceMembershipResourceModel{}, err
	}

	result, ok, err := r.readWorkspaceMembership(ctx, workspaceID, email)
	if err != nil {
		return workspaceMembershipResourceModel{}, err
	}
	if ok {
		switch result.Status {
		case membershipStatusActive:
			if result.RoleID != roleID {
				if err := r.updateActiveWorkspaceMembershipRole(ctx, workspaceID, result.IdentityID, roleID); err != nil {
					return workspaceMembershipResourceModel{}, err
				}
				return r.readWorkspaceMembershipModel(ctx, workspaceID, email)
			}
			return workspaceMembershipModelFromReadResult(workspaceID, email, result), nil
		case membershipStatusPending:
			if result.RoleID != roleID {
				return workspaceMembershipResourceModel{}, fmt.Errorf("pending workspace membership for %s cannot be updated; remove the pending invite or wait for organization membership acceptance before changing the workspace role", email)
			}
			return workspaceMembershipModelFromReadResult(workspaceID, email, result), nil
		default:
			return workspaceMembershipResourceModel{}, fmt.Errorf("unsupported membership status %q for %s in workspace %s", result.Status, email, workspaceID)
		}
	}

	orgMembership, ok, err := readMembershipLists(ctx, r.client, orgMembershipActivePath, orgMembershipPendingPath, email)
	if err != nil {
		return workspaceMembershipResourceModel{}, err
	}
	if !ok {
		return workspaceMembershipResourceModel{}, fmt.Errorf("organization membership for %s was not found; create or import langsmith_org_membership first", email)
	}
	if orgMembership.Status == membershipStatusPending {
		return workspaceMembershipResourceModel{}, fmt.Errorf("organization membership for %s is pending; the invite must be accepted before Terraform can add workspace membership", email)
	}

	payload, err := workspaceMemberPayloadFromOrgMembership(orgMembership, roleID)
	if err != nil {
		return workspaceMembershipResourceModel{}, err
	}
	if err := r.createWorkspaceMembership(ctx, workspaceID, payload); err != nil {
		return workspaceMembershipResourceModel{}, err
	}
	return r.readWorkspaceMembershipModel(ctx, workspaceID, email)
}

func (r *WorkspaceMembershipResource) readWorkspaceMembershipModel(ctx context.Context, workspaceID string, email string) (workspaceMembershipResourceModel, error) {
	result, ok, err := r.readWorkspaceMembership(ctx, workspaceID, email)
	if err != nil {
		return workspaceMembershipResourceModel{}, err
	}
	if !ok {
		return workspaceMembershipResourceModel{}, fmt.Errorf("workspace membership for %s in workspace %s was not found after update", normalizeMembershipEmail(email), strings.TrimSpace(workspaceID))
	}
	return workspaceMembershipModelFromReadResult(workspaceID, email, result), nil
}

func (r *WorkspaceMembershipResource) readWorkspaceMembership(ctx context.Context, workspaceID string, email string) (membershipReadResult, bool, error) {
	return readMembershipLists(ctx, r.client, workspaceMembershipActivePath, workspaceMembershipPendingPath, email, workspaceTenantOption(workspaceID))
}

func (r *WorkspaceMembershipResource) createWorkspaceMembership(ctx context.Context, workspaceID string, payload workspaceMemberPayload) error {
	var result memberIdentityAPI
	return r.client.Post(ctx, workspaceMembershipCreatePath, payload, &result, workspaceTenantOption(workspaceID))
}

func (r *WorkspaceMembershipResource) updateActiveWorkspaceMembershipRole(ctx context.Context, workspaceID string, identityID string, roleID string) error {
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return errors.New("active workspace membership is missing member ID")
	}
	return r.client.Patch(ctx, workspaceMembershipActiveMemberPath(identityID), membershipRolePatchPayload{RoleID: roleID}, nil, workspaceTenantOption(workspaceID))
}

func (r *WorkspaceMembershipResource) deleteWorkspaceMembership(ctx context.Context, state workspaceMembershipResourceModel) error {
	workspaceID := stringConfig(state.WorkspaceID)
	email := stringConfig(state.Email)
	if workspaceID == "" || email == "" {
		return nil
	}
	result, ok, err := r.readWorkspaceMembership(ctx, workspaceID, email)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if result.Status == membershipStatusPending {
		return r.deletePendingWorkspaceMembership(ctx, workspaceID, result.PendingIdentityID)
	}
	if result.Status == membershipStatusActive {
		return r.deleteActiveWorkspaceMembership(ctx, workspaceID, result.IdentityID)
	}
	return nil
}

func (r *WorkspaceMembershipResource) deletePendingWorkspaceMembership(ctx context.Context, workspaceID string, pendingIdentityID string) error {
	pendingIdentityID = strings.TrimSpace(pendingIdentityID)
	if pendingIdentityID == "" {
		return errors.New("pending workspace membership is missing invite ID")
	}
	return r.client.Delete(ctx, workspaceMembershipPendingMemberPath(pendingIdentityID), nil, nil, workspaceTenantOption(workspaceID))
}

func (r *WorkspaceMembershipResource) deleteActiveWorkspaceMembership(ctx context.Context, workspaceID string, identityID string) error {
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return errors.New("active workspace membership is missing member ID")
	}
	return r.client.Delete(ctx, workspaceMembershipActiveMemberPath(identityID), nil, nil, workspaceTenantOption(workspaceID))
}

func workspaceMemberPayloadFromOrgMembership(result membershipReadResult, roleID string) (workspaceMemberPayload, error) {
	payload := workspaceMemberPayload{RoleID: roleID}
	switch {
	case result.IdentityID != "":
		payload.OrgIdentityID = result.IdentityID
	case result.LSUserID != "":
		payload.LSUserID = result.LSUserID
	case result.UserID != "":
		payload.UserID = result.UserID
	default:
		return workspaceMemberPayload{}, errors.New("active organization membership is missing a user identifier required for workspace membership")
	}
	return payload, nil
}

func workspaceMembershipModelFromReadResult(workspaceID string, email string, result membershipReadResult) workspaceMembershipResourceModel {
	workspaceID = strings.TrimSpace(workspaceID)
	return workspaceMembershipResourceModel{
		ID:          types.StringValue(workspaceMembershipID(workspaceID, email)),
		WorkspaceID: types.StringValue(workspaceID),
		Email:       types.StringValue(normalizeMembershipEmail(email)),
		RoleID:      types.StringValue(result.RoleID),
		Status:      types.StringValue(result.Status),
	}
}

func validateWorkspaceMembershipRequiredFields(data workspaceMembershipResourceModel, diagnostics interface {
	AddAttributeError(attributePath path.Path, summary string, detail string)
}) bool {
	ok := true
	workspaceID := data.WorkspaceID.ValueString()
	if strings.TrimSpace(workspaceID) == "" {
		diagnostics.AddAttributeError(path.Root("workspace_id"), "Invalid Workspace Membership Workspace", "workspace_id must not be empty.")
		ok = false
	} else if workspaceID != strings.TrimSpace(workspaceID) {
		diagnostics.AddAttributeError(path.Root("workspace_id"), "Invalid Workspace Membership Workspace", "workspace_id must not contain leading or trailing whitespace.")
		ok = false
	}

	email := data.Email.ValueString()
	normalizedEmail := normalizeMembershipEmail(email)
	if normalizedEmail == "" {
		diagnostics.AddAttributeError(path.Root("email"), "Invalid Workspace Membership Email", "email must not be empty.")
		ok = false
	} else if email != normalizedEmail {
		diagnostics.AddAttributeError(path.Root("email"), "Invalid Workspace Membership Email", "email must be lowercase and must not contain leading or trailing whitespace.")
		ok = false
	}

	if stringConfig(data.RoleID) == "" {
		diagnostics.AddAttributeError(path.Root("role_id"), "Invalid Workspace Membership Role", "role_id must not be empty.")
		ok = false
	}
	return ok
}

func workspaceMembershipActiveMemberPath(identityID string) string {
	return fmt.Sprintf("api/v1/workspaces/current/members/%s", identityID)
}

func workspaceMembershipPendingMemberPath(pendingIdentityID string) string {
	return fmt.Sprintf("api/v1/workspaces/current/members/%s/pending", pendingIdentityID)
}

func parseWorkspaceMembershipImportID(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "workspace/") {
		workspaceID, email, ok := strings.Cut(strings.TrimPrefix(value, "workspace/"), "/email/")
		if !ok {
			return "", "", errors.New("Use import ID format `<workspace_id>/<email>` or `workspace/<workspace_id>/email/<email>`.")
		}
		return normalizedWorkspaceMembershipImportID(workspaceID, email)
	}

	workspaceID, email, ok := strings.Cut(value, "/")
	if !ok {
		return "", "", errors.New("Use import ID format `<workspace_id>/<email>` or `workspace/<workspace_id>/email/<email>`.")
	}
	return normalizedWorkspaceMembershipImportID(workspaceID, email)
}

func normalizedWorkspaceMembershipImportID(workspaceID string, email string) (string, string, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	email = normalizeMembershipEmail(email)
	if workspaceID == "" || email == "" {
		return "", "", errors.New("Use import ID format `<workspace_id>/<email>` or `workspace/<workspace_id>/email/<email>`.")
	}
	return workspaceID, email, nil
}
