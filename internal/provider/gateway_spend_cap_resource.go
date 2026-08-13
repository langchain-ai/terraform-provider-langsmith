package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	frameworkvalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var (
	_ resource.Resource                = &GatewaySpendCapResource{}
	_ resource.ResourceWithImportState = &GatewaySpendCapResource{}
)

var (
	spendCapWindows            = []string{"hourly", "daily", "weekly", "monthly"}
	spendCapSubjectMatcherKeys = []string{
		"organization_id",
		"workspace_id",
		"user_id",
		"api_key_id",
		"run_rule_id",
	}
)

func NewGatewaySpendCapResource() resource.Resource { return &GatewaySpendCapResource{} }

type GatewaySpendCapResource struct{ client *langsmith.Client }

type spendCapResourceModel struct {
	ID              types.String                       `tfsdk:"id"`
	Name            types.String                       `tfsdk:"name"`
	Description     types.String                       `tfsdk:"description"`
	SubjectMatchers []gatewayPolicySubjectMatcherModel `tfsdk:"subject_matchers"`
	Window          types.String                       `tfsdk:"window"`
	LimitUSD        types.Float64                      `tfsdk:"limit_usd"`
	Action          types.String                       `tfsdk:"action"`
	Priority        types.Int64                        `tfsdk:"priority"`
	Enabled         types.Bool                         `tfsdk:"enabled"`
	OrganizationID  types.String                       `tfsdk:"organization_id"`
	ParentPolicyID  types.String                       `tfsdk:"parent_policy_id"`
	CurrentSpendUSD types.Float64                      `tfsdk:"current_spend_usd"`
	CreatedAt       types.String                       `tfsdk:"created_at"`
	UpdatedAt       types.String                       `tfsdk:"updated_at"`
}

// spendCapConfig is the policy config for policy_type spend_cap.
type spendCapConfig struct {
	Window   string  `json:"window"`
	LimitUSD float64 `json:"limit_usd"`
}

type (
	spendCapCreatePayload = gatewayPolicyCreatePayload[spendCapConfig]
	spendCapUpdatePayload = gatewayPolicyUpdatePayload[spendCapConfig]
)

func (r *GatewaySpendCapResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gateway_spend_cap"
}

func (r *GatewaySpendCapResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	attributes := gatewayPolicyBaseAttributes("Spend cap policy")
	attributes["subject_matchers"] = schema.SetNestedAttribute{
		Required:            true,
		Validators:          []frameworkvalidator.Set{nonEmptySetValidator{}, spendCapSubjectMatchersValidator{}},
		MarkdownDescription: "Subject matchers that select which requests share this spend limit. All matchers must use the same key; multiple values for that key are ORed. At most " + strconv.Itoa(maxGatewayPolicySubjectMatchers) + " matchers.",
		NestedObject:        gatewayPolicySubjectMatcherNestedObject(spendCapSubjectMatcherKeys),
	}
	attributes["window"] = schema.StringAttribute{
		Required:            true,
		Validators:          []frameworkvalidator.String{oneOfStringValidator{values: spendCapWindows}},
		MarkdownDescription: "Spend window: `hourly`, `daily`, `weekly`, or `monthly`.",
	}
	attributes["limit_usd"] = schema.Float64Attribute{
		Required:            true,
		Validators:          []frameworkvalidator.Float64{positiveFloat64Validator{}},
		MarkdownDescription: "Maximum USD spend allowed in the selected window. Must be greater than 0; the API does not reject a zero or negative limit, which would block every request.",
	}
	attributes["parent_policy_id"] = schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: "Parent `default_spend_cap` ID when this policy was materialized from a default. Null for explicitly managed caps.",
	}
	attributes["current_spend_usd"] = schema.Float64Attribute{
		Computed:            true,
		MarkdownDescription: "Spend accumulated in the policy's active window, when available.",
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an organization-scoped LangSmith LLM Gateway spend cap policy (`spend_cap`).\n\n" +
			"Spend caps block gateway requests once accumulated LLM spend exceeds `limit_usd` within `window`. " +
			"This resource manages explicit per-subject caps, not `default_spend_cap` templates.\n\n" +
			"~> **Subject matchers are the identity of a cap.** The API upserts on them: creating a cap whose " +
			"`subject_matchers` already belong to another cap in the organization updates that cap in place and " +
			"returns its ID rather than creating a second one. Give every `langsmith_gateway_spend_cap` a distinct subject, " +
			"or two resources will manage the same policy and fight over it on every apply. An apply can likewise " +
			"adopt and overwrite a cap created outside Terraform.",
		Attributes: attributes,
	}
}

func (r *GatewaySpendCapResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *GatewaySpendCapResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan spendCapResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	conflict, err := r.findConflictingSpendCap(ctx, gatewayPolicyMatchersFromModel(plan.SubjectMatchers))
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Spend Cap", err.Error())
		return
	}
	if conflict != nil {
		if conflict.ParentPolicyID == nil {
			resp.Diagnostics.AddError("LangSmith Spend Cap Already Exists", fmt.Sprintf(
				"Policy %s (%q) already targets these subject matchers. The API upserts on subject "+
					"matchers, so creating this resource would overwrite that policy in place, and "+
					"destroying this resource later would delete it.\n\n"+
					"Import it instead:\n  terraform import langsmith_gateway_spend_cap.<name> %s",
				conflict.ID, conflict.Name, conflict.ID))
			return
		}
		resp.Diagnostics.AddWarning("Adopted a Materialized Spend Cap", fmt.Sprintf(
			"Policy %s was materialized for this subject from default spend cap %s, and Terraform "+
				"has taken it over. It is now detached, so edits to the default no longer reach it. "+
				"Destroying this resource returns the subject to the default, which materializes a "+
				"fresh policy on the next gateway request.",
			conflict.ID, *conflict.ParentPolicyID))
	}

	model, err := r.createSpendCap(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Spend Cap", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *GatewaySpendCapResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state spendCapResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.readSpendCap(ctx, state.ID.ValueString())
	if err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Spend Cap", err.Error())
		return
	}
	model = preserveSpendCapOptionalShape(model, state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *GatewaySpendCapResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state spendCapResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.updateSpendCap(ctx, state.ID.ValueString(), plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Spend Cap", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *GatewaySpendCapResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state spendCapResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.deleteSpendCap(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to Delete LangSmith Spend Cap", err.Error())
	}
}

func (r *GatewaySpendCapResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *GatewaySpendCapResource) createSpendCap(ctx context.Context, plan spendCapResourceModel) (spendCapResourceModel, error) {
	created, err := createGatewayPolicy(ctx, r.client, spendCapCreatePayloadFromModel(plan))
	if err != nil {
		return spendCapResourceModel{}, err
	}
	model, err := r.readSpendCap(ctx, created.ID)
	if err != nil {
		return spendCapResourceModel{}, err
	}
	return preserveSpendCapOptionalShape(model, plan), nil
}

func (r *GatewaySpendCapResource) findConflictingSpendCap(ctx context.Context, matchers []gatewayPolicySubjectMatcher) (*gatewayPolicyAPI, error) {
	return findConflictingGatewayPolicy(ctx, r.client, gatewayPolicyTypeSpendCap, matchers)
}

func (r *GatewaySpendCapResource) readSpendCap(ctx context.Context, id string) (spendCapResourceModel, error) {
	api, err := readGatewayPolicy(ctx, r.client, id, gatewayPolicyTypeSpendCap)
	if err != nil {
		return spendCapResourceModel{}, err
	}
	return spendCapModelFromAPI(api)
}

func (r *GatewaySpendCapResource) updateSpendCap(ctx context.Context, id string, plan spendCapResourceModel) (spendCapResourceModel, error) {
	if err := updateGatewayPolicy(ctx, r.client, id, spendCapUpdatePayloadFromModel(plan)); err != nil {
		return spendCapResourceModel{}, err
	}
	model, err := r.readSpendCap(ctx, id)
	if err != nil {
		return spendCapResourceModel{}, err
	}
	return preserveSpendCapOptionalShape(model, plan), nil
}

func (r *GatewaySpendCapResource) deleteSpendCap(ctx context.Context, id string) error {
	return deleteGatewayPolicy(ctx, r.client, id)
}

func spendCapCreatePayloadFromModel(model spendCapResourceModel) spendCapCreatePayload {
	return spendCapCreatePayload{
		Name:            model.Name.ValueString(),
		Description:     model.Description.ValueString(),
		SubjectMatchers: gatewayPolicyMatchersFromModel(model.SubjectMatchers),
		PolicyType:      gatewayPolicyTypeSpendCap,
		Config:          spendCapConfigFromModel(model),
		Action:          model.Action.ValueString(),
		Priority:        int(model.Priority.ValueInt64()),
		Enabled:         model.Enabled.ValueBool(),
	}
}

func spendCapUpdatePayloadFromModel(model spendCapResourceModel) spendCapUpdatePayload {
	return spendCapUpdatePayload{
		Name:            model.Name.ValueString(),
		Description:     model.Description.ValueString(),
		SubjectMatchers: gatewayPolicyMatchersFromModel(model.SubjectMatchers),
		Config:          spendCapConfigFromModel(model),
		Action:          model.Action.ValueString(),
		Priority:        int(model.Priority.ValueInt64()),
		Enabled:         model.Enabled.ValueBool(),
	}
}

func spendCapConfigFromModel(model spendCapResourceModel) spendCapConfig {
	return spendCapConfig{
		Window:   model.Window.ValueString(),
		LimitUSD: model.LimitUSD.ValueFloat64(),
	}
}

func spendCapModelFromAPI(api gatewayPolicyAPI) (spendCapResourceModel, error) {
	var config spendCapConfig
	if len(api.Config) > 0 {
		if err := json.Unmarshal(api.Config, &config); err != nil {
			return spendCapResourceModel{}, fmt.Errorf("decode spend cap config: %w", err)
		}
	}
	model := spendCapResourceModel{
		ID:              types.StringValue(api.ID),
		Name:            types.StringValue(api.Name),
		Description:     nullableStringPointer(api.Description),
		SubjectMatchers: gatewayPolicyMatcherModelsFromAPI(api.SubjectMatchers),
		Window:          types.StringValue(config.Window),
		LimitUSD:        types.Float64Value(config.LimitUSD),
		Action:          types.StringValue(api.Action),
		Priority:        types.Int64Value(int64(api.Priority)),
		Enabled:         types.BoolValue(api.Enabled),
		OrganizationID:  types.StringValue(api.OrganizationID),
		ParentPolicyID:  nullableStringPointer(api.ParentPolicyID),
		CreatedAt:       nullableString(api.CreatedAt),
		UpdatedAt:       nullableString(api.UpdatedAt),
		CurrentSpendUSD: types.Float64Null(),
	}
	if api.CurrentSpendUSD != nil {
		model.CurrentSpendUSD = types.Float64Value(*api.CurrentSpendUSD)
	}
	return model, nil
}

func preserveSpendCapOptionalShape(model, configured spendCapResourceModel) spendCapResourceModel {
	model.Description = gatewayPolicyDescriptionShape(model.Description, configured.Description)
	return model
}

type positiveFloat64Validator struct{}

func (v positiveFloat64Validator) Description(ctx context.Context) string {
	return "value must be greater than 0"
}
func (v positiveFloat64Validator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v positiveFloat64Validator) ValidateFloat64(ctx context.Context, req frameworkvalidator.Float64Request, resp *frameworkvalidator.Float64Response) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if req.ConfigValue.ValueFloat64() <= 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Value",
			fmt.Sprintf("Value must be greater than 0, got %v.", req.ConfigValue.ValueFloat64()))
	}
}

// spendCapSubjectMatchersValidator enforces at plan time what the API would
// otherwise reject at apply time: a cap targets at most
// maxGatewayPolicySubjectMatchers subjects, all under one key. Mixing built-in
// keys would AND them together, which is never what a shared spend limit means.
type spendCapSubjectMatchersValidator struct{}

func (v spendCapSubjectMatchersValidator) Description(ctx context.Context) string {
	return fmt.Sprintf("all matchers must share one key, and there must be at most %d of them", maxGatewayPolicySubjectMatchers)
}
func (v spendCapSubjectMatchersValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v spendCapSubjectMatchersValidator) ValidateSet(ctx context.Context, req frameworkvalidator.SetRequest, resp *frameworkvalidator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	elements := req.ConfigValue.Elements()
	if len(elements) > maxGatewayPolicySubjectMatchers {
		resp.Diagnostics.AddAttributeError(req.Path, "Too Many Subject Matchers",
			fmt.Sprintf("A spend cap targets at most %d subjects, got %d.", maxGatewayPolicySubjectMatchers, len(elements)))
		return
	}
	first := ""
	for _, element := range elements {
		object, ok := element.(types.Object)
		if !ok {
			continue
		}
		key, ok := object.Attributes()["key"].(types.String)
		if !ok || key.IsNull() || key.IsUnknown() {
			continue
		}
		if first == "" {
			first = key.ValueString()
			continue
		}
		if key.ValueString() != first {
			resp.Diagnostics.AddAttributeError(req.Path, "Mixed Subject Matcher Keys",
				fmt.Sprintf("All subject matchers must use the same key, got %q and %q. Use one resource per key.", first, key.ValueString()))
			return
		}
	}
}
