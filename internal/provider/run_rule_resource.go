package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

var (
	_ resource.Resource                = &RunRuleResource{}
	_ resource.ResourceWithImportState = &RunRuleResource{}
	_ resource.ResourceWithModifyPlan  = &RunRuleResource{}
)

func NewRunRuleResource() resource.Resource {
	return &RunRuleResource{}
}

type RunRuleResource struct {
	client *langsmith.Client
}

type runRuleModel struct {
	ID                           types.String       `tfsdk:"id"`
	WorkspaceID                  types.String       `tfsdk:"workspace_id"`
	TenantID                     types.String       `tfsdk:"tenant_id"`
	DisplayName                  types.String       `tfsdk:"display_name"`
	IsEnabled                    types.Bool         `tfsdk:"is_enabled"`
	SessionID                    types.String       `tfsdk:"session_id"`
	SessionName                  types.String       `tfsdk:"session_name"`
	DatasetID                    types.String       `tfsdk:"dataset_id"`
	DatasetName                  types.String       `tfsdk:"dataset_name"`
	SamplingRate                 types.Float64      `tfsdk:"sampling_rate"`
	Filter                       types.String       `tfsdk:"filter"`
	TraceFilter                  types.String       `tfsdk:"trace_filter"`
	TreeFilter                   types.String       `tfsdk:"tree_filter"`
	AddToAnnotationQueueID       types.String       `tfsdk:"add_to_annotation_queue_id"`
	AddToAnnotationQueueName     types.String       `tfsdk:"add_to_annotation_queue_name"`
	AddToDatasetID               types.String       `tfsdk:"add_to_dataset_id"`
	AddToDatasetName             types.String       `tfsdk:"add_to_dataset_name"`
	AddToDatasetPreferCorrection types.Bool         `tfsdk:"add_to_dataset_prefer_correction"`
	UseCorrectionsDataset        types.Bool         `tfsdk:"use_corrections_dataset"`
	NumFewShotExamples           types.Int64        `tfsdk:"num_few_shot_examples"`
	CorrectionsDatasetID         types.String       `tfsdk:"corrections_dataset_id"`
	EvaluatorID                  types.String       `tfsdk:"evaluator_id"`
	EvaluatorVersion             types.Int64        `tfsdk:"evaluator_version"`
	EvaluatorsJSON               types.String       `tfsdk:"evaluators_json"`
	CodeEvaluatorsJSON           types.String       `tfsdk:"code_evaluators_json"`
	Webhooks                     []runRuleWebhook   `tfsdk:"webhooks"`
	ExtendOnly                   types.Bool         `tfsdk:"extend_only"`
	Transient                    types.Bool         `tfsdk:"transient"`
	IncludeExtendedStats         types.Bool         `tfsdk:"include_extended_stats"`
	GroupBy                      types.String       `tfsdk:"group_by"`
	SpendLimit                   *runRuleSpendLimit `tfsdk:"spend_limit"`
	BackfillFrom                 types.String       `tfsdk:"backfill_from"`
	BackfillID                   types.String       `tfsdk:"backfill_id"`
	BackfillStatus               types.String       `tfsdk:"backfill_status"`
	BackfillProgress             types.Float64      `tfsdk:"backfill_progress"`
	BackfillError                types.String       `tfsdk:"backfill_error"`
	BackfillCompletedAt          types.String       `tfsdk:"backfill_completed_at"`
	CreateAlignmentQueue         types.Bool         `tfsdk:"create_alignment_queue"`
	AlignmentAnnotationQueueID   types.String       `tfsdk:"alignment_annotation_queue_id"`
	CreatedAt                    types.String       `tfsdk:"created_at"`
	UpdatedAt                    types.String       `tfsdk:"updated_at"`
	URLEnvFingerprint            types.String       `tfsdk:"url_env_fingerprint"`
}

type runRuleWebhook struct {
	URL         types.String `tfsdk:"url"`
	URLEnv      types.String `tfsdk:"url_env"`
	HeadersJSON types.String `tfsdk:"headers_json"`
}

type runRuleSpendLimit struct {
	LimitUSD types.Float64 `tfsdk:"limit_usd"`
	Window   types.String  `tfsdk:"window"`
}

// runRuleAPI mirrors smith-backend's RunRulesSchema / RunRulesBaseSchema.
// All fields that may be absent are pointers/nullable so they round-trip
// through encoding/json without leaking zero values into the request body.
type runRuleAPI struct {
	ID                           string              `json:"id,omitempty"`
	TenantID                     string              `json:"tenant_id,omitempty"`
	DisplayName                  string              `json:"display_name"`
	IsEnabled                    bool                `json:"is_enabled"`
	SessionID                    *string             `json:"session_id,omitempty"`
	SessionName                  *string             `json:"session_name,omitempty"`
	DatasetID                    *string             `json:"dataset_id,omitempty"`
	DatasetName                  *string             `json:"dataset_name,omitempty"`
	SamplingRate                 float64             `json:"sampling_rate"`
	Filter                       *string             `json:"filter,omitempty"`
	TraceFilter                  *string             `json:"trace_filter,omitempty"`
	TreeFilter                   *string             `json:"tree_filter,omitempty"`
	AddToAnnotationQueueID       *string             `json:"add_to_annotation_queue_id,omitempty"`
	AddToAnnotationQueueName     *string             `json:"add_to_annotation_queue_name,omitempty"`
	AddToDatasetID               *string             `json:"add_to_dataset_id,omitempty"`
	AddToDatasetName             *string             `json:"add_to_dataset_name,omitempty"`
	AddToDatasetPreferCorrection bool                `json:"add_to_dataset_prefer_correction"`
	UseCorrectionsDataset        bool                `json:"use_corrections_dataset"`
	NumFewShotExamples           *int64              `json:"num_few_shot_examples,omitempty"`
	CorrectionsDatasetID         *string             `json:"corrections_dataset_id,omitempty"`
	EvaluatorID                  *string             `json:"evaluator_id,omitempty"`
	EvaluatorVersion             *int64              `json:"evaluator_version,omitempty"`
	Evaluators                   []json.RawMessage   `json:"evaluators,omitempty"`
	CodeEvaluators               []json.RawMessage   `json:"code_evaluators,omitempty"`
	Webhooks                     []runRuleWebhookAPI `json:"webhooks,omitempty"`
	ExtendOnly                   bool                `json:"extend_only"`
	Transient                    bool                `json:"transient"`
	IncludeExtendedStats         bool                `json:"include_extended_stats"`
	GroupBy                      *string             `json:"group_by,omitempty"`
	SpendLimit                   *runRuleSpendAPI    `json:"spend_limit,omitempty"`
	BackfillFrom                 *string             `json:"backfill_from,omitempty"`
	BackfillID                   *string             `json:"backfill_id,omitempty"`
	BackfillStatus               *string             `json:"backfill_status,omitempty"`
	BackfillProgress             *float64            `json:"backfill_progress,omitempty"`
	BackfillError                *string             `json:"backfill_error,omitempty"`
	BackfillCompletedAt          *string             `json:"backfill_completed_at,omitempty"`
	CreateAlignmentQueue         *bool               `json:"create_alignment_queue,omitempty"`
	AlignmentAnnotationQueueID   *string             `json:"alignment_annotation_queue_id,omitempty"`
	CreatedAt                    string              `json:"created_at,omitempty"`
	UpdatedAt                    string              `json:"updated_at,omitempty"`
}

type runRuleWebhookAPI struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type runRuleSpendAPI struct {
	LimitUSD float64 `json:"limit_usd"`
	Window   string  `json:"window"`
}

func (r *RunRuleResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_run_rule"
}

func (r *RunRuleResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a LangSmith run rule (online evaluator / annotation queue / dataset routing).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Run rule ID.",
			},
			"workspace_id": schema.StringAttribute{
				Optional:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "LangSmith workspace (tenant) ID that owns this rule. When unset, the resource uses the workspace configured on the provider block.",
			},
			"tenant_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Workspace (tenant) ID that owns this rule, as returned by the API.",
			},
			"display_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Human-readable rule name.",
			},
			"is_enabled": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Whether the rule is currently active. Defaults to true.",
			},
			"session_id": schema.StringAttribute{
				Optional:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "Tracing project to attach this rule to. Exactly one of `session_id` or `dataset_id` must be set.",
			},
			"session_name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Tracing project name (resolved by the API).",
			},
			"dataset_id": schema.StringAttribute{
				Optional:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "Dataset to attach this rule to. Exactly one of `session_id` or `dataset_id` must be set.",
			},
			"dataset_name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Dataset name (resolved by the API).",
			},
			"sampling_rate": schema.Float64Attribute{
				Required:            true,
				MarkdownDescription: "Fraction of matching runs the rule applies to, in [0, 1].",
			},
			"filter": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Run-filter expression.",
			},
			"trace_filter": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Trace-level filter expression.",
			},
			"tree_filter": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Run-tree filter expression.",
			},
			"add_to_annotation_queue_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Annotation queue to push matching runs into.",
			},
			"add_to_annotation_queue_name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resolved annotation queue name.",
			},
			"add_to_dataset_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Dataset to add matching runs to.",
			},
			"add_to_dataset_name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resolved target dataset name.",
			},
			"add_to_dataset_prefer_correction": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "When true, use the corrected output (from a correction) when adding the run to the target dataset.",
			},
			"use_corrections_dataset": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Whether to write to a corrections dataset.",
			},
			"num_few_shot_examples": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Number of few-shot examples drawn from the corrections dataset.",
			},
			"corrections_dataset_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "ID of the corrections dataset (created or referenced by this rule's evaluator).",
			},
			"evaluator_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "ID of a saved `langsmith_evaluator` to attach to this rule.",
			},
			"evaluator_version": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Evaluator schema version. Defaults to the workspace's current version.",
				// Backend-owned and effectively immutable after create: the update API
				// routes on the sent version (at_least_v3 = version >= 3) and rejects a
				// mismatch with the stored value. Preserve the prior version on update
				// so it is echoed back in the PATCH; otherwise it replans as unknown,
				// gets dropped from the request, and evaluator reuse fails with
				// "Evaluator reuse is not supported for evaluator versions < 3."
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"evaluators_json": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "JSON-encoded list of inline LLM-as-judge evaluator definitions (each `{structured: {...}}`).",
			},
			"code_evaluators_json": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "JSON-encoded list of inline code-evaluator definitions (each `{code: ..., language: ...}`).",
			},
			"webhooks": schema.ListNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Webhooks invoked when the rule applies. Use `url_env` to source the URL from an environment variable so it stays out of Terraform state.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"url": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Webhook URL. Prefer `url_env` for secret webhook URLs.",
						},
						"url_env": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Environment variable containing a webhook URL. The value is sent to LangSmith but not stored in Terraform state.",
						},
						"headers_json": schema.StringAttribute{
							Optional:            true,
							Sensitive:           true,
							MarkdownDescription: "JSON-encoded `{header: value}` map sent with each webhook call. Values are redacted from Terraform plan/show output. Note that they are still written to the Terraform state file, so prefer state backends with at-rest encryption and access control.",
						},
					},
				},
			},
			"extend_only": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "When true, the rule only extends existing runs (no evaluator execution).",
			},
			"transient": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "When true, mark the rule as transient (one-shot).",
			},
			"include_extended_stats": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Include extended run stats (feedback, tokens, cost) in the evaluator input.",
			},
			"group_by": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional grouping. Only `thread_id` is currently supported.",
			},
			"spend_limit": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Per-rule spend limit for LLM-as-judge invocations.",
				Attributes: map[string]schema.Attribute{
					"limit_usd": schema.Float64Attribute{
						Required:            true,
						MarkdownDescription: "Maximum USD spend per window. Must be > 0.",
					},
					"window": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Spend window. Only `weekly` is currently supported.",
					},
				},
			},
			"backfill_from": schema.StringAttribute{
				Optional:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "RFC3339 timestamp; if set, backfill the rule over existing runs since this time. Only consulted at create time.",
			},
			"backfill_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Backfill job ID, if a backfill was scheduled.",
			},
			"backfill_status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Backfill status.",
			},
			"backfill_progress": schema.Float64Attribute{
				Computed:            true,
				MarkdownDescription: "Backfill progress, in [0, 1].",
			},
			"backfill_error": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Last backfill error, if any.",
			},
			"backfill_completed_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Backfill completion timestamp.",
			},
			"create_alignment_queue": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "When true, also create an alignment annotation queue for this rule. Only consulted at create time.",
			},
			"alignment_annotation_queue_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "ID of the alignment annotation queue, when one exists.",
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

func (r *RunRuleResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
// resolved URL is never stored in state (see modelFromRunRuleAPI), which
// otherwise means a rotation produces no plan diff and is never re-sent to
// LangSmith. A changed fingerprint triggers Update, which re-reads the env var.
func (r *RunRuleResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Nothing to fingerprint when the resource is being destroyed.
	if req.Plan.Raw.IsNull() {
		return
	}

	var webhooks []runRuleWebhook
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("webhooks"), &webhooks)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fp, hasURLEnv, resolved := urlEnvFingerprint(webhookURLEnvs(webhooks))
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

// webhookURLEnvs returns the url_env name for each webhook slot ("" when a
// webhook has no url_env), positionally aligned to the webhooks list.
func webhookURLEnvs(webhooks []runRuleWebhook) []string {
	names := make([]string, len(webhooks))
	for i, w := range webhooks {
		if !w.URLEnv.IsNull() && !w.URLEnv.IsUnknown() {
			names[i] = w.URLEnv.ValueString()
		}
	}
	return names
}

func (r *RunRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan runRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if d := validateRunRulePlan(plan); d != nil {
		resp.Diagnostics.Append(d...)
		return
	}

	payload, err := runRuleAPIFromModel(plan, true)
	if err != nil {
		resp.Diagnostics.AddError("Invalid LangSmith Run Rule", err.Error())
		return
	}

	var result runRuleAPI
	if err := r.client.Post(ctx, runRuleCollectionPath(), payload, &result, workspaceOpts(plan.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Run Rule", err.Error())
		return
	}
	if result.ID == "" {
		resp.Diagnostics.AddError("Unable to Create LangSmith Run Rule", "LangSmith did not return a rule ID.")
		return
	}

	next, diags := modelFromRunRuleAPI(result, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *RunRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state runRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	api, found, err := r.getRunRuleByID(ctx, state.ID.ValueString(), workspaceOpts(state.WorkspaceID))
	if err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Run Rule", err.Error())
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}

	next, diags := modelFromRunRuleAPI(api, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *RunRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan runRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state runRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.ID.IsNull() || plan.ID.IsUnknown() {
		plan.ID = state.ID
	}
	if d := validateRunRulePlan(plan); d != nil {
		resp.Diagnostics.Append(d...)
		return
	}

	payload, err := runRuleAPIFromModel(plan, true)
	if err != nil {
		resp.Diagnostics.AddError("Invalid LangSmith Run Rule", err.Error())
		return
	}

	var result runRuleAPI
	if err := r.client.Patch(ctx, runRuleResourcePath(plan.ID.ValueString()), payload, &result, workspaceOpts(plan.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Run Rule", err.Error())
		return
	}

	next, diags := modelFromRunRuleAPI(result, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *RunRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state runRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Delete(ctx, runRuleResourcePath(state.ID.ValueString()), nil, nil, workspaceOpts(state.WorkspaceID)...); err != nil {
		if isLangSmithNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to Delete LangSmith Run Rule", err.Error())
		return
	}
}

func (r *RunRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *RunRuleResource) getRunRuleByID(ctx context.Context, ruleID string, opts []option.RequestOption) (runRuleAPI, bool, error) {
	// smith-backend exposes list+filter but no single-rule GET; we use ?id=
	// and pick the only match.
	params := url.Values{}
	params.Set("id", ruleID)
	var list []runRuleAPI
	path := runRuleCollectionPath() + "?" + params.Encode()
	if err := r.client.Get(ctx, path, nil, &list, opts...); err != nil {
		return runRuleAPI{}, false, err
	}
	for _, rule := range list {
		if rule.ID == ruleID {
			return rule, true, nil
		}
	}
	return runRuleAPI{}, false, nil
}

func validateRunRulePlan(plan runRuleModel) diag.Diagnostics {
	var diags diag.Diagnostics
	hasSession := !plan.SessionID.IsNull() && !plan.SessionID.IsUnknown() && plan.SessionID.ValueString() != ""
	hasDataset := !plan.DatasetID.IsNull() && !plan.DatasetID.IsUnknown() && plan.DatasetID.ValueString() != ""
	if hasSession == hasDataset {
		diags.AddError(
			"Invalid LangSmith Run Rule",
			"Exactly one of `session_id` or `dataset_id` must be set.",
		)
	}
	if !plan.SamplingRate.IsNull() && !plan.SamplingRate.IsUnknown() {
		rate := plan.SamplingRate.ValueFloat64()
		if rate < 0 || rate > 1 {
			diags.AddError(
				"Invalid LangSmith Run Rule",
				fmt.Sprintf("`sampling_rate` must be in [0, 1], got %v.", rate),
			)
		}
	}
	if !plan.GroupBy.IsNull() && !plan.GroupBy.IsUnknown() {
		if v := plan.GroupBy.ValueString(); v != "" && v != "thread_id" {
			diags.AddError(
				"Invalid LangSmith Run Rule",
				fmt.Sprintf("`group_by` must be \"thread_id\" if set, got %q.", v),
			)
		}
	}
	if plan.SpendLimit != nil {
		if !plan.SpendLimit.Window.IsNull() && !plan.SpendLimit.Window.IsUnknown() {
			if w := plan.SpendLimit.Window.ValueString(); w != "weekly" {
				diags.AddError(
					"Invalid LangSmith Run Rule",
					fmt.Sprintf("`spend_limit.window` must be \"weekly\", got %q.", w),
				)
			}
		}
		if !plan.SpendLimit.LimitUSD.IsNull() && !plan.SpendLimit.LimitUSD.IsUnknown() {
			if plan.SpendLimit.LimitUSD.ValueFloat64() <= 0 {
				diags.AddError(
					"Invalid LangSmith Run Rule",
					"`spend_limit.limit_usd` must be > 0.",
				)
			}
		}
	}
	// smith-backend rejects a rule that both references a saved evaluator and supplies
	// inline evaluator definitions (HTTP 422). Fail fast here with a clearer message
	// instead of surfacing the raw API error at apply time.
	hasEvaluatorID := !plan.EvaluatorID.IsNull() && !plan.EvaluatorID.IsUnknown() && plan.EvaluatorID.ValueString() != ""
	hasInlineEvaluators := (!plan.EvaluatorsJSON.IsNull() && !plan.EvaluatorsJSON.IsUnknown() && plan.EvaluatorsJSON.ValueString() != "") ||
		(!plan.CodeEvaluatorsJSON.IsNull() && !plan.CodeEvaluatorsJSON.IsUnknown() && plan.CodeEvaluatorsJSON.ValueString() != "")
	if hasEvaluatorID && hasInlineEvaluators {
		diags.AddError(
			"Invalid LangSmith Run Rule",
			"Provide either `evaluator_id` or `evaluators_json`/`code_evaluators_json`, not both.",
		)
	}
	return diags
}

func runRuleAPIFromModel(data runRuleModel, resolveSecretURLs bool) (runRuleAPI, error) {
	out := runRuleAPI{
		ID:                           stringValue(data.ID),
		DisplayName:                  data.DisplayName.ValueString(),
		IsEnabled:                    boolWithDefault(data.IsEnabled, true),
		SamplingRate:                 data.SamplingRate.ValueFloat64(),
		AddToDatasetPreferCorrection: data.AddToDatasetPreferCorrection.ValueBool(),
		UseCorrectionsDataset:        data.UseCorrectionsDataset.ValueBool(),
		ExtendOnly:                   data.ExtendOnly.ValueBool(),
		Transient:                    data.Transient.ValueBool(),
		IncludeExtendedStats:         data.IncludeExtendedStats.ValueBool(),
	}
	out.SessionID = stringPtr(data.SessionID)
	out.DatasetID = stringPtr(data.DatasetID)
	out.Filter = stringPtr(data.Filter)
	out.TraceFilter = stringPtr(data.TraceFilter)
	out.TreeFilter = stringPtr(data.TreeFilter)
	out.AddToAnnotationQueueID = stringPtr(data.AddToAnnotationQueueID)
	out.AddToDatasetID = stringPtr(data.AddToDatasetID)
	out.NumFewShotExamples = intPtr(data.NumFewShotExamples)
	out.EvaluatorID = stringPtr(data.EvaluatorID)
	out.EvaluatorVersion = intPtr(data.EvaluatorVersion)
	out.GroupBy = stringPtr(data.GroupBy)
	out.BackfillFrom = stringPtr(data.BackfillFrom)
	out.CreateAlignmentQueue = boolPtr(data.CreateAlignmentQueue)

	if value := stringPtr(data.EvaluatorsJSON); value != nil {
		list, err := canonicalJSONList(*value, "evaluators_json")
		if err != nil {
			return runRuleAPI{}, err
		}
		out.Evaluators = list
	}
	if value := stringPtr(data.CodeEvaluatorsJSON); value != nil {
		list, err := canonicalJSONList(*value, "code_evaluators_json")
		if err != nil {
			return runRuleAPI{}, err
		}
		out.CodeEvaluators = list
	}

	if len(data.Webhooks) > 0 {
		out.Webhooks = make([]runRuleWebhookAPI, 0, len(data.Webhooks))
		for i, w := range data.Webhooks {
			webhook := runRuleWebhookAPI{}
			switch {
			case resolveSecretURLs && !w.URLEnv.IsNull() && w.URLEnv.ValueString() != "":
				envName := w.URLEnv.ValueString()
				v := strings.TrimSpace(os.Getenv(envName))
				if v == "" {
					return runRuleAPI{}, fmt.Errorf("environment variable %s is not set or is empty", envName)
				}
				webhook.URL = v
			case !w.URL.IsNull() && w.URL.ValueString() != "":
				webhook.URL = w.URL.ValueString()
			default:
				return runRuleAPI{}, fmt.Errorf("webhooks[%d]: either `url` or `url_env` is required", i)
			}
			if v := stringValue(w.HeadersJSON); v != "" {
				headers := map[string]string{}
				if err := json.Unmarshal([]byte(v), &headers); err != nil {
					return runRuleAPI{}, fmt.Errorf("webhooks[%d].headers_json must be a JSON object of strings: %w", i, err)
				}
				webhook.Headers = headers
			}
			out.Webhooks = append(out.Webhooks, webhook)
		}
	}

	if data.SpendLimit != nil {
		out.SpendLimit = &runRuleSpendAPI{
			LimitUSD: data.SpendLimit.LimitUSD.ValueFloat64(),
			Window:   data.SpendLimit.Window.ValueString(),
		}
	}

	return out, nil
}

func modelFromRunRuleAPI(api runRuleAPI, previous runRuleModel) (runRuleModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	next := previous
	next.ID = types.StringValue(api.ID)
	next.TenantID = optionalStringFromValue(api.TenantID)
	next.DisplayName = types.StringValue(api.DisplayName)
	next.IsEnabled = types.BoolValue(api.IsEnabled)
	next.SessionID = optionalStringFromPtr(api.SessionID)
	next.SessionName = optionalStringFromPtr(api.SessionName)
	next.DatasetID = optionalStringFromPtr(api.DatasetID)
	next.DatasetName = optionalStringFromPtr(api.DatasetName)
	next.SamplingRate = types.Float64Value(api.SamplingRate)
	next.Filter = optionalStringFromPtr(api.Filter)
	next.TraceFilter = optionalStringFromPtr(api.TraceFilter)
	next.TreeFilter = optionalStringFromPtr(api.TreeFilter)
	next.AddToAnnotationQueueID = optionalStringFromPtr(api.AddToAnnotationQueueID)
	next.AddToAnnotationQueueName = optionalStringFromPtr(api.AddToAnnotationQueueName)
	next.AddToDatasetID = optionalStringFromPtr(api.AddToDatasetID)
	next.AddToDatasetName = optionalStringFromPtr(api.AddToDatasetName)
	next.AddToDatasetPreferCorrection = types.BoolValue(api.AddToDatasetPreferCorrection)
	next.UseCorrectionsDataset = types.BoolValue(api.UseCorrectionsDataset)
	next.NumFewShotExamples = optionalIntFromPtr(api.NumFewShotExamples)
	next.CorrectionsDatasetID = optionalStringFromPtr(api.CorrectionsDatasetID)
	// smith-backend assigns a generated evaluator_id even to inline-evaluator rules
	// (where the config leaves evaluator_id unset). evaluator_id is Optional, not
	// Computed, so importing that generated id when the user didn't configure one
	// produces a "provider produced inconsistent result" error (planned null, got a
	// string). Only reflect the API value when the user actually set evaluator_id.
	if previous.EvaluatorID.IsNull() {
		next.EvaluatorID = types.StringNull()
	} else {
		next.EvaluatorID = optionalStringFromPtr(api.EvaluatorID)
	}
	next.EvaluatorVersion = optionalIntFromPtr(api.EvaluatorVersion)
	next.ExtendOnly = types.BoolValue(api.ExtendOnly)
	next.Transient = types.BoolValue(api.Transient)
	next.IncludeExtendedStats = types.BoolValue(api.IncludeExtendedStats)
	next.GroupBy = optionalStringFromPtr(api.GroupBy)
	next.BackfillFrom = optionalStringFromPtr(api.BackfillFrom)
	next.BackfillID = optionalStringFromPtr(api.BackfillID)
	next.BackfillStatus = optionalStringFromPtr(api.BackfillStatus)
	next.BackfillProgress = optionalFloatFromPtr(api.BackfillProgress)
	next.BackfillError = optionalStringFromPtr(api.BackfillError)
	next.BackfillCompletedAt = optionalStringFromPtr(api.BackfillCompletedAt)
	next.AlignmentAnnotationQueueID = optionalStringFromPtr(api.AlignmentAnnotationQueueID)
	next.CreatedAt = optionalStringFromValue(api.CreatedAt)
	next.UpdatedAt = optionalStringFromValue(api.UpdatedAt)

	// smith-backend always reports an `evaluator_id` and echoes the rule's evaluator
	// back inline under `evaluators` / `code_evaluators` — both for rules that
	// reference a saved evaluator (it resolves and echoes the code) and for rules
	// created from inline definitions (it auto-creates a backing evaluator and still
	// returns its generated id). The two cases are indistinguishable in the response,
	// so the returned evaluator_id is not a reliable signal.
	//
	// User intent lives in the prior config/state instead: when the inline attribute
	// was null (a saved-evaluator rule, or no inline config at all), keep it null
	// rather than importing the backend's echo — otherwise the resolved code is diffed
	// against the null config on every plan, a permanent diff. When the user did
	// configure an inline definition, surface the backend's canonical form as before.
	if previous.EvaluatorsJSON.IsNull() {
		next.EvaluatorsJSON = types.StringNull()
	} else {
		evList, err := canonicalJSONListString(api.Evaluators)
		switch {
		case err != nil:
			diags.AddError("Unable to encode LangSmith run rule", fmt.Sprintf("evaluators: %s", err))
		case evList != "":
			next.EvaluatorsJSON = types.StringValue(evList)
		default:
			next.EvaluatorsJSON = types.StringNull()
		}
	}
	if previous.CodeEvaluatorsJSON.IsNull() {
		next.CodeEvaluatorsJSON = types.StringNull()
	} else {
		codeList, err := canonicalJSONListString(api.CodeEvaluators)
		switch {
		case err != nil:
			diags.AddError("Unable to encode LangSmith run rule", fmt.Sprintf("code_evaluators: %s", err))
		case codeList != "":
			next.CodeEvaluatorsJSON = types.StringValue(codeList)
		default:
			next.CodeEvaluatorsJSON = types.StringNull()
		}
	}

	if len(api.Webhooks) > 0 {
		out := make([]runRuleWebhook, 0, len(api.Webhooks))
		for i, w := range api.Webhooks {
			model := runRuleWebhook{}
			// Preserve any previous `url_env` so we don't leak the resolved
			// URL into state; the same approach as alert_rule_resource.go.
			if i < len(previous.Webhooks) && !previous.Webhooks[i].URLEnv.IsNull() && previous.Webhooks[i].URLEnv.ValueString() != "" {
				model.URLEnv = previous.Webhooks[i].URLEnv
				model.URL = types.StringNull()
			} else {
				model.URL = types.StringValue(w.URL)
				model.URLEnv = types.StringNull()
			}
			if len(w.Headers) > 0 {
				raw, err := json.Marshal(w.Headers)
				if err != nil {
					diags.AddError("Unable to encode LangSmith run rule", fmt.Sprintf("webhooks[%d].headers: %s", i, err))
					continue
				}
				model.HeadersJSON = types.StringValue(string(raw))
			} else {
				model.HeadersJSON = types.StringNull()
			}
			out = append(out, model)
		}
		next.Webhooks = out
	} else {
		next.Webhooks = nil
	}

	if api.SpendLimit != nil {
		next.SpendLimit = &runRuleSpendLimit{
			LimitUSD: types.Float64Value(api.SpendLimit.LimitUSD),
			Window:   types.StringValue(api.SpendLimit.Window),
		}
	} else {
		next.SpendLimit = nil
	}

	// `create_alignment_queue` is a write-only field — the API doesn't echo it
	// back, so we keep whatever the user planned.
	if !previous.CreateAlignmentQueue.IsNull() && !previous.CreateAlignmentQueue.IsUnknown() {
		next.CreateAlignmentQueue = previous.CreateAlignmentQueue
	}

	return next, diags
}

func canonicalJSONList(raw string, field string) ([]json.RawMessage, error) {
	var items []any
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("%s must be a JSON array: %w", field, err)
	}
	out := make([]json.RawMessage, 0, len(items))
	for i, item := range items {
		b, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", field, i, err)
		}
		out = append(out, b)
	}
	return out, nil
}

func canonicalJSONListString(items []json.RawMessage) (string, error) {
	if len(items) == 0 {
		return "", nil
	}
	canonical := make([]any, 0, len(items))
	for _, raw := range items {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return "", err
		}
		canonical = append(canonical, v)
	}
	b, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func boolWithDefault(value types.Bool, def bool) bool {
	if value.IsNull() || value.IsUnknown() {
		return def
	}
	return value.ValueBool()
}

func runRuleCollectionPath() string {
	return "api/v1/runs/rules"
}

func runRuleResourcePath(ruleID string) string {
	return "api/v1/runs/rules/" + ruleID
}
