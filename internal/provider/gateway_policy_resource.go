package provider

// Manages a LangSmith Gateway Policy.

// Implemented policy types:
// - spend_cap policy

// TODO: Future policy types:
// - rate_limit policy
// - default_spend_cap policy
// - default_rate_limit policy
// - ...

// Regarding default_spend_cap and materialized children:
// We can have the terraform resource to create the default_spend_cap policy,
// but here are some notes concerning the materialized children:
// - they appear on-demand as new users start using the gateway.
// - they may be imported to and managed by the terraform state as a gateway policy resource.
//     - Updating a materialized child directly does detach it from the parent, but another child can be
//       auto-created if there is another gateway invocation with different subject matchers.
//     - If updating the default_spend_cap policy, the materialized child will also inherit the changes.
//       This means that the imported materialized child will have drift, and the terraform plan
//       would cause an update to detach the child from the parent.
// - it may become useful to have a list of all the materialized children in the terraform state,
//   for visibility and management, so we may implement a terraform data source that
//   lists the children of a given parent policy id. This would not be a list of
//   gateway policy terraform resources, but rather a list of gateway data objects.

// TODO: validators for the subject_matchers, for each of the policy types.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/objectvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

// data models

// gatewayPolicyModel maps gateway policy terraform configuration data.
type gatewayPolicyModel struct {
	Action            types.String                       `tfsdk:"action"`
	Config            *gatewayPolicyConfigModel          `tfsdk:"config"`
	CreatedAt         types.String                       `tfsdk:"created_at"`
	CreatedBy         types.String                       `tfsdk:"created_by"`
	Description       types.String                       `tfsdk:"description"`
	Enabled           types.Bool                         `tfsdk:"enabled"`
	ID                types.String                       `tfsdk:"id"`
	IsSystemGenerated types.Bool                         `tfsdk:"is_system_generated"`
	Name              types.String                       `tfsdk:"name"`
	OrganizationID    types.String                       `tfsdk:"organization_id"`
	ParentPolicyID    types.String                       `tfsdk:"parent_policy_id"`
	PolicyType        types.String                       `tfsdk:"policy_type"`
	Priority          types.Int64                        `tfsdk:"priority"`
	SubjectMatchers   []gatewayPolicySubjectMatcherModel `tfsdk:"subject_matchers"`
	UpdatedAt         types.String                       `tfsdk:"updated_at"`
}

var gatewayPolicyActions = []string{
	"block",
}

const (
	gatewayPolicyTypeSpendCap  = "spend_cap"
	gatewayPolicyTypeRateLimit = "rate_limit"
)

var gatewayPolicyTypes = []string{
	gatewayPolicyTypeSpendCap,
	gatewayPolicyTypeRateLimit,
}

// gatewayPolicyConfigModel maps gateway policy config schema data for the terraform configuration.
type gatewayPolicyConfigModel struct {
	SpendCap  *gatewayPolicySpendCapConfigModel  `tfsdk:"spend_cap"`
	RateLimit *gatewayPolicyRateLimitConfigModel `tfsdk:"rate_limit"`
}

// gatewayPolicySpendCapConfigModel maps gateway policy spend cap config schema data for the terraform configuration.
type gatewayPolicySpendCapConfigModel struct {
	LimitUSD types.Float64 `tfsdk:"limit_usd"`
	Window   types.String  `tfsdk:"window"`
}

// gatewayPolicyRateLimitConfigModel maps a gateway policy rate limit config schema data from the terraform configuration.
type gatewayPolicyRateLimitConfigModel struct {
	Version types.Int64                             `tfsdk:"version"`
	Limits  gatewayPolicyRateLimitConfigLimitsModel `tfsdk:"limits"`
}

// gatewayPolicyRateLimitConfigLimitsModel maps a rate limit from the schema to the terraform configuration.
type gatewayPolicyRateLimitConfigLimitsModel struct {
	Requests *gatewayPolicyRateLimitConfigLimitsWindowValue `tfsdk:"requests"`
	Tokens   *gatewayPolicyRateLimitConfigLimitsWindowValue `tfsdk:"tokens"`
}

// gatewayPolicyRateLimitConfigLimitsWindowValue maps a rate limit by window and value from the scham to the terraform configuration.
type gatewayPolicyRateLimitConfigLimitsWindowValue struct {
	Window types.String `tfsdk:"window"`
	Value  types.Int64  `tfsdk:"value"`
}

// gatewayPolicySubjectMatcherModel maps gateway policy subject matcher schema data for the terraform configuration.
type gatewayPolicySubjectMatcherModel struct {
	Key   types.String `tfsdk:"key"`
	Value types.String `tfsdk:"value"`
}

// gatewayPolicyCreateAPI defines the request body for creating a gateway policy.
type gatewayPolicyCreateAPI struct {
	Action          string                           `json:"action"`
	Config          json.RawMessage                  `json:"config"`
	Description     *string                          `json:"description"`
	Enabled         *bool                            `json:"enabled"`
	Name            string                           `json:"name"`
	PolicyType      string                           `json:"policy_type"`
	Priority        *int64                           `json:"priority"`
	SubjectMatchers []gatewayPolicySubjectMatcherAPI `json:"subject_matchers"`
}

// gatewayPolicyUpdateAPI defines the request body for updating a gateway policy.
type gatewayPolicyUpdateAPI struct {
	Action          *string                           `json:"action"`
	Config          json.RawMessage                   `json:"config"`
	Description     *string                           `json:"description"`
	Enabled         *bool                             `json:"enabled"`
	Name            *string                           `json:"name"`
	Priority        *int64                            `json:"priority"`
	SubjectMatchers *[]gatewayPolicySubjectMatcherAPI `json:"subject_matchers"`
}

// gatewayPolicySubjectMatcherAPI is the subject_matchers from the API.
type gatewayPolicySubjectMatcherAPI struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// gatewayPolicySpendCapConfigAPI is the spend_cap config from the API.
type gatewayPolicySpendCapConfigAPI struct {
	LimitUSD float64 `json:"limit_usd"`
	Window   string  `json:"window"`
}

// gatewayPolicyRateLimitConfigAPI is the rate_limit config from the API.
type gatewayPolicyRateLimitConfigAPI struct {
	Version int64                                        `json:"version"`
	Limits  []gatewayPolicyRateLimitConfigWindowValueAPI `json:"limits"`
}

// gatewayPolicyRateLimitConfigLimitAPI is the rate_limit config's limit window and value from the API.
type gatewayPolicyRateLimitConfigWindowValueAPI struct {
	Metric string `json:"metric"`
	Value  int64  `json:"value"`
	Window string `json:"window"`
}

const (
	gatewayPolicyRateLimitMetricRequests string = "requests"
	gatewayPolicyRateLimitMetricTokens   string = "tokens"
)

var gatewaySpendCapWindows = []string{
	"hourly",
	"daily",
	"weekly",
	"monthly",
}

var gatewayPolicyRateLimitWindows = []string{
	"minute",
	"hour",
}

var gatewayRateLimitPolicyConfigVersions = []int64{
	1,
}

// gatewayPolicyGetAPI maps a GatewayPolicyRecord from the admin API.
type gatewayPolicyGetAPI struct {
	ID                string                           `json:"id"`
	OrganizationID    string                           `json:"organization_id"`
	Name              string                           `json:"name"`
	Description       *string                          `json:"description"`
	SubjectMatchers   []gatewayPolicySubjectMatcherAPI `json:"subject_matchers"`
	PolicyType        string                           `json:"policy_type"`
	Config            json.RawMessage                  `json:"config"`
	Action            string                           `json:"action"`
	Priority          int                              `json:"priority"`
	Enabled           bool                             `json:"enabled"`
	CreatedAt         string                           `json:"created_at"`
	UpdatedAt         string                           `json:"updated_at"`
	CreatedBy         *string                          `json:"created_by"`
	IsSystemGenerated bool                             `json:"is_system_generated"`
	ParentPolicyID    *string                          `json:"parent_policy_id"`
}

// data model conversion functions

// terraform model from API responses

// gatewayPolicyModelFromAPI maps the API data to the state data.
func gatewayPolicyModelFromAPI(api gatewayPolicyGetAPI) (gatewayPolicyModel, error) {
	// unmarshal the subject matchers from the API.
	matchers := make([]gatewayPolicySubjectMatcherModel, 0, len(api.SubjectMatchers))
	for _, m := range api.SubjectMatchers {
		matchers = append(matchers, gatewayPolicySubjectMatcherModel{
			Key:   types.StringValue(m.Key),
			Value: types.StringValue(m.Value),
		})
	}
	// unmarshal the config from the API.
	config, err := gatewayPolicyConfigModelFromAPI(api.PolicyType, api.Config)
	if err != nil {
		return gatewayPolicyModel{}, err
	}
	return gatewayPolicyModel{
		Action:            types.StringValue(api.Action),
		Config:            config,
		CreatedAt:         nullableString(api.CreatedAt),
		CreatedBy:         nullableStringPointer(api.CreatedBy),
		Description:       nullableStringPointer(api.Description),
		Enabled:           types.BoolValue(api.Enabled),
		ID:                types.StringValue(api.ID),
		IsSystemGenerated: types.BoolValue(api.IsSystemGenerated),
		Name:              types.StringValue(api.Name),
		OrganizationID:    types.StringValue(api.OrganizationID),
		ParentPolicyID:    nullableStringPointer(api.ParentPolicyID),
		PolicyType:        types.StringValue(api.PolicyType),
		Priority:          types.Int64Value(int64(api.Priority)),
		SubjectMatchers:   matchers,
		UpdatedAt:         nullableString(api.UpdatedAt),
	}, nil
}

// gatewayPolicyConfigModelFromAPI maps the API policy's `config` to `config` in the terraform configuration.
func gatewayPolicyConfigModelFromAPI(policyType string, raw json.RawMessage) (*gatewayPolicyConfigModel, error) {
	switch policyType {
	case gatewayPolicyTypeSpendCap:
		var cfg gatewayPolicySpendCapConfigAPI
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("decode spend_cap config: %w", err)
		}
		return &gatewayPolicyConfigModel{
			SpendCap: &gatewayPolicySpendCapConfigModel{
				Window:   types.StringValue(cfg.Window),
				LimitUSD: types.Float64Value(cfg.LimitUSD),
			},
		}, nil
	case gatewayPolicyTypeRateLimit:
		var cfg gatewayPolicyRateLimitConfigAPI
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("decode rate_limit config: %w", err)
		}
		var requests *gatewayPolicyRateLimitConfigLimitsWindowValue
		var tokens *gatewayPolicyRateLimitConfigLimitsWindowValue
		for _, limit := range cfg.Limits {
			switch limit.Metric {
			case gatewayPolicyRateLimitMetricRequests:
				requests = &gatewayPolicyRateLimitConfigLimitsWindowValue{
					Window: types.StringValue(limit.Window),
					Value:  types.Int64Value(limit.Value),
				}
			case gatewayPolicyRateLimitMetricTokens:
				tokens = &gatewayPolicyRateLimitConfigLimitsWindowValue{
					Window: types.StringValue(limit.Window),
					Value:  types.Int64Value(limit.Value),
				}
			default:
				return nil, fmt.Errorf("unknown metric type: %s", limit.Metric)
			}
		}
		return &gatewayPolicyConfigModel{
			RateLimit: &gatewayPolicyRateLimitConfigModel{
				Version: types.Int64Value(cfg.Version),
				Limits: gatewayPolicyRateLimitConfigLimitsModel{
					Requests: requests,
					Tokens:   tokens,
				},
			},
		}, nil
	default:
		// The API serves policy types this provider cannot represent (guard,
		// route_config, rate_limit, and the default_* variants). Reject them
		// rather than returning a null config, which an import would otherwise
		// write to state for a required attribute.
		return nil, fmt.Errorf(
			"policy_type %q is not supported by this provider; supported types: %s",
			policyType, strings.Join(gatewayPolicyTypes, ", "),
		)
	}
}

// API request models from terraform models

// gatewayPolicyCreateAPIFromModel converts the plan model to the create API request.
func gatewayPolicyCreateAPIFromModel(plan gatewayPolicyModel) (gatewayPolicyCreateAPI, error) {
	policyType, policyConfigAPI, err := gatewayPolicyConfigAPIFromModel(plan)
	if err != nil {
		return gatewayPolicyCreateAPI{}, err
	}

	subjectMatchers, err := gatewayPolicySubjectMatchersAPIFromModel(plan)
	if err != nil {
		return gatewayPolicyCreateAPI{}, err
	}

	return gatewayPolicyCreateAPI{
		Action:          plan.Action.ValueString(),
		Config:          policyConfigAPI,
		Description:     plan.Description.ValueStringPointer(),
		Enabled:         plan.Enabled.ValueBoolPointer(),
		Name:            plan.Name.ValueString(),
		PolicyType:      policyType,
		Priority:        plan.Priority.ValueInt64Pointer(),
		SubjectMatchers: subjectMatchers,
	}, nil
}

// gatewayPolicyUpdateAPIFromModel converts the plan model to the update API request.
func gatewayPolicyUpdateAPIFromModel(plan gatewayPolicyModel) (gatewayPolicyUpdateAPI, error) {
	_, policyConfigAPI, err := gatewayPolicyConfigAPIFromModel(plan)
	if err != nil {
		return gatewayPolicyUpdateAPI{}, err
	}
	subjectMatchers, err := gatewayPolicySubjectMatchersAPIFromModel(plan)
	if err != nil {
		return gatewayPolicyUpdateAPI{}, err
	}
	return gatewayPolicyUpdateAPI{
		Action:          plan.Action.ValueStringPointer(),
		Config:          policyConfigAPI,
		Description:     plan.Description.ValueStringPointer(),
		Enabled:         plan.Enabled.ValueBoolPointer(),
		Name:            plan.Name.ValueStringPointer(),
		Priority:        plan.Priority.ValueInt64Pointer(),
		SubjectMatchers: &subjectMatchers,
	}, nil
}

// gatewayPolicyConfigAPIFromModel converts the plan model to the config API request.
func gatewayPolicyConfigAPIFromModel(plan gatewayPolicyModel) (string, json.RawMessage, error) {
	if plan.Config == nil {
		return "", nil, fmt.Errorf("config is required")
	}
	var policyType string
	var policyConfig any
	switch {
	case plan.Config.SpendCap != nil:
		policyType = gatewayPolicyTypeSpendCap
		policyConfig = gatewayPolicySpendCapConfigAPI{
			Window:   plan.Config.SpendCap.Window.ValueString(),
			LimitUSD: plan.Config.SpendCap.LimitUSD.ValueFloat64(),
		}
	case plan.Config.RateLimit != nil:
		policyType = gatewayPolicyTypeRateLimit
		limits := []gatewayPolicyRateLimitConfigWindowValueAPI{}
		if plan.Config.RateLimit.Limits.Requests != nil {
			limits = append(limits, gatewayPolicyRateLimitConfigWindowValueAPI{
				Metric: gatewayPolicyRateLimitMetricRequests,
				Value:  plan.Config.RateLimit.Limits.Requests.Value.ValueInt64(),
				Window: plan.Config.RateLimit.Limits.Requests.Window.ValueString(),
			})
		}
		if plan.Config.RateLimit.Limits.Tokens != nil {
			limits = append(limits, gatewayPolicyRateLimitConfigWindowValueAPI{
				Metric: gatewayPolicyRateLimitMetricTokens,
				Value:  plan.Config.RateLimit.Limits.Tokens.Value.ValueInt64(),
				Window: plan.Config.RateLimit.Limits.Tokens.Window.ValueString(),
			})
		}
		policyConfig = gatewayPolicyRateLimitConfigAPI{
			Version: plan.Config.RateLimit.Version.ValueInt64(),
			Limits:  limits,
		}
	default:
		return "", nil, fmt.Errorf("invalid policy config")
	}

	policyConfigAPI, err := json.Marshal(policyConfig)
	if err != nil {
		return "", nil, fmt.Errorf("marshal config: %w", err)
	}
	return policyType, policyConfigAPI, nil
}

// gatewayPolicySubjectMatchersAPIFromModel converts the plan model subjects matchers to the subject matchers for an API request.
func gatewayPolicySubjectMatchersAPIFromModel(plan gatewayPolicyModel) ([]gatewayPolicySubjectMatcherAPI, error) {
	if plan.SubjectMatchers == nil {
		return nil, fmt.Errorf("subject matchers are required")
	}
	subjectMatchers := make([]gatewayPolicySubjectMatcherAPI, 0, len(plan.SubjectMatchers))
	for _, subjectMatcher := range plan.SubjectMatchers {
		subjectMatchers = append(subjectMatchers, gatewayPolicySubjectMatcherAPI{
			Key:   subjectMatcher.Key.ValueString(),
			Value: subjectMatcher.Value.ValueString(),
		})
	}
	return subjectMatchers, nil
}

// Terraform resource implementation

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                     = &gatewayPolicyResource{}
	_ resource.ResourceWithConfigure        = &gatewayPolicyResource{}
	_ resource.ResourceWithImportState      = &gatewayPolicyResource{}
	_ resource.ResourceWithConfigValidators = &gatewayPolicyResource{}
)

// NewGatewayPolicyResource is a helper function to simplify the provider implementation.
func NewGatewayPolicyResource() resource.Resource {
	return &gatewayPolicyResource{}
}

// gatewayPolicyResource is the resource implementation.
type gatewayPolicyResource struct {
	client *langsmith.Client
}

// Metadata returns the resource type name.
func (r *gatewayPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gateway_policy"
}

// Schema defines the schema for the resource.
func (r *gatewayPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a LangSmith Gateway Policy",
		Attributes: map[string]schema.Attribute{
			"action": schema.StringAttribute{
				Description: "The action to perform when the policy is violated",
				Validators: []validator.String{
					stringvalidator.OneOf(gatewayPolicyActions...),
				},
				Required: true,
			},
			"config": schema.SingleNestedAttribute{
				Description: "The config of the gateway policy. Exactly one typed child is set.",
				Required:    true,
				Attributes: map[string]schema.Attribute{
					gatewayPolicyTypeSpendCap: schema.SingleNestedAttribute{
						Description: "Spend-cap config when policy_type is spend_cap.",
						Optional:    true,
						Attributes: map[string]schema.Attribute{
							"window": schema.StringAttribute{
								Description: "The time window for the spend cap",
								Validators: []validator.String{
									stringvalidator.OneOf(gatewaySpendCapWindows...),
								},
								Required: true,
							},
							"limit_usd": schema.Float64Attribute{
								Description: "The spend cap amount in USD",
								Required:    true,
							},
						},
					},
					"rate_limit": schema.SingleNestedAttribute{
						Description: "rate-limit config when policy_type is rate_limit",
						Optional:    true,
						Attributes: map[string]schema.Attribute{
							"version": schema.Int64Attribute{
								Description: "The version of the policy configuration",
								Required:    true,
								Validators: []validator.Int64{
									int64validator.OneOf(gatewayRateLimitPolicyConfigVersions...),
								},
							},
							"limits": schema.SingleNestedAttribute{
								Description: "The limit dimensions. At least one is required.",
								Required:    true,
								Attributes: map[string]schema.Attribute{
									gatewayPolicyRateLimitMetricRequests: schema.SingleNestedAttribute{
										Description: "The requests rate limit",
										Optional:    true,
										Attributes: map[string]schema.Attribute{
											"window": schema.StringAttribute{
												Description: "The time window for the rate limit",
												Validators: []validator.String{
													stringvalidator.OneOf(gatewayPolicyRateLimitWindows...),
												},
												Required: true,
											},
											"value": schema.Int64Attribute{
												Description: "The rate limit value",
												Required:    true,
											},
										},
									},
									gatewayPolicyRateLimitMetricTokens: schema.SingleNestedAttribute{
										Description: "The tokens rate limit",
										Optional:    true,
										Attributes: map[string]schema.Attribute{
											"window": schema.StringAttribute{
												Description: "The time window for the rate limit",
												Validators: []validator.String{
													stringvalidator.OneOf(gatewayPolicyRateLimitWindows...),
												},
												Required: true,
											},
											"value": schema.Int64Attribute{
												Description: "The rate limit value",
												Required:    true,
											},
										},
									},
								},
								Validators: []validator.Object{
									objectvalidator.AtLeastOneOf(
										path.MatchRelative().AtParent().AtName(gatewayPolicyRateLimitMetricRequests),
										path.MatchRelative().AtParent().AtName(gatewayPolicyRateLimitMetricTokens),
									),
								},
							},
						},
					},
				},
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp of when the gateway policy was created",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_by": schema.StringAttribute{
				Description: "The ID of the user who created the gateway policy",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			// current_spend_usd is omitted
			// current_usage is omitted
			"description": schema.StringAttribute{
				Description: "The description of the gateway policy",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"enabled": schema.BoolAttribute{
				Description: "Whether the gateway policy is enabled",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"id": schema.StringAttribute{
				Description: "The ID of the gateway policy",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"is_system_generated": schema.BoolAttribute{
				Description:        "Whether the gateway policy is system generated",
				DeprecationMessage: "This field is deprecated and will be removed in a future version.",
				Computed:           true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the gateway policy",
				Required:    true,
			},
			"organization_id": schema.StringAttribute{
				Description: "The ID of the LangSmith organization to which the gateway policy belongs",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			// Updates to a materialized child take ownership of the child, so the API clears parent_policy_id if it is set.
			"parent_policy_id": schema.StringAttribute{
				Description: "The ID of the parent policy",
				Computed:    true,
			},
			"policy_type": schema.StringAttribute{
				Description: "The type of the gateway policy, inferred from which config block is set.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"priority": schema.Int64Attribute{
				Description: "The priority of the gateway policy",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(0),
			},
			"subject_matchers": schema.ListNestedAttribute{
				Description: "The subject matchers of the gateway policy",
				Required:    true,
				Validators: []validator.List{
					listvalidator.SizeBetween(1, 10), // API limit
					listvalidator.UniqueValues(),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"key": schema.StringAttribute{
							MarkdownDescription: `Subject kind. Built-in: organization_id, workspace_id, user_id, or api_key_id.

For a custom X-Gateway-* header, drop the "X-Gateway-" prefix and lowercase the rest, replacing any non [a-z0-9_] char with _ (e.g. header "X-Gateway-My-Internal-Team" matches as key "my_internal_team").`,
							Required: true,
						},
						"value": schema.StringAttribute{
							MarkdownDescription: `Subject id for that kind (e.g. workspace UUID), or the custom header value. Matched exactly (case-sensitive) against the request value.`,
							Required:            true,
						},
					},
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp of when the gateway policy was last updated",
				Computed:    true,
			},
		},
	}
}

func (r *gatewayPolicyResource) ConfigValidators(ctx context.Context) []resource.ConfigValidator {
	expressions := []path.Expression{}
	for _, policyType := range gatewayPolicyTypes {
		expressions = append(expressions, path.MatchRoot("config").AtName(policyType))
	}
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(expressions...),
	}
}

// Create creates the resource and sets the initial Terraform state.
func (r *gatewayPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	// Retrieve values from plan
	var plan gatewayPolicyModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Create the gateway policy
	apiRequest, err := gatewayPolicyCreateAPIFromModel(plan)
	if err != nil {
		resp.Diagnostics.AddError("Failed to build gateway policy request", err.Error())
		return
	}
	var apiResponse gatewayPolicyGetAPI
	apiPath := "api/v1/platform/gateway-policies"
	if err := r.client.Post(ctx, apiPath, apiRequest, &apiResponse); err != nil {
		resp.Diagnostics.AddError("Failed to create gateway policy", err.Error())
		return
	}
	// Map the API response to terraform state data
	state, err := gatewayPolicyModelFromAPI(apiResponse)
	if err != nil {
		resp.Diagnostics.AddError("Failed to decode gateway policy", err.Error())
		return
	}
	// Set the state data
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *gatewayPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	// Get current currentState
	var currentState gatewayPolicyModel
	diags := req.State.Get(ctx, &currentState)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// fetch the refreshed policy from the API by ID.
	var apiData gatewayPolicyGetAPI
	apiPath := fmt.Sprintf("api/v1/platform/gateway-policies/%s", currentState.ID.ValueString())
	if err := r.client.Get(ctx, apiPath, nil, &apiData); err != nil {
		// The policy is gone; drop it from state so the next plan recreates it
		// instead of every refresh failing.
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read gateway policy", err.Error())
		return
	}
	// map the API response to state
	newState, err := gatewayPolicyModelFromAPI(apiData)
	if err != nil {
		resp.Diagnostics.AddError("Failed to decode gateway policy", err.Error())
		return
	}
	// save the refreshed state
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// ImportState imports the resource state from an ID.
func (r *gatewayPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Retrieve import ID and save to id attribute
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *gatewayPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Retrieve values from plan
	var plan gatewayPolicyModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Update the gateway policy
	apiRequest, err := gatewayPolicyUpdateAPIFromModel(plan)
	if err != nil {
		resp.Diagnostics.AddError("Failed to build gateway policy request", err.Error())
		return
	}
	apiPath := fmt.Sprintf("api/v1/platform/gateway-policies/%s", plan.ID.ValueString())
	var apiResponse gatewayPolicyGetAPI
	if err := r.client.Patch(ctx, apiPath, apiRequest, &apiResponse); err != nil {
		resp.Diagnostics.AddError("Failed to update gateway policy", err.Error())
		return
	}
	// Map the API response to terraform state data. The response is authoritative:
	// a PATCH to the API also refreshes updated_at and potentially clears parent_policy_id.
	state, err := gatewayPolicyModelFromAPI(apiResponse)
	if err != nil {
		resp.Diagnostics.AddError("Failed to decode gateway policy", err.Error())
		return
	}
	// save the updated state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *gatewayPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state gatewayPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiPath := fmt.Sprintf("api/v1/platform/gateway-policies/%s", state.ID.ValueString())
	// idempotency: ignore 404 errors.
	if err := r.client.Delete(ctx, apiPath, nil, nil); err != nil && !isLangSmithNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete gateway policy", err.Error())
	}
}

// Configure adds the provider configured client to the resource.
func (r *gatewayPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Add a nil check when handling ProviderData because Terraform
	// sets that data after it calls the ConfigureProvider RPC.
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
