package provider

import (
	"context"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	frameworkvalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestGatewaySpendCapResourceCreateAndRead(t *testing.T) {
	requests := []string{}
	resource := newGatewaySpendCapResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.Method+" "+req.URL.Path)
		switch req.Method {
		case http.MethodPost:
			var payload spendCapCreatePayload
			decodeJSON(t, req, &payload)
			if payload.Name != "workspace monthly" || payload.PolicyType != gatewayPolicyTypeSpendCap {
				t.Fatalf("create payload = %#v", payload)
			}
			if payload.Config.Window != "monthly" || payload.Config.LimitUSD != 100 {
				t.Fatalf("create config = %#v", payload.Config)
			}
			if payload.Action != "block" || !payload.Enabled {
				t.Fatalf("create action/enabled = %#v", payload)
			}
			if len(payload.SubjectMatchers) != 1 || payload.SubjectMatchers[0].Key != "workspace_id" {
				t.Fatalf("create matchers = %#v", payload.SubjectMatchers)
			}
			writeJSON(t, w, gatewayPolicyAPI{ID: "policy-id"})
		case http.MethodGet:
			writeJSON(t, w, sampleSpendCapAPI())
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
	}))

	model, err := resource.createSpendCap(context.Background(), sampleSpendCapModel())
	if err != nil {
		t.Fatalf("createSpendCap() error = %v", err)
	}
	if model.ID.ValueString() != "policy-id" {
		t.Fatalf("model = %#v", model)
	}
	if model.LimitUSD.ValueFloat64() != 100 {
		t.Fatalf("LimitUSD = %v", model.LimitUSD.ValueFloat64())
	}
	if !reflect.DeepEqual(requests, []string{
		"POST /api/v1/platform/gateway-policies",
		"GET /api/v1/platform/gateway-policies/policy-id",
	}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestGatewaySpendCapResourceUpdate(t *testing.T) {
	requests := []string{}
	resource := newGatewaySpendCapResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.Method+" "+req.URL.Path)
		switch req.Method {
		case http.MethodPatch:
			var payload spendCapUpdatePayload
			decodeJSON(t, req, &payload)
			if payload.Name != "workspace monthly" || payload.Config.LimitUSD != 100 {
				t.Fatalf("update payload = %#v", payload)
			}
			if payload.Description != "Cap production workspace spend" {
				t.Fatalf("update description = %q", payload.Description)
			}
			writeJSON(t, w, gatewayPolicyAPI{ID: "policy-id"})
		case http.MethodGet:
			writeJSON(t, w, sampleSpendCapAPI())
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
	}))

	model, err := resource.updateSpendCap(context.Background(), "policy-id", sampleSpendCapModel())
	if err != nil {
		t.Fatalf("updateSpendCap() error = %v", err)
	}
	if model.ID.ValueString() != "policy-id" {
		t.Fatalf("model = %#v", model)
	}
	if !reflect.DeepEqual(requests, []string{
		"PATCH /api/v1/platform/gateway-policies/policy-id",
		"GET /api/v1/platform/gateway-policies/policy-id",
	}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestGatewaySpendCapResourceDelete(t *testing.T) {
	requests := 0
	resource := newGatewaySpendCapResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		if req.Method != http.MethodDelete || req.URL.Path != "/api/v1/platform/gateway-policies/policy-id" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	if err := resource.deleteSpendCap(context.Background(), "policy-id"); err != nil {
		t.Fatalf("deleteSpendCap() error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

// A create upserts on subject matchers, so replaying one could overwrite the
// policy the first attempt already made.
func TestSpendCapCreateDoesNotRetry(t *testing.T) {
	requests := 0
	resource := newGatewaySpendCapResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		http.Error(w, "temporary failure", http.StatusInternalServerError)
	}))

	if _, err := resource.createSpendCap(context.Background(), sampleSpendCapModel()); err == nil {
		t.Fatal("createSpendCap() error = nil")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

// GET by id does not filter on policy_type, so importing another kind of
// gateway policy has to be caught here rather than silently rewritten.
func TestSpendCapReadRejectsNonSpendCapPolicy(t *testing.T) {
	resource := newGatewaySpendCapResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		api := sampleSpendCapAPI()
		api.PolicyType = gatewayPolicyTypeDefaultSpendCap
		writeJSON(t, w, api)
	}))

	if _, err := resource.readSpendCap(context.Background(), "policy-id"); err == nil {
		t.Fatal("readSpendCap() error = nil, want type mismatch")
	}
}

// The API applies only the fields it finds non-null, so a JSON null would leave
// the stored description untouched and the apply would fail as an inconsistent
// result. Clearing has to go out as an empty string.
func TestSpendCapUpdateClearsDescriptionWithEmptyString(t *testing.T) {
	payload := spendCapUpdatePayloadFromModel(spendCapResourceModel{
		Name:            types.StringValue("cap"),
		Description:     types.StringNull(),
		Window:          types.StringValue("monthly"),
		LimitUSD:        types.Float64Value(10),
		Action:          types.StringValue("block"),
		Priority:        types.Int64Value(0),
		Enabled:         types.BoolValue(true),
		SubjectMatchers: []gatewayPolicySubjectMatcherModel{{Key: types.StringValue("workspace_id"), Value: types.StringValue("ws")}},
	})
	if payload.Description != "" {
		t.Fatalf("Description = %q, want empty string", payload.Description)
	}
}

func TestPreserveSpendCapOptionalShape(t *testing.T) {
	model, err := spendCapModelFromAPI(gatewayPolicyAPI{ID: "id", Name: "n", Action: "block", Config: []byte(`{"window":"monthly","limit_usd":1}`)})
	if err != nil {
		t.Fatalf("spendCapModelFromAPI() error = %v", err)
	}
	configured := spendCapResourceModel{Description: types.StringValue("")}
	got := preserveSpendCapOptionalShape(model, configured)
	if got.Description.IsNull() || got.Description.ValueString() != "" {
		t.Fatalf("Description = %#v, want explicit empty string", got.Description)
	}
	got = preserveSpendCapOptionalShape(model, spendCapResourceModel{Description: types.StringNull()})
	if !got.Description.IsNull() {
		t.Fatalf("optional null shape was not preserved: %#v", got)
	}

	// A cleared description round-trips as "" rather than null, which would
	// otherwise read as drift against a config that omits the attribute.
	cleared, err := spendCapModelFromAPI(gatewayPolicyAPI{ID: "id", Name: "n", Action: "block", Description: new(string), Config: []byte(`{"window":"monthly","limit_usd":1}`)})
	if err != nil {
		t.Fatalf("spendCapModelFromAPI() error = %v", err)
	}
	got = preserveSpendCapOptionalShape(cleared, spendCapResourceModel{Description: types.StringNull()})
	if !got.Description.IsNull() {
		t.Fatalf("Description = %#v, want null to match the unset config", got.Description)
	}
}

func TestGatewaySpendCapResourceMetadata(t *testing.T) {
	var resp resource.MetadataResponse
	NewGatewaySpendCapResource().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "langsmith"}, &resp)
	if resp.TypeName != "langsmith_gateway_spend_cap" {
		t.Fatalf("TypeName = %q, want langsmith_gateway_spend_cap", resp.TypeName)
	}
}

// The schema is assembled from the shared base plus the spend-cap attributes,
// so this also pins that the merge keeps both.
func TestGatewaySpendCapResourceSchema(t *testing.T) {
	var resp resource.SchemaResponse
	NewGatewaySpendCapResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)

	for _, name := range []string{
		"id", "name", "description", "action", "priority", "enabled",
		"organization_id", "created_at", "updated_at",
		"subject_matchers", "window", "limit_usd", "parent_policy_id", "current_spend_usd",
	} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Fatalf("schema missing %q", name)
		}
	}
	for _, name := range []string{"action", "priority", "enabled"} {
		attribute := resp.Schema.Attributes[name]
		if !attribute.IsComputed() || !attribute.IsOptional() {
			t.Fatalf("%q must be Optional+Computed so its default resolves at plan time", name)
		}
	}
}

func TestSpendCapReadReportsNotFound(t *testing.T) {
	res := newGatewaySpendCapResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.NotFound(w, req)
	}))

	if _, err := res.readSpendCap(context.Background(), "missing"); !isLangSmithNotFound(err) {
		t.Fatalf("readSpendCap() error = %v, want LangSmith 404", err)
	}
}

func TestSpendCapDeleteTreatsMissingAsDeleted(t *testing.T) {
	res := newGatewaySpendCapResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.NotFound(w, req)
	}))

	if err := res.deleteSpendCap(context.Background(), "missing"); err != nil {
		t.Fatalf("deleteSpendCap() error = %v, want nil for an already-deleted policy", err)
	}
}

// A materialized child is reported like any other conflict; Create tells the
// two apart by parent_policy_id and downgrades this case to a warning.
func TestFindConflictingSpendCapReportsMaterializedChild(t *testing.T) {
	parent := "default-policy"
	res := newGatewaySpendCapResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		writeJSON(t, w, []gatewayPolicyAPI{{
			ID:              "child",
			PolicyType:      gatewayPolicyTypeSpendCap,
			ParentPolicyID:  &parent,
			SubjectMatchers: []gatewayPolicySubjectMatcher{{Key: "workspace_id", Value: "ws-1"}},
		}})
	}))

	conflict, err := res.findConflictingSpendCap(context.Background(), []gatewayPolicySubjectMatcher{
		{Key: "workspace_id", Value: "ws-1"},
	})
	if err != nil {
		t.Fatalf("findConflictingSpendCap() error = %v", err)
	}
	if conflict == nil || conflict.ParentPolicyID == nil || *conflict.ParentPolicyID != parent {
		t.Fatalf("conflict = %#v, want the materialized child", conflict)
	}
}

func TestPositiveFloat64Validator(t *testing.T) {
	for _, tc := range []struct {
		value   types.Float64
		wantErr bool
	}{
		{types.Float64Value(0.01), false},
		{types.Float64Value(0), true},
		{types.Float64Value(-1), true},
		{types.Float64Null(), false},
		{types.Float64Unknown(), false},
	} {
		var resp frameworkvalidator.Float64Response
		positiveFloat64Validator{}.ValidateFloat64(
			context.Background(),
			frameworkvalidator.Float64Request{Path: path.Root("limit_usd"), ConfigValue: tc.value},
			&resp,
		)
		if got := resp.Diagnostics.HasError(); got != tc.wantErr {
			t.Fatalf("value %v: HasError() = %v, want %v", tc.value, got, tc.wantErr)
		}
	}
}

func TestSpendCapSubjectMatchersValidator(t *testing.T) {
	sameKey := spendCapMatcherSet(t, [][2]string{{"workspace_id", "a"}, {"workspace_id", "b"}})
	mixedKeys := spendCapMatcherSet(t, [][2]string{{"workspace_id", "a"}, {"user_id", "b"}})

	tooMany := make([][2]string, maxGatewayPolicySubjectMatchers+1)
	for i := range tooMany {
		tooMany[i] = [2]string{"workspace_id", strconv.Itoa(i)}
	}

	for name, tc := range map[string]struct {
		value   types.Set
		wantErr bool
	}{
		"same key":  {sameKey, false},
		"mixed key": {mixedKeys, true},
		"too many":  {spendCapMatcherSet(t, tooMany), true},
	} {
		var resp frameworkvalidator.SetResponse
		spendCapSubjectMatchersValidator{}.ValidateSet(
			context.Background(),
			frameworkvalidator.SetRequest{Path: path.Root("subject_matchers"), ConfigValue: tc.value},
			&resp,
		)
		if got := resp.Diagnostics.HasError(); got != tc.wantErr {
			t.Fatalf("%s: HasError() = %v, want %v", name, got, tc.wantErr)
		}
	}
}

func spendCapMatcherSet(t *testing.T, matchers [][2]string) types.Set {
	t.Helper()
	objectType := types.ObjectType{AttrTypes: map[string]attr.Type{"key": types.StringType, "value": types.StringType}}
	elements := make([]attr.Value, 0, len(matchers))
	for _, matcher := range matchers {
		object, diags := types.ObjectValue(objectType.AttrTypes, map[string]attr.Value{
			"key":   types.StringValue(matcher[0]),
			"value": types.StringValue(matcher[1]),
		})
		if diags.HasError() {
			t.Fatalf("ObjectValue() diagnostics = %v", diags)
		}
		elements = append(elements, object)
	}
	set, diags := types.SetValue(objectType, elements)
	if diags.HasError() {
		t.Fatalf("SetValue() diagnostics = %v", diags)
	}
	return set
}

func sampleSpendCapModel() spendCapResourceModel {
	return spendCapResourceModel{
		Name:        types.StringValue("workspace monthly"),
		Description: types.StringValue("Cap production workspace spend"),
		Window:      types.StringValue("monthly"),
		LimitUSD:    types.Float64Value(100),
		Action:      types.StringValue("block"),
		Priority:    types.Int64Value(0),
		Enabled:     types.BoolValue(true),
		SubjectMatchers: []gatewayPolicySubjectMatcherModel{{
			Key:   types.StringValue("workspace_id"),
			Value: types.StringValue("28473783-8e72-446b-ae6b-93addd5bc67f"),
		}},
	}
}

func sampleSpendCapAPI() gatewayPolicyAPI {
	spend := 12.5
	desc := "Cap production workspace spend"
	return gatewayPolicyAPI{
		ID:             "policy-id",
		OrganizationID: "org-id",
		Name:           "workspace monthly",
		Description:    &desc,
		SubjectMatchers: []gatewayPolicySubjectMatcher{{
			Key: "workspace_id", Value: "28473783-8e72-446b-ae6b-93addd5bc67f",
		}},
		PolicyType:      gatewayPolicyTypeSpendCap,
		Config:          []byte(`{"window":"monthly","limit_usd":100}`),
		Action:          "block",
		Priority:        0,
		Enabled:         true,
		CreatedAt:       "created",
		UpdatedAt:       "updated",
		CurrentSpendUSD: &spend,
	}
}

func newGatewaySpendCapResourceWithServer(t *testing.T, handler http.Handler) *GatewaySpendCapResource {
	t.Helper()
	return &GatewaySpendCapResource{client: newGatewayPolicyTestClient(t, handler)}
}
