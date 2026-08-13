package provider

// The LLM Gateway exposes every kind of policy through a single collection:
// spend caps, rate limits, guard (data protection) policies and route configs
// are all rows that differ only by policy_type and the shape of config. This
// file holds what they share. Each policy type still gets its own resource,
// because the config shapes, the subject matcher contracts and the computed
// fields diverge too much to fold into one polymorphic resource.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	frameworkvalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

const gatewayPoliciesPath = "api/v1/platform/gateway-policies"

// Policy types the API accepts. Types without a resource of their own are named
// here because policy families span them, and a family decides what a create
// collides with.
const (
	gatewayPolicyTypeSpendCap         = "spend_cap"
	gatewayPolicyTypeDefaultSpendCap  = "default_spend_cap"
	gatewayPolicyTypeGuard            = "guard"
	gatewayPolicyTypeRateLimit        = "rate_limit"
	gatewayPolicyTypeDefaultRateLimit = "default_rate_limit"
	gatewayPolicyTypeRouteConfig      = "route_config"
)

// gatewayPolicyTypes lists every accepted policy type, for filters that accept
// any of them.
var gatewayPolicyTypes = []string{
	gatewayPolicyTypeSpendCap,
	gatewayPolicyTypeDefaultSpendCap,
	gatewayPolicyTypeGuard,
	gatewayPolicyTypeRateLimit,
	gatewayPolicyTypeDefaultRateLimit,
	gatewayPolicyTypeRouteConfig,
}

// gatewayPolicyAction is the only enforcement action the API accepts, for every
// policy type.
const gatewayPolicyAction = "block"

// maxGatewayPolicySubjectMatchers mirrors the API's per-policy matcher ceiling.
const maxGatewayPolicySubjectMatchers = 10

func gatewayPolicyResourcePath(id string) string {
	return fmt.Sprintf("%s/%s", gatewayPoliciesPath, id)
}

// gatewayPolicySubjectMatcher is one key/value pair selecting the requests a
// policy applies to.
type gatewayPolicySubjectMatcher struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type gatewayPolicySubjectMatcherModel struct {
	Key   types.String `tfsdk:"key"`
	Value types.String `tfsdk:"value"`
}

// gatewayPolicyAPI mirrors the API's policy record. The usage fields are
// type-specific — current_spend_usd is populated only for spend caps — which
// follows the server, where one record type carries whichever usage applies.
type gatewayPolicyAPI struct {
	ID              string                        `json:"id"`
	OrganizationID  string                        `json:"organization_id"`
	Name            string                        `json:"name"`
	Description     *string                       `json:"description"`
	SubjectMatchers []gatewayPolicySubjectMatcher `json:"subject_matchers"`
	PolicyType      string                        `json:"policy_type"`
	Config          json.RawMessage               `json:"config"`
	Action          string                        `json:"action"`
	Priority        int                           `json:"priority"`
	Enabled         bool                          `json:"enabled"`
	CreatedAt       string                        `json:"created_at"`
	UpdatedAt       string                        `json:"updated_at"`
	ParentPolicyID  *string                       `json:"parent_policy_id"`
	CurrentSpendUSD *float64                      `json:"current_spend_usd"`
}

// gatewayPolicyCreatePayload is generic over the config so each policy type
// keeps a typed, validated config while the fields around it stay in one place.
type gatewayPolicyCreatePayload[Config any] struct {
	Name            string                        `json:"name"`
	Description     string                        `json:"description"`
	SubjectMatchers []gatewayPolicySubjectMatcher `json:"subject_matchers"`
	PolicyType      string                        `json:"policy_type"`
	Config          Config                        `json:"config"`
	Action          string                        `json:"action"`
	Priority        int                           `json:"priority"`
	Enabled         bool                          `json:"enabled"`
}

// gatewayPolicyUpdatePayload omits policy_type, which is immutable. Description
// is a plain string rather than a pointer because the API applies only the
// fields it finds non-null: a JSON null reads as "absent" and silently leaves
// the stored description in place, so clearing one means sending an empty
// string.
type gatewayPolicyUpdatePayload[Config any] struct {
	Name            string                        `json:"name"`
	Description     string                        `json:"description"`
	SubjectMatchers []gatewayPolicySubjectMatcher `json:"subject_matchers"`
	Config          Config                        `json:"config"`
	Action          string                        `json:"action"`
	Priority        int                           `json:"priority"`
	Enabled         bool                          `json:"enabled"`
}

// createGatewayPolicy posts a new policy. Retries are disabled because a create
// upserts on subject matchers: replaying one after an ambiguous failure would
// overwrite the policy the first attempt already made.
func createGatewayPolicy[Config any](ctx context.Context, client *langsmith.Client, payload gatewayPolicyCreatePayload[Config]) (gatewayPolicyAPI, error) {
	var result gatewayPolicyAPI
	if err := client.Post(ctx, gatewayPoliciesPath, payload, &result, option.WithMaxRetries(0)); err != nil {
		return gatewayPolicyAPI{}, err
	}
	if result.ID == "" {
		return gatewayPolicyAPI{}, errors.New("LangSmith did not return a gateway policy ID")
	}
	return result, nil
}

// readGatewayPolicy fetches one policy and rejects a record of another type.
// GET by id does not filter on policy_type, so importing the id of, say, a
// guard policy into a spend cap resource would otherwise bind the two and
// rewrite the row as a spend cap on the next apply.
func readGatewayPolicy(ctx context.Context, client *langsmith.Client, id, policyType string) (gatewayPolicyAPI, error) {
	var result gatewayPolicyAPI
	if err := client.Get(ctx, gatewayPolicyResourcePath(id), nil, &result); err != nil {
		return gatewayPolicyAPI{}, err
	}
	if result.PolicyType != "" && result.PolicyType != policyType {
		return gatewayPolicyAPI{}, fmt.Errorf("gateway policy %s has type %q, want %q", id, result.PolicyType, policyType)
	}
	return result, nil
}

func updateGatewayPolicy[Config any](ctx context.Context, client *langsmith.Client, id string, payload gatewayPolicyUpdatePayload[Config]) error {
	var result gatewayPolicyAPI
	return client.Patch(ctx, gatewayPolicyResourcePath(id), payload, &result)
}

// deleteGatewayPolicy treats an already-missing policy as deleted.
func deleteGatewayPolicy(ctx context.Context, client *langsmith.Client, id string) error {
	if err := client.Delete(ctx, gatewayPolicyResourcePath(id), nil, nil); err != nil && !isLangSmithNotFound(err) {
		return err
	}
	return nil
}

// gatewayPolicyListQuery narrows the policy list to one key/value matcher pair.
// The API filters by JSONB containment, so it returns every policy that carries
// the pair, not only those whose matcher set is exactly it.
type gatewayPolicyListQuery struct {
	PolicyType          string `query:"policy_type"`
	SubjectMatcherKey   string `query:"subject_matcher_key"`
	SubjectMatcherValue string `query:"subject_matcher_value"`
}

func (q gatewayPolicyListQuery) URLQuery() url.Values {
	values := url.Values{}
	if q.PolicyType != "" {
		values.Set("policy_type", q.PolicyType)
	}
	if q.SubjectMatcherKey != "" {
		values.Set("subject_matcher_key", q.SubjectMatcherKey)
	}
	if q.SubjectMatcherValue != "" {
		values.Set("subject_matcher_value", q.SubjectMatcherValue)
	}
	return values
}

// gatewayPolicyFamily mirrors policyFamily on the API: the bucket within which
// a create upserts, which is coarser than policy_type. A default and the
// explicit policy that overrides it share a bucket, so creating one can land on
// the other.
func gatewayPolicyFamily(policyType string) string {
	switch policyType {
	case gatewayPolicyTypeGuard:
		return "guard"
	case gatewayPolicyTypeRateLimit, gatewayPolicyTypeDefaultRateLimit:
		return "rate_limit"
	case gatewayPolicyTypeRouteConfig:
		return "route_config"
	default:
		return "cost"
	}
}

// findConflictingGatewayPolicy returns the policy that a create with these
// matchers would upsert onto, or nil when there is none. The API upserts rather
// than rejecting a duplicate, so an unchecked create would overwrite that
// policy in place and a later destroy would delete it.
//
// Candidates are matched by family rather than by policy_type, mirroring the
// API: a spend_cap create lands on a default_spend_cap carrying the same
// matchers, and a rate_limit on a default_rate_limit. The list filter is
// containment on a single pair, so probing with one matcher returns a superset
// of the set-equal candidates and the exact comparison happens here.
//
// This is advisory only: a policy created between this read and the POST still
// upserts.
func findConflictingGatewayPolicy(ctx context.Context, client *langsmith.Client, policyType string, matchers []gatewayPolicySubjectMatcher) (*gatewayPolicyAPI, error) {
	// route_config rows never upsert — several may share one workspace matcher.
	if len(matchers) == 0 || policyType == gatewayPolicyTypeRouteConfig {
		return nil, nil
	}
	// Deliberately unfiltered by policy_type so the rest of the family comes
	// back too; the family comparison below is what narrows the result.
	query := gatewayPolicyListQuery{
		SubjectMatcherKey:   matchers[0].Key,
		SubjectMatcherValue: matchers[0].Value,
	}
	var candidates []gatewayPolicyAPI
	if err := client.Get(ctx, gatewayPoliciesPath, query, &candidates); err != nil {
		return nil, err
	}
	family := gatewayPolicyFamily(policyType)
	for i := range candidates {
		if gatewayPolicyFamily(candidates[i].PolicyType) != family {
			continue
		}
		if sameSubjectMatcherSet(candidates[i].SubjectMatchers, matchers) {
			return &candidates[i], nil
		}
	}
	return nil, nil
}

// sameSubjectMatcherSet mirrors how the API decides that a create collides with
// an existing policy: bidirectional JSONB containment, so order and duplicates
// do not matter.
func sameSubjectMatcherSet(left, right []gatewayPolicySubjectMatcher) bool {
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	distinct := func(matchers []gatewayPolicySubjectMatcher) map[gatewayPolicySubjectMatcher]struct{} {
		out := make(map[gatewayPolicySubjectMatcher]struct{}, len(matchers))
		for _, matcher := range matchers {
			out[matcher] = struct{}{}
		}
		return out
	}
	leftSet, rightSet := distinct(left), distinct(right)
	if len(leftSet) != len(rightSet) {
		return false
	}
	for matcher := range leftSet {
		if _, ok := rightSet[matcher]; !ok {
			return false
		}
	}
	return true
}

// gatewayPolicyBaseAttributes returns the schema attributes every gateway
// policy resource shares. Callers add their own subject_matchers, since the
// matcher contract differs by policy type, plus the config attributes and any
// computed field specific to the type. displayName opens the generated
// descriptions: "Spend cap policy" yields "Spend cap policy ID".
func gatewayPolicyBaseAttributes(displayName string) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			MarkdownDescription: displayName + " ID.",
		},
		"name": schema.StringAttribute{
			Required:            true,
			Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
			MarkdownDescription: "Human-readable policy name.",
		},
		"description": schema.StringAttribute{
			Optional:            true,
			MarkdownDescription: "Optional policy description.",
		},
		"action": schema.StringAttribute{
			Optional:            true,
			Computed:            true,
			Default:             stringdefault.StaticString(gatewayPolicyAction),
			Validators:          []frameworkvalidator.String{oneOfStringValidator{values: []string{gatewayPolicyAction}}},
			MarkdownDescription: "Enforcement action when the policy is triggered. Currently only `block` is supported, which is also the default.",
		},
		"priority": schema.Int64Attribute{
			Optional:            true,
			Computed:            true,
			Default:             int64default.StaticInt64(0),
			MarkdownDescription: "Policy priority. Lower values take precedence when multiple policies match. Defaults to `0`.",
		},
		"enabled": schema.BoolAttribute{
			Optional:            true,
			Computed:            true,
			Default:             booldefault.StaticBool(true),
			MarkdownDescription: "Whether the policy is enforced. Defaults to `true`.",
		},
		"organization_id": schema.StringAttribute{
			Computed:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			MarkdownDescription: "Organization that owns the policy.",
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
	}
}

// gatewayPolicySubjectMatcherNestedObject builds the key/value matcher object.
// keys restricts the matcher key when non-empty; a policy type that also
// accepts custom header keys passes nil.
//
// The value is required to be non-empty, which is what keeps an explicit policy
// from ever being set-equal to a default of the same family — defaults are the
// rows whose single matcher value is blank. A policy type that manages defaults
// needs its own matcher object, and findConflictingGatewayPolicy becomes load
// bearing for it.
func gatewayPolicySubjectMatcherNestedObject(keys []string) schema.NestedAttributeObject {
	key := schema.StringAttribute{
		Required:            true,
		MarkdownDescription: "Matcher key.",
	}
	if len(keys) > 0 {
		key.Validators = []frameworkvalidator.String{oneOfStringValidator{values: keys}}
		key.MarkdownDescription = "Matcher key: " + orList(keys) + "."
	}
	return schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
		"key": key,
		"value": schema.StringAttribute{
			Required:            true,
			Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
			MarkdownDescription: "Matcher value for the selected key.",
		},
	}}
}

// gatewayPolicyDescriptionShape keeps description shaped like the configuration
// it came from. The API treats an absent description and an empty one as the
// same thing, so a round trip can flip null to "" or back; echoing whichever the
// practitioner wrote avoids an "inconsistent result after apply" error on the
// apply that clears it, and a permanent diff afterwards.
func gatewayPolicyDescriptionShape(fromAPI, configured types.String) types.String {
	if configured.IsUnknown() {
		return fromAPI
	}
	if configured.IsNull() {
		if !fromAPI.IsNull() && fromAPI.ValueString() == "" {
			return types.StringNull()
		}
		return fromAPI
	}
	if fromAPI.IsNull() && configured.ValueString() == "" {
		return types.StringValue("")
	}
	return fromAPI
}

// orList renders values as a backtick-quoted prose list for documentation.
func orList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "`"+value+"`")
	}
	switch len(quoted) {
	case 0, 1:
		return strings.Join(quoted, "")
	case 2:
		return quoted[0] + " or " + quoted[1]
	default:
		return strings.Join(quoted[:len(quoted)-1], ", ") + ", or " + quoted[len(quoted)-1]
	}
}

func gatewayPolicyMatchersFromModel(matchers []gatewayPolicySubjectMatcherModel) []gatewayPolicySubjectMatcher {
	out := make([]gatewayPolicySubjectMatcher, 0, len(matchers))
	for _, matcher := range matchers {
		out = append(out, gatewayPolicySubjectMatcher{
			Key:   matcher.Key.ValueString(),
			Value: matcher.Value.ValueString(),
		})
	}
	return out
}

func gatewayPolicyMatcherModelsFromAPI(matchers []gatewayPolicySubjectMatcher) []gatewayPolicySubjectMatcherModel {
	out := make([]gatewayPolicySubjectMatcherModel, 0, len(matchers))
	for _, matcher := range matchers {
		out = append(out, gatewayPolicySubjectMatcherModel{
			Key:   types.StringValue(matcher.Key),
			Value: types.StringValue(matcher.Value),
		})
	}
	return out
}
