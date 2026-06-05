package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// useStateForUnknownDescription is the canonical description emitted by
// {int64,string}planmodifier.UseStateForUnknown(). We match on it to assert the
// right plan modifier is wired up without depending on framework internals.
const useStateForUnknownDescription = "Once set, the value of this attribute in state will not change."

// TestRunRuleEvaluatorVersionUsesStateForUnknown is a regression test for the
// run-rule apply failure:
//
//	Error: Unable to Update LangSmith Run Rule
//	PATCH .../api/v1/runs/rules/<id>: 400 Bad Request
//	{"detail":"Evaluator reuse is not supported for evaluator versions < 3."}
//
// evaluator_version is Optional+Computed. Without a plan modifier, any in-place
// update replans it as unknown ("known after apply"), the provider converts the
// unknown to nil (intPtr) and drops it from the PATCH body (omitempty). The
// backend then sees evaluator_version=null, computes at_least_v3 = (None or 0) >= 3
// = false, routes to the legacy update path, and rejects the evaluator_id reuse.
//
// UseStateForUnknown carries the prior version (e.g. 3) into the plan so it is
// sent in the PATCH, routing the backend to update_rule_v3 where reuse is allowed.
func TestRunRuleEvaluatorVersionUsesStateForUnknown(t *testing.T) {
	ctx := context.Background()

	var resp resource.SchemaResponse
	(&RunRuleResource{}).Schema(ctx, resource.SchemaRequest{}, &resp)

	raw, ok := resp.Schema.Attributes["evaluator_version"]
	if !ok {
		t.Fatal("evaluator_version attribute missing from run rule schema")
	}
	attr, ok := raw.(schema.Int64Attribute)
	if !ok {
		t.Fatalf("evaluator_version is %T, want schema.Int64Attribute", raw)
	}
	if !attr.Optional || !attr.Computed {
		t.Fatalf("evaluator_version Optional=%v Computed=%v, want both true (precondition for UseStateForUnknown)", attr.Optional, attr.Computed)
	}

	mods := attr.Int64PlanModifiers()
	for _, m := range mods {
		if m.Description(ctx) == useStateForUnknownDescription {
			return // found it
		}
	}
	t.Fatalf("evaluator_version is missing a UseStateForUnknown plan modifier (have %d modifiers); without it, in-place updates replan it as unknown and the provider drops it from the PATCH, breaking evaluator reuse", len(mods))
}

// TestRunRuleInlineEvaluatorsFollowPriorState is a regression test for a permanent
// diff on rules that attach a saved evaluator via evaluator_id.
//
// smith-backend always returns an evaluator_id (it auto-creates a backing evaluator
// even for inline rules) and echoes the evaluator back inline under code_evaluators /
// evaluators. Importing that echo into code_evaluators_json / evaluators_json when the
// config left them null produced a permanent diff:
//
//	~ resource "langsmith_run_rule" "commands" {
//	  - code_evaluators_json = jsonencode([...]) -> null
//	  }
//
// Because the response is identical for saved-evaluator and inline rules, the returned
// evaluator_id can't be the signal. modelFromRunRuleAPI must key off the prior
// config/state: keep the inline attributes null when they were null (saved-evaluator
// rule), and surface the backend's copy only when the user configured inline.
func TestRunRuleInlineEvaluatorsFollowPriorState(t *testing.T) {
	code := []json.RawMessage{json.RawMessage(`{"code":"function performEval(){}","language":"javascript"}`)}
	evID := "eval-1"

	// Saved-evaluator rule: prior inline state is null and the backend returns both an
	// evaluator_id and the resolved code inline. The inline attributes must stay null.
	saved := runRuleAPI{ID: "rule-1", EvaluatorID: &evID, CodeEvaluators: code}
	next, diags := modelFromRunRuleAPI(saved, runRuleModel{})
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if !next.CodeEvaluatorsJSON.IsNull() {
		t.Errorf("code_evaluators_json = %q, want null when the prior value was null", next.CodeEvaluatorsJSON.ValueString())
	}
	if !next.EvaluatorsJSON.IsNull() {
		t.Errorf("evaluators_json = %q, want null when the prior value was null", next.EvaluatorsJSON.ValueString())
	}

	// Inline rule: the user configured code_evaluators_json (prior non-null) and the
	// backend also reports a generated evaluator_id. The inline list must still be
	// surfaced — despite the evaluator_id — so the configured value round-trips.
	prior := runRuleModel{CodeEvaluatorsJSON: types.StringValue(`[{"code":"x","language":"javascript"}]`)}
	inline := runRuleAPI{ID: "rule-2", EvaluatorID: &evID, CodeEvaluators: code}
	next, diags = modelFromRunRuleAPI(inline, prior)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if next.CodeEvaluatorsJSON.IsNull() {
		t.Error("code_evaluators_json is null, want the inline list surfaced when the prior value was set")
	}
}

// TestRunRuleRejectsEvaluatorIDWithInline asserts the provider rejects a rule that
// combines evaluator_id with inline evaluator definitions, matching smith-backend's
// 422 (rules.py: "Provide either evaluator_id or evaluators/code_evaluators, not both").
func TestRunRuleRejectsEvaluatorIDWithInline(t *testing.T) {
	plan := runRuleModel{
		SessionID:          types.StringValue("11111111-1111-1111-1111-111111111111"),
		SamplingRate:       types.Float64Value(1),
		EvaluatorID:        types.StringValue("eval-1"),
		CodeEvaluatorsJSON: types.StringValue(`[{"code":"x","language":"javascript"}]`),
	}
	if d := validateRunRulePlan(plan); d == nil || !d.HasError() {
		t.Fatal("expected validateRunRulePlan to reject evaluator_id combined with inline code_evaluators_json")
	}
}

// TestRunRuleEvaluatorIDFollowsPriorState is a regression test for the inconsistent-result
// error on inline-evaluator rules. smith-backend assigns a generated evaluator_id even when
// the config leaves it unset; evaluator_id is Optional (not Computed), so writing that
// generated id into state when the user didn't configure one fails apply with
// "provider produced inconsistent result after apply" (planned null, got a string).
// modelFromRunRuleAPI must keep evaluator_id null unless the user actually set it.
func TestRunRuleEvaluatorIDFollowsPriorState(t *testing.T) {
	genID := "generated-by-backend"

	// Inline rule: user left evaluator_id unset (prior null); backend returns a generated
	// id. It must NOT be imported into state.
	inline := runRuleAPI{ID: "rule-1", EvaluatorID: &genID}
	next, diags := modelFromRunRuleAPI(inline, runRuleModel{})
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if !next.EvaluatorID.IsNull() {
		t.Errorf("evaluator_id = %q, want null when the user did not configure evaluator_id", next.EvaluatorID.ValueString())
	}

	// Saved-evaluator rule: user set evaluator_id (prior non-null); the API value is reflected.
	savedID := "eval-1"
	prior := runRuleModel{EvaluatorID: types.StringValue(savedID)}
	saved := runRuleAPI{ID: "rule-2", EvaluatorID: &savedID}
	next, diags = modelFromRunRuleAPI(saved, prior)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if next.EvaluatorID.ValueString() != savedID {
		t.Errorf("evaluator_id = %q, want %q reflected when the user configured it", next.EvaluatorID.ValueString(), savedID)
	}
}
