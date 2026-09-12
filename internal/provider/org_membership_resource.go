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
	orgMembershipActivePath  = "api/v1/orgs/current/members/active"
	orgMembershipPendingPath = "api/v1/orgs/current/members/pending"
	orgMembershipCreatePath  = "api/v1/orgs/current/members"
)

var (
	_ resource.Resource                = &OrgMembershipResource{}
	_ resource.ResourceWithImportState = &OrgMembershipResource{}
)

func NewOrgMembershipResource() resource.Resource {
	return &OrgMembershipResource{}
}

type OrgMembershipResource struct {
	client *langsmith.Client
}

type orgMembershipResourceModel struct {
	ID     types.String `tfsdk:"id"`
	Email  types.String `tfsdk:"email"`
	RoleID types.String `tfsdk:"role_id"`
	Status types.String `tfsdk:"status"`
}

type membershipRolePatchPayload struct {
	RoleID string `json:"role_id"`
}

func (r *OrgMembershipResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_org_membership"
}

func (r *OrgMembershipResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a LangSmith organization membership. Creating a membership sends an invite when the user has not accepted org membership yet.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Stable membership ID derived from the normalized email.",
			},
			"email": schema.StringAttribute{
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
				MarkdownDescription: "User email address for the organization membership.",
			},
			"role_id": schema.StringAttribute{
				Required:            true,
				Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
				MarkdownDescription: "Organization role ID to assign.",
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Membership status: `active` for accepted membership or `pending` for an outstanding invite.",
			},
		},
	}
}

func (r *OrgMembershipResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *OrgMembershipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan orgMembershipResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !validateOrgMembershipRequiredFields(plan, &resp.Diagnostics) {
		return
	}

	next, err := r.ensureOrgMembership(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Organization Membership", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *OrgMembershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state orgMembershipResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, ok, err := r.readOrgMembership(ctx, state.Email.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read LangSmith Organization Membership", err.Error())
		return
	}
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, orgMembershipModelFromReadResult(state.Email.ValueString(), result))...)
}

func (r *OrgMembershipResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan orgMembershipResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !validateOrgMembershipRequiredFields(plan, &resp.Diagnostics) {
		return
	}

	next, err := r.ensureOrgMembership(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Organization Membership", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *OrgMembershipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state orgMembershipResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.deleteOrgMembership(ctx, state); err != nil {
		if isLangSmithNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to Delete LangSmith Organization Membership", err.Error())
		return
	}
}

func (r *OrgMembershipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	email, err := parseOrgMembershipImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), orgMembershipID(email))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("email"), email)...)
}

func (r *OrgMembershipResource) ensureOrgMembership(ctx context.Context, plan orgMembershipResourceModel) (orgMembershipResourceModel, error) {
	email := normalizeMembershipEmail(plan.Email.ValueString())
	roleID := stringConfig(plan.RoleID)
	if err := validateRoleScope(ctx, r.client, roleID, accessScopeOrganization); err != nil {
		return orgMembershipResourceModel{}, err
	}

	result, ok, err := r.readOrgMembership(ctx, email)
	if err != nil {
		return orgMembershipResourceModel{}, err
	}
	if ok {
		switch result.Status {
		case membershipStatusActive:
			if result.RoleID != roleID {
				if err := r.updateActiveOrgMembershipRole(ctx, result.IdentityID, roleID); err != nil {
					return orgMembershipResourceModel{}, err
				}
				return r.readOrgMembershipModel(ctx, email)
			}
			return orgMembershipModelFromReadResult(email, result), nil
		case membershipStatusPending:
			if result.RoleID != roleID {
				if err := r.deletePendingOrgMembership(ctx, result.PendingIdentityID); err != nil && !isLangSmithNotFound(err) {
					return orgMembershipResourceModel{}, err
				}
				if err := r.createOrgMembershipInvite(ctx, email, roleID); err != nil {
					return orgMembershipResourceModel{}, err
				}
				return r.readOrgMembershipModel(ctx, email)
			}
			return orgMembershipModelFromReadResult(email, result), nil
		default:
			return orgMembershipResourceModel{}, fmt.Errorf("unsupported membership status %q for %s", result.Status, email)
		}
	}

	if err := r.createOrgMembershipInvite(ctx, email, roleID); err != nil {
		return orgMembershipResourceModel{}, err
	}
	return r.readOrgMembershipModel(ctx, email)
}

func (r *OrgMembershipResource) readOrgMembershipModel(ctx context.Context, email string) (orgMembershipResourceModel, error) {
	result, ok, err := r.readOrgMembership(ctx, email)
	if err != nil {
		return orgMembershipResourceModel{}, err
	}
	if !ok {
		return orgMembershipResourceModel{}, fmt.Errorf("organization membership for %s was not found after update", normalizeMembershipEmail(email))
	}
	return orgMembershipModelFromReadResult(email, result), nil
}

func (r *OrgMembershipResource) readOrgMembership(ctx context.Context, email string) (membershipReadResult, bool, error) {
	return readMembershipLists(ctx, r.client, orgMembershipActivePath, orgMembershipPendingPath, email)
}

func (r *OrgMembershipResource) createOrgMembershipInvite(ctx context.Context, email string, roleID string) error {
	var result pendingIdentityAPI
	return r.client.Post(ctx, orgMembershipCreatePath, orgInvitePayload{
		Email:  normalizeMembershipEmail(email),
		RoleID: roleID,
	}, &result)
}

func (r *OrgMembershipResource) updateActiveOrgMembershipRole(ctx context.Context, identityID string, roleID string) error {
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return errors.New("active membership is missing member ID")
	}
	return r.client.Patch(ctx, orgMembershipActiveMemberPath(identityID), membershipRolePatchPayload{RoleID: roleID}, nil)
}

func (r *OrgMembershipResource) deleteOrgMembership(ctx context.Context, state orgMembershipResourceModel) error {
	email := stringConfig(state.Email)
	if email == "" {
		return nil
	}
	result, ok, err := r.readOrgMembership(ctx, email)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if result.Status == membershipStatusPending {
		return r.deletePendingOrgMembership(ctx, result.PendingIdentityID)
	}
	if result.Status == membershipStatusActive {
		return r.deleteActiveOrgMembership(ctx, result.IdentityID)
	}
	return nil
}

func (r *OrgMembershipResource) deletePendingOrgMembership(ctx context.Context, pendingIdentityID string) error {
	pendingIdentityID = strings.TrimSpace(pendingIdentityID)
	if pendingIdentityID == "" {
		return errors.New("pending membership is missing invite ID")
	}
	return r.client.Delete(ctx, orgMembershipPendingMemberPath(pendingIdentityID), nil, nil)
}

func (r *OrgMembershipResource) deleteActiveOrgMembership(ctx context.Context, identityID string) error {
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return errors.New("active membership is missing member ID")
	}
	return r.client.Delete(ctx, orgMembershipActiveMemberPath(identityID), nil, nil)
}

func orgMembershipModelFromReadResult(email string, result membershipReadResult) orgMembershipResourceModel {
	return orgMembershipResourceModel{
		ID:     types.StringValue(orgMembershipID(email)),
		Email:  types.StringValue(normalizeMembershipEmail(email)),
		RoleID: types.StringValue(result.RoleID),
		Status: types.StringValue(result.Status),
	}
}

func validateOrgMembershipRequiredFields(data orgMembershipResourceModel, diagnostics interface {
	AddAttributeError(attributePath path.Path, summary string, detail string)
}) bool {
	ok := true
	email := data.Email.ValueString()
	normalizedEmail := normalizeMembershipEmail(email)
	if normalizedEmail == "" {
		diagnostics.AddAttributeError(path.Root("email"), "Invalid Organization Membership Email", "email must not be empty.")
		ok = false
	} else if email != normalizedEmail {
		diagnostics.AddAttributeError(path.Root("email"), "Invalid Organization Membership Email", "email must be lowercase and must not contain leading or trailing whitespace.")
		ok = false
	}
	if stringConfig(data.RoleID) == "" {
		diagnostics.AddAttributeError(path.Root("role_id"), "Invalid Organization Membership Role", "role_id must not be empty.")
		ok = false
	}
	return ok
}

func orgMembershipActiveMemberPath(identityID string) string {
	return fmt.Sprintf("api/v1/orgs/current/members/%s", identityID)
}

func orgMembershipPendingMemberPath(pendingIdentityID string) string {
	return fmt.Sprintf("api/v1/orgs/current/members/%s/pending", pendingIdentityID)
}

func parseOrgMembershipImportID(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "org/current/email/")
	email := normalizeMembershipEmail(value)
	if email == "" {
		return "", errors.New("use import ID format `<email>` or `org/current/email/<email>`")
	}
	return email, nil
}
