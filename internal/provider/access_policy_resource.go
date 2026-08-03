package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"

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
	_ resource.Resource                = &AccessPolicyResource{}
	_ resource.ResourceWithImportState = &AccessPolicyResource{}
)

const accessPoliciesPath = "api/v1/platform/orgs/current/access-policies"

var (
	accessPolicyEffects       = []string{"allow", "deny"}
	accessPolicyResourceTypes = []string{"queue", "dataset", "deployment", "fleet_integration", "mcp_server", "project", "prompt", "sandbox"}
	accessPolicyOperators     = []string{"equals", "not_equals", "equals_ignore_case", "not_equals_ignore_case", "matches", "not_matches", "equals_if_exists", "not_equals_if_exists", "equals_ignore_case_if_exists", "not_equals_ignore_case_if_exists", "matches_if_exists", "not_matches_if_exists"}
)

func NewAccessPolicyResource() resource.Resource { return &AccessPolicyResource{} }

type AccessPolicyResource struct{ client *langsmith.Client }

type accessPolicyResourceModel struct {
	ID              types.String                      `tfsdk:"id"`
	Name            types.String                      `tfsdk:"name"`
	Description     types.String                      `tfsdk:"description"`
	Effect          types.String                      `tfsdk:"effect"`
	ConditionGroups []accessPolicyConditionGroupModel `tfsdk:"condition_groups"`
	RoleIDs         []string                          `tfsdk:"role_ids"`
	CreatedAt       types.String                      `tfsdk:"created_at"`
	UpdatedAt       types.String                      `tfsdk:"updated_at"`
}

type accessPolicyConditionGroupModel struct {
	Permission   types.String                 `tfsdk:"permission"`
	ResourceType types.String                 `tfsdk:"resource_type"`
	Conditions   []accessPolicyConditionModel `tfsdk:"conditions"`
}

type accessPolicyConditionModel struct {
	AttributeName  types.String `tfsdk:"attribute_name"`
	AttributeKey   types.String `tfsdk:"attribute_key"`
	Operator       types.String `tfsdk:"operator"`
	AttributeValue types.String `tfsdk:"attribute_value"`
}

type accessPolicyPayload struct {
	Name            string                       `json:"name"`
	Description     string                       `json:"description"`
	Effect          string                       `json:"effect"`
	ConditionGroups []accessPolicyConditionGroup `json:"condition_groups"`
	RoleIDs         []string                     `json:"role_ids,omitempty"`
}

type accessPolicyConditionGroup struct {
	Permission   string                  `json:"permission"`
	ResourceType string                  `json:"resource_type"`
	Conditions   []accessPolicyCondition `json:"conditions"`
}

type accessPolicyCondition struct {
	AttributeName  string `json:"attribute_name"`
	AttributeKey   string `json:"attribute_key"`
	Operator       string `json:"operator"`
	AttributeValue string `json:"attribute_value"`
}

type accessPolicyAPI struct {
	ID              string                       `json:"id"`
	Name            string                       `json:"name"`
	Description     string                       `json:"description"`
	Effect          string                       `json:"effect"`
	ConditionGroups []accessPolicyConditionGroup `json:"condition_groups"`
	RoleIDs         []string                     `json:"role_ids"`
	CreatedAt       string                       `json:"created_at"`
	UpdatedAt       string                       `json:"updated_at"`
}

type accessPolicyCreateResponse struct {
	ID string `json:"id"`
}

type accessPolicyAttachmentPayload struct {
	AccessPolicyIDs []string `json:"access_policy_ids"`
}

func (r *AccessPolicyResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_access_policy"
}

func (r *AccessPolicyResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an organization-scoped LangSmith ABAC access policy and its workspace-role attachments.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Access policy ID."},
			"name":        schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}, MarkdownDescription: "Access policy name."},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Access policy description."},
			"effect":      schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{oneOfStringValidator{values: accessPolicyEffects}}, MarkdownDescription: "Policy effect: `allow` or `deny`."},
			"condition_groups": schema.SetNestedAttribute{
				Required:            true,
				Validators:          []frameworkvalidator.Set{nonEmptySetValidator{}},
				MarkdownDescription: "Condition groups. Groups are ORed; conditions within each group are ANDed.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"permission":    schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}, MarkdownDescription: "LangSmith permission governed by this group."},
					"resource_type": schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{oneOfStringValidator{values: accessPolicyResourceTypes}}, MarkdownDescription: "ABAC resource type."},
					"conditions": schema.SetNestedAttribute{
						Required:            true,
						Validators:          []frameworkvalidator.Set{nonEmptySetValidator{}},
						MarkdownDescription: "Tag conditions that must all match.",
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"attribute_name":  schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{oneOfStringValidator{values: []string{"resource_tag_key"}}}, MarkdownDescription: "Condition attribute; currently `resource_tag_key`."},
							"attribute_key":   schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}, MarkdownDescription: "Resource tag key to evaluate."},
							"operator":        schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{oneOfStringValidator{values: accessPolicyOperators}}, MarkdownDescription: "Comparison operator."},
							"attribute_value": schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}, MarkdownDescription: "Resource tag value or glob pattern to compare."},
						}},
					},
				}},
			},
			"role_ids":   schema.SetAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Workspace role IDs to attach to this policy."},
			"created_at": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Creation timestamp."},
			"updated_at": schema.StringAttribute{Computed: true, MarkdownDescription: "Last update timestamp."},
		},
	}
}

func (r *AccessPolicyResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *AccessPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan accessPolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.createAccessPolicy(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Access Policy", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *AccessPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state accessPolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.readAccessPolicy(ctx, state.ID.ValueString())
	if err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Access Policy", err.Error())
		return
	}
	model = preserveAccessPolicyOptionalShape(model, state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *AccessPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state accessPolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.updateAccessPolicy(ctx, state.ID.ValueString(), plan)
	if err != nil {
		if !model.ID.IsNull() && !model.ID.IsUnknown() {
			resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
		}
		resp.Diagnostics.AddError("Unable to Update LangSmith Access Policy", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *AccessPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state accessPolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.deleteAccessPolicy(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to Delete LangSmith Access Policy", err.Error())
	}
}

func (r *AccessPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *AccessPolicyResource) createAccessPolicy(ctx context.Context, plan accessPolicyResourceModel) (accessPolicyResourceModel, error) {
	var result accessPolicyCreateResponse
	if err := r.client.Post(ctx, accessPoliciesPath, accessPolicyPayloadFromModel(plan, true), &result, option.WithMaxRetries(0)); err != nil {
		return accessPolicyResourceModel{}, err
	}
	if result.ID == "" {
		return accessPolicyResourceModel{}, errors.New("LangSmith did not return an access policy ID")
	}
	model, err := r.readAccessPolicy(ctx, result.ID)
	if err != nil {
		return accessPolicyResourceModel{}, err
	}
	return preserveAccessPolicyOptionalShape(model, plan), nil
}

func (r *AccessPolicyResource) readAccessPolicy(ctx context.Context, id string) (accessPolicyResourceModel, error) {
	var result accessPolicyAPI
	if err := r.client.Get(ctx, accessPolicyPath(id), nil, &result); err != nil {
		return accessPolicyResourceModel{}, err
	}
	return accessPolicyModelFromAPI(result), nil
}

func (r *AccessPolicyResource) updateAccessPolicy(ctx context.Context, id string, plan accessPolicyResourceModel) (accessPolicyResourceModel, error) {
	var result accessPolicyAPI
	if err := r.client.Patch(ctx, accessPolicyPath(id), accessPolicyPayloadFromModel(plan, false), &result); err != nil {
		return accessPolicyResourceModel{}, err
	}
	if err := r.reconcileAccessPolicyRoles(ctx, id, result.RoleIDs, plan.RoleIDs); err != nil {
		live, readErr := r.readAccessPolicy(ctx, id)
		if readErr != nil {
			return accessPolicyResourceModel{}, errors.Join(err, fmt.Errorf("read partially updated access policy: %w", readErr))
		}
		return preserveAccessPolicyOptionalShape(live, plan), err
	}
	model, err := r.readAccessPolicy(ctx, id)
	if err != nil {
		return accessPolicyResourceModel{}, err
	}
	return preserveAccessPolicyOptionalShape(model, plan), nil
}

func (r *AccessPolicyResource) reconcileAccessPolicyRoles(ctx context.Context, policyID string, current, desired []string) error {
	for _, roleID := range stringSetDifference(desired, current) {
		if err := r.client.Post(ctx, accessPolicyRolePath(roleID), accessPolicyAttachmentPayload{AccessPolicyIDs: []string{policyID}}, nil, option.WithMaxRetries(0)); err != nil {
			return fmt.Errorf("attach access policy to role %s: %w", roleID, err)
		}
	}
	for _, roleID := range stringSetDifference(current, desired) {
		if err := r.client.Delete(ctx, accessPolicyRolePath(roleID), accessPolicyAttachmentPayload{AccessPolicyIDs: []string{policyID}}, nil); err != nil {
			return fmt.Errorf("detach access policy from role %s: %w", roleID, err)
		}
	}
	return nil
}

func (r *AccessPolicyResource) deleteAccessPolicy(ctx context.Context, id string) error {
	if err := r.client.Delete(ctx, accessPolicyPath(id), nil, nil); err != nil && !isLangSmithNotFound(err) {
		return err
	}
	return nil
}

func accessPolicyPath(id string) string { return fmt.Sprintf("%s/%s", accessPoliciesPath, id) }
func accessPolicyRolePath(roleID string) string {
	return fmt.Sprintf("%s/roles/%s/access-policies", accessPoliciesPath, roleID)
}

func accessPolicyPayloadFromModel(model accessPolicyResourceModel, includeRoles bool) accessPolicyPayload {
	payload := accessPolicyPayload{
		Name: model.Name.ValueString(), Description: model.Description.ValueString(), Effect: model.Effect.ValueString(),
		ConditionGroups: make([]accessPolicyConditionGroup, 0, len(model.ConditionGroups)),
	}
	if includeRoles {
		payload.RoleIDs = sortedStrings(model.RoleIDs)
	}
	for _, group := range model.ConditionGroups {
		apiGroup := accessPolicyConditionGroup{Permission: group.Permission.ValueString(), ResourceType: group.ResourceType.ValueString(), Conditions: make([]accessPolicyCondition, 0, len(group.Conditions))}
		for _, condition := range group.Conditions {
			apiGroup.Conditions = append(apiGroup.Conditions, accessPolicyCondition{AttributeName: condition.AttributeName.ValueString(), AttributeKey: condition.AttributeKey.ValueString(), Operator: condition.Operator.ValueString(), AttributeValue: condition.AttributeValue.ValueString()})
		}
		payload.ConditionGroups = append(payload.ConditionGroups, apiGroup)
	}
	return payload
}

func accessPolicyModelFromAPI(api accessPolicyAPI) accessPolicyResourceModel {
	model := accessPolicyResourceModel{
		ID: types.StringValue(api.ID), Name: types.StringValue(api.Name), Description: nullableString(api.Description), Effect: types.StringValue(api.Effect),
		ConditionGroups: make([]accessPolicyConditionGroupModel, 0, len(api.ConditionGroups)), RoleIDs: sortedStrings(api.RoleIDs), CreatedAt: nullableString(api.CreatedAt), UpdatedAt: nullableString(api.UpdatedAt),
	}
	for _, group := range api.ConditionGroups {
		modelGroup := accessPolicyConditionGroupModel{Permission: types.StringValue(group.Permission), ResourceType: types.StringValue(group.ResourceType), Conditions: make([]accessPolicyConditionModel, 0, len(group.Conditions))}
		for _, condition := range group.Conditions {
			modelGroup.Conditions = append(modelGroup.Conditions, accessPolicyConditionModel{AttributeName: types.StringValue(condition.AttributeName), AttributeKey: types.StringValue(condition.AttributeKey), Operator: types.StringValue(condition.Operator), AttributeValue: types.StringValue(condition.AttributeValue)})
		}
		model.ConditionGroups = append(model.ConditionGroups, modelGroup)
	}
	return model
}

func preserveAccessPolicyOptionalShape(model, configured accessPolicyResourceModel) accessPolicyResourceModel {
	if model.Description.IsNull() && !configured.Description.IsNull() && !configured.Description.IsUnknown() && configured.Description.ValueString() == "" {
		model.Description = types.StringValue("")
	}
	if model.RoleIDs == nil && configured.RoleIDs != nil {
		model.RoleIDs = []string{}
	}
	return model
}

func stringSetDifference(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, value := range right {
		rightSet[value] = struct{}{}
	}
	var result []string
	for _, value := range left {
		if _, exists := rightSet[value]; !exists {
			result = append(result, value)
		}
	}
	return sortedStrings(result)
}

func sortedStrings(values []string) []string {
	if values == nil {
		return nil
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

type nonEmptySetValidator struct{}

func (v nonEmptySetValidator) Description(ctx context.Context) string {
	return "set must contain at least one element"
}
func (v nonEmptySetValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v nonEmptySetValidator) ValidateSet(ctx context.Context, req frameworkvalidator.SetRequest, resp *frameworkvalidator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if len(req.ConfigValue.Elements()) == 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Empty Set", "Set must contain at least one element.")
	}
}
