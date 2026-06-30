package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

var (
	_ resource.Resource                = &AlertRuleResource{}
	_ resource.ResourceWithImportState = &AlertRuleResource{}
	_ resource.ResourceWithModifyPlan  = &AlertRuleResource{}
)

func NewAlertRuleResource() resource.Resource {
	return &AlertRuleResource{}
}

type AlertRuleResource struct {
	client *langsmith.Client
}

type alertRuleModel struct {
	ID                     types.String       `tfsdk:"id"`
	WorkspaceID            types.String       `tfsdk:"workspace_id"`
	SessionID              types.String       `tfsdk:"session_id"`
	Name                   types.String       `tfsdk:"name"`
	Description            types.String       `tfsdk:"description"`
	Type                   types.String       `tfsdk:"type"`
	Aggregation            types.String       `tfsdk:"aggregation"`
	Attribute              types.String       `tfsdk:"attribute"`
	Operator               types.String       `tfsdk:"operator"`
	WindowMinutes          types.Int64        `tfsdk:"window_minutes"`
	Actions                []alertActionModel `tfsdk:"actions"`
	Filter                 types.String       `tfsdk:"filter"`
	DenominatorFilter      types.String       `tfsdk:"denominator_filter"`
	Threshold              types.Float64      `tfsdk:"threshold"`
	ThresholdMultiplier    types.Float64      `tfsdk:"threshold_multiplier"`
	ThresholdWindowMinutes types.Int64        `tfsdk:"threshold_window_minutes"`
	CreatedAt              types.String       `tfsdk:"created_at"`
	UpdatedAt              types.String       `tfsdk:"updated_at"`
	URLEnvFingerprint      types.String       `tfsdk:"url_env_fingerprint"`
}

type alertActionModel struct {
	Target     types.String `tfsdk:"target"`
	ConfigJSON types.String `tfsdk:"config_json"`
	URLEnv     types.String `tfsdk:"url_env"`
}

type alertRulePayload struct {
	Rule    alertRuleAPI     `json:"rule"`
	Actions []alertActionAPI `json:"actions"`
}

type alertRuleAPI struct {
	ID                     string   `json:"id,omitempty"`
	Name                   string   `json:"name"`
	Description            string   `json:"description"`
	Type                   string   `json:"type"`
	Attribute              string   `json:"attribute"`
	Aggregation            string   `json:"aggregation"`
	WindowMinutes          int64    `json:"window_minutes"`
	Operator               string   `json:"operator"`
	Threshold              *float64 `json:"threshold,omitempty"`
	ThresholdWindowMinutes *int64   `json:"threshold_window_minutes,omitempty"`
	ThresholdMultiplier    *float64 `json:"threshold_multiplier,omitempty"`
	Filter                 *string  `json:"filter,omitempty"`
	DenominatorFilter      *string  `json:"denominator_filter,omitempty"`
	CreatedAt              string   `json:"created_at,omitempty"`
	UpdatedAt              string   `json:"updated_at,omitempty"`
}

type alertActionAPI struct {
	ID          string          `json:"id,omitempty"`
	Target      string          `json:"target"`
	Config      json.RawMessage `json:"config"`
	AlertRuleID string          `json:"alert_rule_id,omitempty"`
	CreatedAt   string          `json:"created_at,omitempty"`
	UpdatedAt   string          `json:"updated_at,omitempty"`
}

func (r *AlertRuleResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_rule"
}

func (r *AlertRuleResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a LangSmith project alert rule.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Alert rule ID.",
			},
			"workspace_id": schema.StringAttribute{
				Optional:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "LangSmith workspace (tenant) ID that owns this rule. When unset, the resource uses the workspace configured on the provider block.",
			},
			"session_id": schema.StringAttribute{
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "LangSmith tracing project/session ID.",
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Alert rule name.",
			},
			"description": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Alert rule description.",
			},
			"type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Alert rule type, such as `threshold` or `change`.",
			},
			"aggregation": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Metric aggregation, such as `avg`, `sum`, or `pct`.",
			},
			"attribute": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Metric attribute to monitor.",
			},
			"operator": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Comparison operator.",
			},
			"window_minutes": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "Evaluation window in minutes.",
			},
			"actions": schema.ListNestedAttribute{
				Required:            true,
				MarkdownDescription: "Alert delivery actions. Use `url_env` for webhook URLs so they are sent to LangSmith without being stored in Terraform state.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"target": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Action target: `webhook`, `pagerduty`, or `dynatrace`.",
						},
						"config_json": schema.StringAttribute{
							Optional:            true,
							Computed:            true,
							MarkdownDescription: "JSON-encoded action config.",
						},
						"url_env": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Environment variable containing a webhook URL. The value is sent to LangSmith but not stored in Terraform state.",
						},
					},
				},
			},
			"filter": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Run filter expression.",
			},
			"denominator_filter": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Denominator filter for percentage aggregations.",
			},
			"threshold": schema.Float64Attribute{
				Optional:            true,
				MarkdownDescription: "Threshold for threshold alert rules.",
			},
			"threshold_multiplier": schema.Float64Attribute{
				Optional:            true,
				MarkdownDescription: "Multiplier for change alert rules.",
			},
			"threshold_window_minutes": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Comparison window for change alert rules.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Creation timestamp.",
			},
			"updated_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Last update timestamp.",
			},
			"url_env_fingerprint": schema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "Internal fingerprint of the webhook URL(s) resolved from `url_env`, used to detect out-of-band secret rotations so a changed URL triggers an update. This is a non-reversible digest, not the URL itself; the URL is never stored in state.",
			},
		},
	}
}

func (r *AlertRuleResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ModifyPlan records a fingerprint of the webhook URLs resolved from url_env so
// that rotating the secret behind that env var is detected as a change. The
// resolved URL is never stored in state (see modelFromAlertRuleResponse), which
// otherwise means a rotation produces no plan diff and is never re-sent to
// LangSmith. A changed fingerprint triggers Update, which re-reads the env var.
func (r *AlertRuleResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Nothing to fingerprint when the resource is being destroyed.
	if req.Plan.Raw.IsNull() {
		return
	}

	var actions []alertActionModel
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("actions"), &actions)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fp, hasURLEnv, resolved := urlEnvFingerprint(actionURLEnvs(actions))
	var value types.String
	switch {
	case !hasURLEnv:
		// No url_env-sourced webhook to track.
		value = types.StringNull()
	case !resolved:
		// The secret isn't available at plan time (e.g. a local plan without the
		// env var set). Keep the prior value so we don't force a spurious diff.
		if req.State.Raw.IsNull() {
			value = types.StringNull()
		} else {
			resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("url_env_fingerprint"), &value)...)
			if resp.Diagnostics.HasError() {
				return
			}
		}
	default:
		value = types.StringValue(fp)
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("url_env_fingerprint"), value)...)
}

// actionURLEnvs returns the url_env name for each action slot ("" when an action
// has no url_env), positionally aligned to the actions list.
func actionURLEnvs(actions []alertActionModel) []string {
	names := make([]string, len(actions))
	for i, a := range actions {
		if !a.URLEnv.IsNull() && !a.URLEnv.IsUnknown() {
			names[i] = a.URLEnv.ValueString()
		}
	}
	return names
}

func (r *AlertRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan alertRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	payload, err := alertRulePayloadFromModel(plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid LangSmith Alert Rule", err.Error())
		return
	}

	var result alertRulePayload
	opts := workspaceOpts(plan.WorkspaceID)
	if err := r.client.Post(ctx, alertRuleCollectionPath(plan.SessionID.ValueString()), payload, &result, opts...); err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Alert Rule", err.Error())
		return
	}
	if result.Rule.ID == "" {
		resp.Diagnostics.AddError("Unable to Create LangSmith Alert Rule", "LangSmith did not return an alert rule ID.")
		return
	}

	next, err := r.readAlertRule(ctx, plan.SessionID.ValueString(), result.Rule.ID, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Created LangSmith Alert Rule", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *AlertRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state alertRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var result alertRulePayload
	if err := r.client.Get(ctx, alertRuleResourcePath(state.SessionID.ValueString(), state.ID.ValueString()), nil, &result, workspaceOpts(state.WorkspaceID)...); err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Alert Rule", err.Error())
		return
	}

	next, err := modelFromAlertRuleResponse(result, state)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Decode LangSmith Alert Rule", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *AlertRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan alertRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state alertRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	nextPlan, sessionID, alertID, err := alertRuleIdentityForUpdate(plan, state)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Alert Rule", err.Error())
		return
	}

	payload, err := alertRulePayloadFromModel(nextPlan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid LangSmith Alert Rule", err.Error())
		return
	}

	if err := r.client.Patch(ctx, alertRuleResourcePath(sessionID, alertID), payload, nil, workspaceOpts(nextPlan.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Alert Rule", err.Error())
		return
	}

	next, err := r.readAlertRule(ctx, sessionID, alertID, nextPlan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Updated LangSmith Alert Rule", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *AlertRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state alertRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.Delete(ctx, alertRuleResourcePath(state.SessionID.ValueString(), state.ID.ValueString()), nil, nil, workspaceOpts(state.WorkspaceID)...); err != nil {
		if isLangSmithNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to Delete LangSmith Alert Rule", err.Error())
		return
	}
}

func (r *AlertRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	sessionID, alertID, ok := strings.Cut(req.ID, "/")
	if !ok || sessionID == "" || alertID == "" {
		resp.Diagnostics.AddError("Invalid Import ID", "Use import ID format `<session_id>/<alert_rule_id>`.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("session_id"), sessionID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), alertID)...)
}

func (r *AlertRuleResource) readAlertRule(ctx context.Context, sessionID string, alertID string, previous alertRuleModel) (alertRuleModel, error) {
	var result alertRulePayload
	if err := r.client.Get(ctx, alertRuleResourcePath(sessionID, alertID), nil, &result, workspaceOpts(previous.WorkspaceID)...); err != nil {
		return alertRuleModel{}, err
	}
	return modelFromAlertRuleResponse(result, previous)
}

func alertRuleIdentityForUpdate(plan alertRuleModel, state alertRuleModel) (alertRuleModel, string, string, error) {
	sessionID := stringValue(state.SessionID)
	if sessionID == "" {
		sessionID = stringValue(plan.SessionID)
	}
	if sessionID == "" {
		return alertRuleModel{}, "", "", errors.New("missing LangSmith project/session ID in plan and state")
	}

	alertID := stringValue(state.ID)
	if alertID == "" {
		alertID = stringValue(plan.ID)
	}
	if alertID == "" {
		return alertRuleModel{}, "", "", errors.New("missing LangSmith alert rule ID in plan and state")
	}

	plan.SessionID = types.StringValue(sessionID)
	plan.ID = types.StringValue(alertID)
	return plan, sessionID, alertID, nil
}

func alertRulePayloadFromModel(data alertRuleModel) (alertRulePayload, error) {
	rule := alertRuleAPI{
		ID:            stringValue(data.ID),
		Name:          data.Name.ValueString(),
		Description:   data.Description.ValueString(),
		Type:          data.Type.ValueString(),
		Attribute:     data.Attribute.ValueString(),
		Aggregation:   data.Aggregation.ValueString(),
		WindowMinutes: data.WindowMinutes.ValueInt64(),
		Operator:      data.Operator.ValueString(),
	}
	if value := stringPtr(data.Filter); value != nil {
		rule.Filter = value
	}
	if value := stringPtr(data.DenominatorFilter); value != nil {
		rule.DenominatorFilter = value
	}
	if value := floatPtr(data.Threshold); value != nil {
		rule.Threshold = value
	}
	if value := floatPtr(data.ThresholdMultiplier); value != nil {
		rule.ThresholdMultiplier = value
	}
	if value := intPtr(data.ThresholdWindowMinutes); value != nil {
		rule.ThresholdWindowMinutes = value
	}

	actions := make([]alertActionAPI, 0, len(data.Actions))
	for _, action := range data.Actions {
		config := map[string]any{}
		if !action.ConfigJSON.IsNull() && action.ConfigJSON.ValueString() != "" {
			if err := json.Unmarshal([]byte(action.ConfigJSON.ValueString()), &config); err != nil {
				return alertRulePayload{}, fmt.Errorf("actions config_json must be a JSON object: %w", err)
			}
		}
		if !action.URLEnv.IsNull() && action.URLEnv.ValueString() != "" {
			url := strings.TrimSpace(os.Getenv(action.URLEnv.ValueString()))
			if url == "" {
				return alertRulePayload{}, fmt.Errorf("environment variable %s is not set or is empty", action.URLEnv.ValueString())
			}
			config["url"] = url
		}
		configJSON, err := encodeActionConfigForAPI(config)
		if err != nil {
			return alertRulePayload{}, fmt.Errorf("marshal action config: %w", err)
		}
		actions = append(actions, alertActionAPI{
			Target: action.Target.ValueString(),
			Config: configJSON,
		})
	}

	return alertRulePayload{Rule: rule, Actions: actions}, nil
}

func modelFromAlertRuleResponse(result alertRulePayload, previous alertRuleModel) (alertRuleModel, error) {
	next := previous
	next.ID = types.StringValue(result.Rule.ID)
	next.Name = types.StringValue(result.Rule.Name)
	next.Description = types.StringValue(result.Rule.Description)
	next.Type = types.StringValue(result.Rule.Type)
	next.Attribute = types.StringValue(result.Rule.Attribute)
	next.Aggregation = types.StringValue(result.Rule.Aggregation)
	next.WindowMinutes = types.Int64Value(result.Rule.WindowMinutes)
	next.Operator = types.StringValue(result.Rule.Operator)
	next.Filter = optionalStringFromPtr(result.Rule.Filter)
	next.DenominatorFilter = optionalStringFromPtr(result.Rule.DenominatorFilter)
	next.Threshold = optionalFloatFromPtr(result.Rule.Threshold)
	next.ThresholdMultiplier = optionalFloatFromPtr(result.Rule.ThresholdMultiplier)
	next.ThresholdWindowMinutes = optionalIntFromPtr(result.Rule.ThresholdWindowMinutes)
	next.CreatedAt = optionalStringFromValue(result.Rule.CreatedAt)
	next.UpdatedAt = optionalStringFromValue(result.Rule.UpdatedAt)

	actions := make([]alertActionModel, 0, len(result.Actions))
	for i, action := range result.Actions {
		config, err := decodeActionConfigFromAPI(action.Config)
		if err != nil {
			return alertRuleModel{}, fmt.Errorf("decode action config: %w", err)
		}

		urlEnv := types.StringNull()
		if i < len(previous.Actions) && !previous.Actions[i].URLEnv.IsNull() {
			urlEnv = previous.Actions[i].URLEnv
			delete(config, "url")
		}

		configJSON, err := json.Marshal(config)
		if err != nil {
			return alertRuleModel{}, fmt.Errorf("marshal action config: %w", err)
		}
		configValue := types.StringNull()
		if len(config) > 0 {
			configValue = types.StringValue(string(configJSON))
		}

		actions = append(actions, alertActionModel{
			Target:     types.StringValue(action.Target),
			ConfigJSON: configValue,
			URLEnv:     urlEnv,
		})
	}
	next.Actions = actions
	return next, nil
}

func alertRuleCollectionPath(sessionID string) string {
	return fmt.Sprintf("v1/platform/alerts/%s", sessionID)
}

func alertRuleResourcePath(sessionID string, alertID string) string {
	return fmt.Sprintf("v1/platform/alerts/%s/%s", sessionID, alertID)
}

func encodeActionConfigForAPI(config map[string]any) (json.RawMessage, error) {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}

	wireJSON, err := json.Marshal(string(configJSON))
	if err != nil {
		return nil, err
	}
	return wireJSON, nil
}

func decodeActionConfigFromAPI(raw json.RawMessage) (map[string]any, error) {
	config := map[string]any{}
	if len(raw) == 0 || string(raw) == "null" {
		return config, nil
	}

	var configJSON []byte
	if raw[0] == '"' {
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return nil, err
		}
		if encoded == "" {
			return config, nil
		}
		configJSON = []byte(encoded)
	} else {
		configJSON = raw
	}

	if err := json.Unmarshal(configJSON, &config); err != nil {
		return nil, err
	}
	return config, nil
}

func stringValue(value types.String) string {
	if value.IsNull() || value.IsUnknown() {
		return ""
	}
	return value.ValueString()
}

func stringPtr(value types.String) *string {
	if value.IsNull() || value.IsUnknown() {
		return nil
	}
	v := value.ValueString()
	return &v
}

func floatPtr(value types.Float64) *float64 {
	if value.IsNull() || value.IsUnknown() {
		return nil
	}
	v := value.ValueFloat64()
	return &v
}

func intPtr(value types.Int64) *int64 {
	if value.IsNull() || value.IsUnknown() {
		return nil
	}
	v := value.ValueInt64()
	return &v
}

func optionalStringFromPtr(value *string) types.String {
	if value == nil {
		return types.StringNull()
	}
	return types.StringValue(*value)
}

func optionalStringFromValue(value string) types.String {
	if value == "" {
		return types.StringNull()
	}
	return types.StringValue(value)
}

func optionalFloatFromPtr(value *float64) types.Float64 {
	if value == nil {
		return types.Float64Null()
	}
	return types.Float64Value(*value)
}

func optionalIntFromPtr(value *int64) types.Int64 {
	if value == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*value)
}

func isLangSmithNotFound(err error) bool {
	var apiErr *langsmith.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == 404
}

// workspaceOpts returns a per-request override for the LangSmith workspace
// (tenant_id) when the resource has a workspace_id attribute set. When unset,
// the SDK falls back to the workspace configured on the provider block.
func workspaceOpts(workspaceID types.String) []option.RequestOption {
	if workspaceID.IsNull() || workspaceID.IsUnknown() {
		return nil
	}
	v := strings.TrimSpace(workspaceID.ValueString())
	if v == "" {
		return nil
	}
	return []option.RequestOption{option.WithTenantID(v)}
}

func boolPtr(value types.Bool) *bool {
	if value.IsNull() || value.IsUnknown() {
		return nil
	}
	v := value.ValueBool()
	return &v
}

func optionalBoolFromPtr(value *bool) types.Bool {
	if value == nil {
		return types.BoolNull()
	}
	return types.BoolValue(*value)
}

// canonicalJSONObject parses the given JSON, requires it to be a JSON object,
// and re-marshals it so semantically equivalent inputs produce byte-identical
// state values. Without this, key reordering on the wire would surface as a
// spurious Terraform diff on every refresh.
func canonicalJSONObject(raw string, field string) (string, error) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return "", fmt.Errorf("%s must be a JSON object: %w", field, err)
	}
	out, err := json.Marshal(parsed)
	if err != nil {
		return "", fmt.Errorf("marshal %s: %w", field, err)
	}
	return string(out), nil
}
