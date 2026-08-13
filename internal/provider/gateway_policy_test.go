package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestSameSubjectMatcherSet(t *testing.T) {
	workspaceA := gatewayPolicySubjectMatcher{Key: "workspace_id", Value: "a"}
	workspaceB := gatewayPolicySubjectMatcher{Key: "workspace_id", Value: "b"}
	userA := gatewayPolicySubjectMatcher{Key: "user_id", Value: "a"}

	for name, tc := range map[string]struct {
		left, right []gatewayPolicySubjectMatcher
		want        bool
	}{
		"identical":            {[]gatewayPolicySubjectMatcher{workspaceA}, []gatewayPolicySubjectMatcher{workspaceA}, true},
		"reordered":            {[]gatewayPolicySubjectMatcher{workspaceA, workspaceB}, []gatewayPolicySubjectMatcher{workspaceB, workspaceA}, true},
		"duplicates ignored":   {[]gatewayPolicySubjectMatcher{workspaceA, workspaceA}, []gatewayPolicySubjectMatcher{workspaceA}, true},
		"subset is not equal":  {[]gatewayPolicySubjectMatcher{workspaceA}, []gatewayPolicySubjectMatcher{workspaceA, workspaceB}, false},
		"same value other key": {[]gatewayPolicySubjectMatcher{workspaceA}, []gatewayPolicySubjectMatcher{userA}, false},
		"empty never matches":  {nil, []gatewayPolicySubjectMatcher{workspaceA}, false},
	} {
		if got := sameSubjectMatcherSet(tc.left, tc.right); got != tc.want {
			t.Fatalf("%s: sameSubjectMatcherSet() = %v, want %v", name, got, tc.want)
		}
	}
}

// The families decide what a create can collide with, so they have to track the
// API's own buckets: a default and the policy that overrides it share one.
func TestGatewayPolicyFamily(t *testing.T) {
	for name, tc := range map[string]struct {
		left, right string
		want        bool
	}{
		"spend cap and its default": {gatewayPolicyTypeSpendCap, gatewayPolicyTypeDefaultSpendCap, true},
		"rate limit and its default": {
			gatewayPolicyTypeRateLimit, gatewayPolicyTypeDefaultRateLimit, true,
		},
		"spend cap and guard":      {gatewayPolicyTypeSpendCap, gatewayPolicyTypeGuard, false},
		"spend cap and rate limit": {gatewayPolicyTypeSpendCap, gatewayPolicyTypeRateLimit, false},
		"guard and route config":   {gatewayPolicyTypeGuard, gatewayPolicyTypeRouteConfig, false},
	} {
		if got := gatewayPolicyFamily(tc.left) == gatewayPolicyFamily(tc.right); got != tc.want {
			t.Fatalf("%s: same family = %v, want %v", name, got, tc.want)
		}
	}
}

func TestFindConflictingGatewayPolicyMatchesOnlySetEqualPolicies(t *testing.T) {
	var gotQuery url.Values
	client := newGatewayPolicyTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotQuery = req.URL.Query()
		writeJSON(t, w, []gatewayPolicyAPI{
			// Carries the probed pair but targets more subjects, so the API
			// would not upsert onto it.
			{ID: "superset", PolicyType: gatewayPolicyTypeSpendCap, SubjectMatchers: []gatewayPolicySubjectMatcher{
				{Key: "workspace_id", Value: "ws-1"},
				{Key: "workspace_id", Value: "ws-2"},
			}},
			{ID: "exact", PolicyType: gatewayPolicyTypeSpendCap, SubjectMatchers: []gatewayPolicySubjectMatcher{
				{Key: "workspace_id", Value: "ws-1"},
			}},
		})
	}))

	conflict, err := findConflictingGatewayPolicy(context.Background(), client, gatewayPolicyTypeSpendCap, []gatewayPolicySubjectMatcher{
		{Key: "workspace_id", Value: "ws-1"},
	})
	if err != nil {
		t.Fatalf("findConflictingGatewayPolicy() error = %v", err)
	}
	if conflict == nil || conflict.ID != "exact" {
		t.Fatalf("conflict = %#v, want the set-equal policy", conflict)
	}
	// policy_type is deliberately absent: the whole family has to come back so
	// a default carrying the same matchers is seen too.
	if gotQuery.Has("policy_type") {
		t.Fatalf("query pinned policy_type, which would hide the rest of the family: %v", gotQuery)
	}
	if gotQuery.Get("subject_matcher_key") != "workspace_id" || gotQuery.Get("subject_matcher_value") != "ws-1" {
		t.Fatalf("query = %v", gotQuery)
	}
}

func TestFindConflictingGatewayPolicyIgnoresUnrelatedPolicies(t *testing.T) {
	client := newGatewayPolicyTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		writeJSON(t, w, []gatewayPolicyAPI{{
			ID:         "other",
			PolicyType: gatewayPolicyTypeSpendCap,
			SubjectMatchers: []gatewayPolicySubjectMatcher{
				{Key: "workspace_id", Value: "ws-1"},
				{Key: "user_id", Value: "u-1"},
			},
		}})
	}))

	conflict, err := findConflictingGatewayPolicy(context.Background(), client, gatewayPolicyTypeSpendCap, []gatewayPolicySubjectMatcher{
		{Key: "workspace_id", Value: "ws-1"},
	})
	if err != nil {
		t.Fatalf("findConflictingGatewayPolicy() error = %v", err)
	}
	if conflict != nil {
		t.Fatalf("conflict = %#v, want nil", conflict)
	}
}

// A create upserts within a family, so a default carrying the same matchers is
// a conflict even though its policy_type differs.
func TestFindConflictingGatewayPolicyMatchesAcrossTheFamily(t *testing.T) {
	client := newGatewayPolicyTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		writeJSON(t, w, []gatewayPolicyAPI{{
			ID:              "the-default",
			PolicyType:      gatewayPolicyTypeDefaultSpendCap,
			SubjectMatchers: []gatewayPolicySubjectMatcher{{Key: "workspace_id", Value: ""}},
		}})
	}))

	conflict, err := findConflictingGatewayPolicy(context.Background(), client, gatewayPolicyTypeSpendCap, []gatewayPolicySubjectMatcher{
		{Key: "workspace_id", Value: ""},
	})
	if err != nil {
		t.Fatalf("findConflictingGatewayPolicy() error = %v", err)
	}
	if conflict == nil || conflict.ID != "the-default" {
		t.Fatalf("conflict = %#v, want the default in the same family", conflict)
	}
}

// Different families never collide, so a guard policy on the same subject is
// not something a spend cap create would overwrite.
func TestFindConflictingGatewayPolicySkipsOtherFamilies(t *testing.T) {
	client := newGatewayPolicyTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		writeJSON(t, w, []gatewayPolicyAPI{{
			ID:              "guard-policy",
			PolicyType:      gatewayPolicyTypeGuard,
			SubjectMatchers: []gatewayPolicySubjectMatcher{{Key: "workspace_id", Value: "ws-1"}},
		}})
	}))

	conflict, err := findConflictingGatewayPolicy(context.Background(), client, gatewayPolicyTypeSpendCap, []gatewayPolicySubjectMatcher{
		{Key: "workspace_id", Value: "ws-1"},
	})
	if err != nil {
		t.Fatalf("findConflictingGatewayPolicy() error = %v", err)
	}
	if conflict != nil {
		t.Fatalf("conflict = %#v, want nil for a policy in another family", conflict)
	}
}

// route_config rows always insert, so probing for a conflict would be wrong as
// well as wasteful.
func TestFindConflictingGatewayPolicySkipsRouteConfig(t *testing.T) {
	client := newGatewayPolicyTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		t.Fatal("route_config must not probe for conflicts")
	}))

	conflict, err := findConflictingGatewayPolicy(context.Background(), client, gatewayPolicyTypeRouteConfig, []gatewayPolicySubjectMatcher{
		{Key: "workspace_id", Value: "ws-1"},
	})
	if err != nil || conflict != nil {
		t.Fatalf("conflict = %#v, err = %v, want nil, nil", conflict, err)
	}
}

func TestGatewayPolicyDescriptionShape(t *testing.T) {
	for name, tc := range map[string]struct {
		fromAPI    types.String
		configured types.String
		want       types.String
	}{
		// The API reports a cleared description as "", which would read as
		// drift against a config that omits the attribute.
		"cleared stays null":     {types.StringValue(""), types.StringNull(), types.StringNull()},
		"explicit empty is kept": {types.StringNull(), types.StringValue(""), types.StringValue("")},
		"value passes through":   {types.StringValue("set"), types.StringValue("set"), types.StringValue("set")},
		"unknown config defers":  {types.StringValue("set"), types.StringUnknown(), types.StringValue("set")},
	} {
		if got := gatewayPolicyDescriptionShape(tc.fromAPI, tc.configured); !got.Equal(tc.want) {
			t.Fatalf("%s: got %#v, want %#v", name, got, tc.want)
		}
	}
}

func newGatewayPolicyTestClient(t *testing.T, handler http.Handler) *langsmith.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("test-key"))
}
