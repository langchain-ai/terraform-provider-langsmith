package provider

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	frameworkvalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

var (
	_ resource.Resource                = &AccessPolicyAttachmentResource{}
	_ resource.ResourceWithImportState = &AccessPolicyAttachmentResource{}
)

func NewAccessPolicyAttachmentResource() resource.Resource { return &AccessPolicyAttachmentResource{} }

type AccessPolicyAttachmentResource struct{ client *langsmith.Client }

type accessPolicyAttachmentResourceModel struct {
	ID             types.String `tfsdk:"id"`
	RoleID         types.String `tfsdk:"role_id"`
	AccessPolicyID types.String `tfsdk:"access_policy_id"`
}

type accessPolicyAttachmentPayload struct {
	AccessPolicyIDs []string `json:"access_policy_ids"`
}

func (r *AccessPolicyAttachmentResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_access_policy_attachment"
}

func (r *AccessPolicyAttachmentResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Attaches an organization-scoped access policy to a workspace role. Changes replace the attachment. Import with `<role_id>/<access_policy_id>`.",
		Attributes: map[string]schema.Attribute{
			"id":               schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Synthetic attachment ID."},
			"role_id":          schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}, MarkdownDescription: "Workspace role ID."},
			"access_policy_id": schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}, MarkdownDescription: "Access policy ID."},
		},
	}
}

func (r *AccessPolicyAttachmentResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *AccessPolicyAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan accessPolicyAttachmentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.attach(ctx, plan.RoleID.ValueString(), plan.AccessPolicyID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to Attach LangSmith Access Policy", err.Error())
		return
	}
	plan.ID = types.StringValue(accessPolicyAttachmentID(plan.RoleID.ValueString(), plan.AccessPolicyID.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AccessPolicyAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state accessPolicyAttachmentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	attached, err := r.isAttached(ctx, state.RoleID.ValueString(), state.AccessPolicyID.ValueString())
	if err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Access Policy Attachment", err.Error())
		return
	}
	if !attached {
		resp.State.RemoveResource(ctx)
	}
}

func (r *AccessPolicyAttachmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Unable to Update LangSmith Access Policy Attachment", "Access policy attachments are replace-only; all configurable attributes require replacement.")
}

func (r *AccessPolicyAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state accessPolicyAttachmentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.detach(ctx, state.RoleID.ValueString(), state.AccessPolicyID.ValueString()); err != nil && !isLangSmithNotFound(err) {
		resp.Diagnostics.AddError("Unable to Detach LangSmith Access Policy", err.Error())
	}
}

func (r *AccessPolicyAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Invalid Access Policy Attachment Import ID", "Use <role_id>/<access_policy_id>.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("role_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("access_policy_id"), parts[1])...)
}

func (r *AccessPolicyAttachmentResource) attach(ctx context.Context, roleID, policyID string) error {
	return r.client.Post(ctx, accessPolicyRolePath(roleID), accessPolicyAttachmentPayload{AccessPolicyIDs: []string{policyID}}, nil, option.WithMaxRetries(0))
}

func (r *AccessPolicyAttachmentResource) isAttached(ctx context.Context, roleID, policyID string) (bool, error) {
	var policy accessPolicyAPI
	if err := r.client.Get(ctx, accessPolicyPath(policyID), nil, &policy); err != nil {
		return false, err
	}
	return slices.Contains(policy.RoleIDs, roleID), nil
}

func (r *AccessPolicyAttachmentResource) detach(ctx context.Context, roleID, policyID string) error {
	params := url.Values{"access_policy_ids": []string{policyID}}
	return r.client.Delete(ctx, accessPolicyRolePath(roleID)+"?"+params.Encode(), nil, nil)
}

func accessPolicyAttachmentID(roleID, policyID string) string { return roleID + "/" + policyID }

func accessPolicyRolePath(roleID string) string {
	return fmt.Sprintf("api/v1/platform/orgs/current/roles/%s/access-policies", roleID)
}
