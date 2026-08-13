package provider

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestGatewayPoliciesDataSourceMetadata(t *testing.T) {
	var resp datasource.MetadataResponse
	NewGatewayPoliciesDataSource().Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "langsmith"}, &resp)
	if resp.TypeName != "langsmith_gateway_policies" {
		t.Fatalf("TypeName = %q, want langsmith_gateway_policies", resp.TypeName)
	}
}

func TestGatewayPoliciesDataSourceReadPassesFilters(t *testing.T) {
	var gotQuery url.Values
	var gotPath string
	source := &GatewayPoliciesDataSource{client: newGatewayPolicyTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path
		gotQuery = req.URL.Query()
		writeJSON(t, w, []gatewayPolicyAPI{})
	}))}

	data := gatewayPoliciesDataSourceModel{
		PolicyType:          types.StringValue(gatewayPolicyTypeSpendCap),
		SubjectMatcherKey:   types.StringValue("workspace_id"),
		SubjectMatcherValue: types.StringValue("ws-1"),
	}
	if _, _, err := source.listPolicies(context.Background(), data); err != nil {
		t.Fatalf("listPolicies() error = %v", err)
	}

	if gotPath != "/api/v1/platform/gateway-policies" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotQuery.Get("policy_type") != gatewayPolicyTypeSpendCap ||
		gotQuery.Get("subject_matcher_key") != "workspace_id" ||
		gotQuery.Get("subject_matcher_value") != "ws-1" {
		t.Fatalf("query = %v", gotQuery)
	}
}

// With no filters the request carries no query at all, so the API returns every
// policy type rather than defaulting to one.
func TestGatewayPoliciesDataSourceReadWithoutFilters(t *testing.T) {
	var gotQuery url.Values
	source := &GatewayPoliciesDataSource{client: newGatewayPolicyTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotQuery = req.URL.Query()
		writeJSON(t, w, []gatewayPolicyAPI{})
	}))}

	if _, _, err := source.listPolicies(context.Background(), gatewayPoliciesDataSourceModel{}); err != nil {
		t.Fatalf("listPolicies() error = %v", err)
	}
	if len(gotQuery) != 0 {
		t.Fatalf("query = %v, want empty", gotQuery)
	}
}

// The config of a listed policy is passed through as JSON, because its shape
// depends on the policy type and the data source lists every type.
func TestGatewayPolicyDataSourceModelsCarryConfigAndUsage(t *testing.T) {
	spend := 12.5
	parent := "the-default"
	models := gatewayPolicyDataSourceModels([]gatewayPolicyAPI{
		{
			ID:              "child",
			PolicyType:      gatewayPolicyTypeSpendCap,
			Config:          []byte(`{"window":"monthly","limit_usd":100}`),
			ParentPolicyID:  &parent,
			CurrentSpendUSD: &spend,
			SubjectMatchers: []gatewayPolicySubjectMatcher{{Key: "workspace_id", Value: "ws-1"}},
		},
		{
			ID:         "guard",
			PolicyType: gatewayPolicyTypeGuard,
			Config:     []byte(`{"version":1,"detect":{"secrets":true}}`),
		},
	})

	if len(models) != 2 {
		t.Fatalf("models = %#v", models)
	}
	child := models[0]
	if child.ConfigJSON.ValueString() != `{"window":"monthly","limit_usd":100}` {
		t.Fatalf("ConfigJSON = %q", child.ConfigJSON.ValueString())
	}
	if child.ParentPolicyID.ValueString() != parent {
		t.Fatalf("ParentPolicyID = %#v, want the materializing default", child.ParentPolicyID)
	}
	if child.CurrentSpendUSD.ValueFloat64() != spend {
		t.Fatalf("CurrentSpendUSD = %#v", child.CurrentSpendUSD)
	}
	if len(child.SubjectMatchers) != 1 || child.SubjectMatchers[0].Value.ValueString() != "ws-1" {
		t.Fatalf("SubjectMatchers = %#v", child.SubjectMatchers)
	}

	// A policy of another type carries no spend, and must not report zero.
	guard := models[1]
	if !guard.CurrentSpendUSD.IsNull() {
		t.Fatalf("CurrentSpendUSD = %#v, want null for a guard policy", guard.CurrentSpendUSD)
	}
	if !guard.ParentPolicyID.IsNull() {
		t.Fatalf("ParentPolicyID = %#v, want null", guard.ParentPolicyID)
	}
}

// The id distinguishes result sets so two data sources with different filters
// do not collapse into one.
func TestGatewayPoliciesDataSourceID(t *testing.T) {
	unfiltered := gatewayPoliciesDataSourceID(gatewayPolicyListQuery{})
	filtered := gatewayPoliciesDataSourceID(gatewayPolicyListQuery{PolicyType: gatewayPolicyTypeSpendCap})
	if unfiltered.ValueString() != "gateway-policies" {
		t.Fatalf("unfiltered id = %q", unfiltered.ValueString())
	}
	if filtered.Equal(unfiltered) {
		t.Fatalf("filtered id = %q, want it to differ from the unfiltered one", filtered.ValueString())
	}
}
