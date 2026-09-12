package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var (
	_ resource.Resource                = &EvaluatorResource{}
	_ resource.ResourceWithImportState = &EvaluatorResource{}
)

const (
	evaluatorTypeLLM  = "llm"
	evaluatorTypeCode = "code"
)

func NewEvaluatorResource() resource.Resource {
	return &EvaluatorResource{}
}

type EvaluatorResource struct {
	client *langsmith.Client
}

type evaluatorModel struct {
	ID                      types.String        `tfsdk:"id"`
	WorkspaceID             types.String        `tfsdk:"workspace_id"`
	Name                    types.String        `tfsdk:"name"`
	Type                    types.String        `tfsdk:"type"`
	LLMEvaluator            *llmEvaluatorModel  `tfsdk:"llm_evaluator"`
	CodeEvaluator           *codeEvaluatorModel `tfsdk:"code_evaluator"`
	FeedbackKeys            types.List          `tfsdk:"feedback_keys"`
	DeleteRunRulesOnDestroy types.Bool          `tfsdk:"delete_run_rules_on_destroy"`
	CreatedAt               types.String        `tfsdk:"created_at"`
	UpdatedAt               types.String        `tfsdk:"updated_at"`
}

type llmEvaluatorModel struct {
	PromptRepoHandle      types.String `tfsdk:"prompt_repo_handle"`
	VariableMappingJSON   types.String `tfsdk:"variable_mapping_json"`
	CommitHashOrTag       types.String `tfsdk:"commit_hash_or_tag"`
	UseCorrectionsDataset types.Bool   `tfsdk:"use_corrections_dataset"`
	NumFewShotExamples    types.Int64  `tfsdk:"num_few_shot_examples"`
}

type codeEvaluatorModel struct {
	Code     types.String `tfsdk:"code"`
	Language types.String `tfsdk:"language"`
}

type evaluatorAPI struct {
	ID            string            `json:"id,omitempty"`
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	FeedbackKeys  []string          `json:"feedback_keys,omitempty"`
	LLMEvaluator  *llmEvaluatorAPI  `json:"llm_evaluator,omitempty"`
	CodeEvaluator *codeEvaluatorAPI `json:"code_evaluator,omitempty"`
	CreatedAt     string            `json:"created_at,omitempty"`
	UpdatedAt     string            `json:"updated_at,omitempty"`
}

type llmEvaluatorAPI struct {
	PromptRepoHandle      *string          `json:"prompt_repo_handle,omitempty"`
	VariableMapping       *json.RawMessage `json:"variable_mapping,omitempty"`
	CommitHashOrTag       *string          `json:"commit_hash_or_tag,omitempty"`
	UseCorrectionsDataset *bool            `json:"use_corrections_dataset,omitempty"`
	NumFewShotExamples    *int64           `json:"num_few_shot_examples,omitempty"`
}

type codeEvaluatorAPI struct {
	Code     string `json:"code"`
	Language string `json:"language,omitempty"`
}

type createEvaluatorRequest struct {
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	LLMEvaluator  *llmEvaluatorAPI  `json:"llm_evaluator,omitempty"`
	CodeEvaluator *codeEvaluatorAPI `json:"code_evaluator,omitempty"`
}

type updateEvaluatorRequest struct {
	Name          *string           `json:"name,omitempty"`
	LLMEvaluator  *llmEvaluatorAPI  `json:"llm_evaluator,omitempty"`
	CodeEvaluator *codeEvaluatorAPI `json:"code_evaluator,omitempty"`
}

type evaluatorEnvelope struct {
	Evaluator evaluatorAPI `json:"evaluator"`
}

func (r *EvaluatorResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_evaluator"
}

func (r *EvaluatorResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a LangSmith evaluator (LLM-as-judge or code).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Evaluator ID.",
			},
			"workspace_id": schema.StringAttribute{
				Optional:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "LangSmith workspace (tenant) ID that owns this evaluator. When unset, the resource uses the workspace configured on the provider block.",
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Evaluator name.",
			},
			"type": schema.StringAttribute{
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "Evaluator type: `llm` or `code`. Cannot be changed after creation.",
			},
			"llm_evaluator": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "LLM-as-judge configuration. Required when `type = \"llm\"`.",
				Attributes: map[string]schema.Attribute{
					"prompt_repo_handle": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Prompt Hub repo handle (e.g. `owner/prompt`).",
					},
					"variable_mapping_json": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "JSON-encoded `{prompt_var: run_field_path}` mapping.",
					},
					"commit_hash_or_tag": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Prompt commit hash or tag.",
					},
					"use_corrections_dataset": schema.BoolAttribute{
						Optional:            true,
						MarkdownDescription: "Whether the evaluator's run rules should write to a corrections dataset.",
					},
					"num_few_shot_examples": schema.Int64Attribute{
						Optional:            true,
						MarkdownDescription: "Number of few-shot examples to include from the corrections dataset.",
					},
				},
			},
			"code_evaluator": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Code evaluator configuration. Required when `type = \"code\"`.",
				Attributes: map[string]schema.Attribute{
					"code": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Evaluator source code.",
					},
					"language": schema.StringAttribute{
						Optional:            true,
						Computed:            true,
						MarkdownDescription: "Source language: `python` (default) or `javascript`.",
					},
				},
			},
			"feedback_keys": schema.ListAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Feedback keys this evaluator emits, populated by the API.",
			},
			"delete_run_rules_on_destroy": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "When true, `terraform destroy` will cascade-delete any run rules that reference this evaluator. Defaults to false, in which case destroy fails with HTTP 409 if rules still exist.",
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
		},
	}
}

func (r *EvaluatorResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *EvaluatorResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan evaluatorModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	payload, err := createEvaluatorPayloadFromModel(plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid LangSmith Evaluator", err.Error())
		return
	}

	var result evaluatorEnvelope
	if err := r.client.Post(ctx, evaluatorCollectionPath(), payload, &result, workspaceOpts(plan.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Evaluator", err.Error())
		return
	}
	if result.Evaluator.ID == "" {
		resp.Diagnostics.AddError("Unable to Create LangSmith Evaluator", "LangSmith did not return an evaluator ID.")
		return
	}

	next, diags := modelFromEvaluatorAPI(ctx, result.Evaluator, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *EvaluatorResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state evaluatorModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var result evaluatorAPI
	if err := r.client.Get(ctx, evaluatorResourcePath(state.ID.ValueString()), nil, &result, workspaceOpts(state.WorkspaceID)...); err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Evaluator", err.Error())
		return
	}

	next, diags := modelFromEvaluatorAPI(ctx, result, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *EvaluatorResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan evaluatorModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state evaluatorModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.ID.IsNull() || plan.ID.IsUnknown() {
		plan.ID = state.ID
	}

	payload, err := updateEvaluatorPayloadFromModel(plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid LangSmith Evaluator", err.Error())
		return
	}

	var result evaluatorEnvelope
	if err := r.client.Patch(ctx, evaluatorResourcePath(plan.ID.ValueString()), payload, &result, workspaceOpts(plan.WorkspaceID)...); err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Evaluator", err.Error())
		return
	}

	next, diags := modelFromEvaluatorAPI(ctx, result.Evaluator, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *EvaluatorResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state evaluatorModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	path := evaluatorResourcePath(state.ID.ValueString())
	if state.DeleteRunRulesOnDestroy.ValueBool() {
		path += "?delete_run_rules=true"
	}

	if err := r.client.Delete(ctx, path, nil, nil, workspaceOpts(state.WorkspaceID)...); err != nil {
		if isLangSmithNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to Delete LangSmith Evaluator", err.Error())
		return
	}
}

func (r *EvaluatorResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func createEvaluatorPayloadFromModel(data evaluatorModel) (createEvaluatorRequest, error) {
	evalType := data.Type.ValueString()
	switch evalType {
	case evaluatorTypeLLM:
		if data.LLMEvaluator == nil {
			return createEvaluatorRequest{}, fmt.Errorf("`llm_evaluator` block is required when type is %q", evaluatorTypeLLM)
		}
		if data.CodeEvaluator != nil {
			return createEvaluatorRequest{}, fmt.Errorf("`code_evaluator` block is not allowed when type is %q", evaluatorTypeLLM)
		}
		llm, err := llmEvaluatorAPIFromModel(*data.LLMEvaluator)
		if err != nil {
			return createEvaluatorRequest{}, err
		}
		return createEvaluatorRequest{
			Name:         data.Name.ValueString(),
			Type:         evalType,
			LLMEvaluator: llm,
		}, nil
	case evaluatorTypeCode:
		if data.CodeEvaluator == nil {
			return createEvaluatorRequest{}, fmt.Errorf("`code_evaluator` block is required when type is %q", evaluatorTypeCode)
		}
		if data.LLMEvaluator != nil {
			return createEvaluatorRequest{}, fmt.Errorf("`llm_evaluator` block is not allowed when type is %q", evaluatorTypeCode)
		}
		return createEvaluatorRequest{
			Name:          data.Name.ValueString(),
			Type:          evalType,
			CodeEvaluator: codeEvaluatorAPIFromModel(*data.CodeEvaluator),
		}, nil
	default:
		return createEvaluatorRequest{}, fmt.Errorf("invalid evaluator type %q (must be %q or %q)", evalType, evaluatorTypeLLM, evaluatorTypeCode)
	}
}

func updateEvaluatorPayloadFromModel(data evaluatorModel) (updateEvaluatorRequest, error) {
	out := updateEvaluatorRequest{}
	if !data.Name.IsNull() && !data.Name.IsUnknown() {
		v := data.Name.ValueString()
		out.Name = &v
	}
	switch data.Type.ValueString() {
	case evaluatorTypeLLM:
		if data.LLMEvaluator == nil {
			return updateEvaluatorRequest{}, fmt.Errorf("`llm_evaluator` block is required when type is %q", evaluatorTypeLLM)
		}
		llm, err := llmEvaluatorAPIFromModel(*data.LLMEvaluator)
		if err != nil {
			return updateEvaluatorRequest{}, err
		}
		out.LLMEvaluator = llm
	case evaluatorTypeCode:
		if data.CodeEvaluator == nil {
			return updateEvaluatorRequest{}, fmt.Errorf("`code_evaluator` block is required when type is %q", evaluatorTypeCode)
		}
		out.CodeEvaluator = codeEvaluatorAPIFromModel(*data.CodeEvaluator)
	}
	return out, nil
}

func llmEvaluatorAPIFromModel(data llmEvaluatorModel) (*llmEvaluatorAPI, error) {
	out := &llmEvaluatorAPI{}
	if value := stringPtr(data.PromptRepoHandle); value != nil {
		out.PromptRepoHandle = value
	}
	if value := stringPtr(data.CommitHashOrTag); value != nil {
		out.CommitHashOrTag = value
	}
	if !data.UseCorrectionsDataset.IsNull() && !data.UseCorrectionsDataset.IsUnknown() {
		v := data.UseCorrectionsDataset.ValueBool()
		out.UseCorrectionsDataset = &v
	}
	if !data.NumFewShotExamples.IsNull() && !data.NumFewShotExamples.IsUnknown() {
		v := data.NumFewShotExamples.ValueInt64()
		out.NumFewShotExamples = &v
	}
	if value := stringPtr(data.VariableMappingJSON); value != nil {
		canonical, err := canonicalJSONObject(*value, "variable_mapping_json")
		if err != nil {
			return nil, err
		}
		raw := json.RawMessage(canonical)
		out.VariableMapping = &raw
	}
	return out, nil
}

func codeEvaluatorAPIFromModel(data codeEvaluatorModel) *codeEvaluatorAPI {
	out := &codeEvaluatorAPI{Code: data.Code.ValueString()}
	if value := stringValue(data.Language); value != "" {
		out.Language = value
	}
	return out
}

func modelFromEvaluatorAPI(ctx context.Context, api evaluatorAPI, previous evaluatorModel) (evaluatorModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	next := previous
	next.ID = types.StringValue(api.ID)
	next.Name = types.StringValue(api.Name)
	next.Type = types.StringValue(api.Type)
	next.CreatedAt = optionalStringFromValue(api.CreatedAt)
	next.UpdatedAt = optionalStringFromValue(api.UpdatedAt)

	keys := api.FeedbackKeys
	if keys == nil {
		keys = []string{}
	}
	feedbackList, d := types.ListValueFrom(ctx, types.StringType, keys)
	diags.Append(d...)
	next.FeedbackKeys = feedbackList

	if api.LLMEvaluator != nil {
		mapping := types.StringNull()
		if api.LLMEvaluator.VariableMapping != nil {
			raw := string(*api.LLMEvaluator.VariableMapping)
			canonical, err := canonicalJSONObject(raw, "variable_mapping")
			if err == nil {
				mapping = types.StringValue(canonical)
			} else {
				// Don't silently drop to null — that would surface as a
				// perpetual diff vs. the configured value. Surface the parse
				// failure and persist the raw API value so the operator can
				// reconcile it.
				diags.AddWarning(
					"Unable to canonicalize LangSmith evaluator variable_mapping",
					fmt.Sprintf("Persisting the raw API value as-is: %s", err),
				)
				mapping = types.StringValue(raw)
			}
		}
		next.LLMEvaluator = &llmEvaluatorModel{
			PromptRepoHandle:      optionalStringFromPtr(api.LLMEvaluator.PromptRepoHandle),
			VariableMappingJSON:   mapping,
			CommitHashOrTag:       optionalStringFromPtr(api.LLMEvaluator.CommitHashOrTag),
			UseCorrectionsDataset: optionalBoolFromPtr(api.LLMEvaluator.UseCorrectionsDataset),
			NumFewShotExamples:    optionalIntFromPtr(api.LLMEvaluator.NumFewShotExamples),
		}
	} else {
		next.LLMEvaluator = nil
	}

	if api.CodeEvaluator != nil {
		next.CodeEvaluator = &codeEvaluatorModel{
			Code:     types.StringValue(api.CodeEvaluator.Code),
			Language: types.StringValue(api.CodeEvaluator.Language),
		}
	} else {
		next.CodeEvaluator = nil
	}

	return next, diags
}

func evaluatorCollectionPath() string {
	return "api/v1/platform/evaluators"
}

func evaluatorResourcePath(id string) string {
	return "api/v1/platform/evaluators/" + id
}
