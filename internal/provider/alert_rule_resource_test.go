package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

func TestAlertRulePayloadFromModelResolvesURLEnv(t *testing.T) {
	t.Setenv("TEST_LANGSMITH_WEBHOOK_URL", "https://example.com/webhook")

	payload, err := alertRulePayloadFromModel(alertRuleModel{
		SessionID:     types.StringValue("session-id"),
		Name:          types.StringValue("example alert"),
		Description:   types.StringValue("example description"),
		Type:          types.StringValue("threshold"),
		Attribute:     types.StringValue("error_count"),
		Aggregation:   types.StringValue("sum"),
		WindowMinutes: types.Int64Value(15),
		Operator:      types.StringValue("gte"),
		Threshold:     types.Float64Value(10),
		Filter:        types.StringValue("eq(is_root, true)"),
		Actions: []alertActionModel{{
			Target:     types.StringValue("webhook"),
			ConfigJSON: types.StringValue(`{"headers":"{\"Content-Type\":\"application/json\"}"}`),
			URLEnv:     types.StringValue("TEST_LANGSMITH_WEBHOOK_URL"),
		}},
	})
	if err != nil {
		t.Fatalf("alertRulePayloadFromModel returned error: %v", err)
	}

	if got, want := payload.Rule.Filter, "eq(is_root, true)"; got == nil || *got != want {
		t.Fatalf("payload.Rule.Filter = %v, want %q", got, want)
	}
	if got, want := payload.Rule.Threshold, 10.0; got == nil || *got != want {
		t.Fatalf("payload.Rule.Threshold = %v, want %f", got, want)
	}

	config, err := decodeActionConfigFromAPI(payload.Actions[0].Config)
	if err != nil {
		t.Fatalf("decodeActionConfigFromAPI returned error: %v", err)
	}
	if got, want := config["url"], "https://example.com/webhook"; got != want {
		t.Fatalf("config[url] = %v, want %q", got, want)
	}
}

func TestModelFromAlertRuleResponsePreservesURLEnvAndDropsURL(t *testing.T) {
	configJSON, err := encodeActionConfigForAPI(map[string]any{
		"url":          "https://example.com/webhook",
		"project_name": "smith-issues-agent",
	})
	if err != nil {
		t.Fatalf("encodeActionConfigForAPI returned error: %v", err)
	}

	response := alertRulePayload{
		Rule: alertRuleAPI{
			ID:            "alert-id",
			Name:          "example alert",
			Description:   "example description",
			Type:          "threshold",
			Attribute:     "error_count",
			Aggregation:   "sum",
			WindowMinutes: 15,
			Operator:      "gte",
		},
		Actions: []alertActionAPI{{
			Target: "webhook",
			Config: configJSON,
		}},
	}

	state, err := modelFromAlertRuleResponse(response, alertRuleModel{
		Actions: []alertActionModel{{
			Target: types.StringValue("webhook"),
			URLEnv: types.StringValue("TEST_LANGSMITH_WEBHOOK_URL"),
		}},
	})
	if err != nil {
		t.Fatalf("modelFromAlertRuleResponse returned error: %v", err)
	}

	if got, want := state.Actions[0].URLEnv.ValueString(), "TEST_LANGSMITH_WEBHOOK_URL"; got != want {
		t.Fatalf("URLEnv = %q, want %q", got, want)
	}

	var config map[string]any
	if err := json.Unmarshal([]byte(state.Actions[0].ConfigJSON.ValueString()), &config); err != nil {
		t.Fatalf("json.Unmarshal(state config_json) returned error: %v", err)
	}
	if _, ok := config["url"]; ok {
		t.Fatalf("state config_json unexpectedly contains url: %s", state.Actions[0].ConfigJSON.ValueString())
	}
	if got, want := config["project_name"], "smith-issues-agent"; got != want {
		t.Fatalf("config[project_name] = %v, want %q", got, want)
	}
}

func TestAlertRuleIdentityForUpdateUsesStateForComputedID(t *testing.T) {
	plan := alertRuleModel{
		ID:        types.StringUnknown(),
		SessionID: types.StringValue("project-id"),
		Name:      types.StringValue("updated alert"),
	}
	state := alertRuleModel{
		ID:        types.StringValue("alert-id"),
		SessionID: types.StringValue("project-id"),
		Name:      types.StringValue("existing alert"),
	}

	next, sessionID, alertID, err := alertRuleIdentityForUpdate(plan, state)
	if err != nil {
		t.Fatalf("alertRuleIdentityForUpdate returned error: %v", err)
	}
	if got, want := sessionID, "project-id"; got != want {
		t.Fatalf("sessionID = %q, want %q", got, want)
	}
	if got, want := alertID, "alert-id"; got != want {
		t.Fatalf("alertID = %q, want %q", got, want)
	}
	if got, want := next.ID.ValueString(), "alert-id"; got != want {
		t.Fatalf("next.ID = %q, want %q", got, want)
	}
}

func TestAlertRuleIdentityForUpdateRejectsMissingID(t *testing.T) {
	_, _, _, err := alertRuleIdentityForUpdate(alertRuleModel{
		ID:        types.StringUnknown(),
		SessionID: types.StringValue("project-id"),
	}, alertRuleModel{})
	if err == nil {
		t.Fatalf("alertRuleIdentityForUpdate returned nil error, want missing ID error")
	}
}

func TestAccAlertRuleCRUDLocal(t *testing.T) {
	if os.Getenv("LANGSMITH_PROVIDER_ACC") != "1" {
		t.Skip("set LANGSMITH_PROVIDER_ACC=1 to run local alert CRUD smoke test")
	}
	t.Setenv("TEST_LANGSMITH_WEBHOOK_URL", "https://example.com/webhook")

	ctx := context.Background()
	profile := os.Getenv("LANGSMITH_PROFILE")
	if profile == "" {
		profile = "local"
	}
	client := langsmith.NewClient(langsmith.WithProfile(profile))

	sessionName := fmt.Sprintf("terraform-provider-alert-crud-%d", time.Now().UnixNano())
	session, err := client.Sessions.New(ctx, langsmith.SessionNewParams{
		Name:      langsmith.String(sessionName),
		Upsert:    langsmith.Bool(false),
		TraceTier: langsmith.F(langsmith.SessionNewParamsTraceTierLonglived),
	})
	if err != nil {
		t.Fatalf("create test session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = client.Sessions.Delete(context.Background(), session.ID)
	})

	model := alertRuleModel{
		SessionID:     types.StringValue(session.ID),
		Name:          types.StringValue("terraform provider alert CRUD smoke"),
		Description:   types.StringValue("created by Terraform provider local smoke test"),
		Type:          types.StringValue("threshold"),
		Attribute:     types.StringValue("error_count"),
		Aggregation:   types.StringValue("sum"),
		WindowMinutes: types.Int64Value(15),
		Operator:      types.StringValue("gte"),
		Threshold:     types.Float64Value(10),
		Filter:        types.StringValue("eq(is_root, true)"),
		Actions: []alertActionModel{{
			Target:     types.StringValue("webhook"),
			ConfigJSON: types.StringValue(`{"project_name":"` + sessionName + `","headers":"{\"Content-Type\":\"application/json\"}","body":"{\"text\":\"Terraform provider alert CRUD smoke\"}"}`),
			URLEnv:     types.StringValue("TEST_LANGSMITH_WEBHOOK_URL"),
		}},
	}

	createPayload, err := alertRulePayloadFromModel(model)
	if err != nil {
		t.Fatalf("build create payload: %v", err)
	}

	var created alertRulePayload
	if err := client.Post(ctx, alertRuleCollectionPath(session.ID), createPayload, &created); err != nil {
		t.Fatalf("create alert rule: %v", err)
	}
	if created.Rule.ID == "" {
		t.Fatalf("created alert rule had empty id")
	}

	var read alertRulePayload
	if err := client.Get(ctx, alertRuleResourcePath(session.ID, created.Rule.ID), nil, &read); err != nil {
		t.Fatalf("read alert rule: %v", err)
	}
	if got, want := read.Rule.Name, model.Name.ValueString(); got != want {
		t.Fatalf("read.Rule.Name = %q, want %q", got, want)
	}

	model.ID = types.StringValue(created.Rule.ID)
	model.Threshold = types.Float64Value(12)
	updatePayload, err := alertRulePayloadFromModel(model)
	if err != nil {
		t.Fatalf("build update payload: %v", err)
	}
	if err := client.Patch(ctx, alertRuleResourcePath(session.ID, created.Rule.ID), updatePayload, nil); err != nil {
		t.Fatalf("update alert rule: %v", err)
	}
	var updated alertRulePayload
	if err := client.Get(ctx, alertRuleResourcePath(session.ID, created.Rule.ID), nil, &updated); err != nil {
		t.Fatalf("read updated alert rule: %v", err)
	}
	if updated.Rule.Threshold == nil || *updated.Rule.Threshold != 12 {
		t.Fatalf("updated threshold = %v, want 12", updated.Rule.Threshold)
	}

	if err := client.Delete(ctx, alertRuleResourcePath(session.ID, created.Rule.ID), nil, nil); err != nil {
		t.Fatalf("delete alert rule: %v", err)
	}
	if err := client.Get(ctx, alertRuleResourcePath(session.ID, created.Rule.ID), nil, &read); !isLangSmithNotFound(err) {
		t.Fatalf("read after delete error = %v, want 404", err)
	}
}
