package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	frameworkvalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var (
	_ datasource.DataSource                   = &GatewayPoliciesDataSource{}
	_ datasource.DataSourceWithValidateConfig = &GatewayPoliciesDataSource{}
)

func NewGatewayPoliciesDataSource() datasource.DataSource { return &GatewayPoliciesDataSource{} }

type GatewayPoliciesDataSource struct {
	client *langsmith.Client
}

type gatewayPoliciesDataSourceModel struct {
	ID                  types.String                   `tfsdk:"id"`
	PolicyType          types.String                   `tfsdk:"policy_type"`
	SubjectMatcherKey   types.String                   `tfsdk:"subject_matcher_key"`
	SubjectMatcherValue types.String                   `tfsdk:"subject_matcher_value"`
	Policies            []gatewayPolicyDataSourceModel `tfsdk:"policies"`
}

type gatewayPolicyDataSourceModel struct {
	ID              types.String                       `tfsdk:"id"`
	Name            types.String                       `tfsdk:"name"`
	Description     types.String                       `tfsdk:"description"`
	PolicyType      types.String                       `tfsdk:"policy_type"`
	SubjectMatchers []gatewayPolicySubjectMatcherModel `tfsdk:"subject_matchers"`
	ConfigJSON      types.String                       `tfsdk:"config_json"`
	Action          types.String                       `tfsdk:"action"`
	Priority        types.Int64                        `tfsdk:"priority"`
	Enabled         types.Bool                         `tfsdk:"enabled"`
	OrganizationID  types.String                       `tfsdk:"organization_id"`
	ParentPolicyID  types.String                       `tfsdk:"parent_policy_id"`
	CurrentSpendUSD types.Float64                      `tfsdk:"current_spend_usd"`
	CreatedAt       types.String                       `tfsdk:"created_at"`
	UpdatedAt       types.String                       `tfsdk:"updated_at"`
}

func (d *GatewayPoliciesDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gateway_policies"
}

func (d *GatewayPoliciesDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists LangSmith LLM Gateway policies in the organization, of every type: spend caps, " +
			"rate limits, guard policies and route configs.\n\n" +
			"This is the way to observe policies Terraform does not manage, including the caps the gateway " +
			"materializes for individual subjects from a `default_spend_cap`. Those carry a `parent_policy_id` " +
			"and appear only once a request for that subject has been served.\n\n" +
			"~> **Policies created by LangSmith itself are not listed.** The API hides system-generated rows from " +
			"external callers, so a default spend cap that LangSmith created, and the children materialized from " +
			"it, stay invisible here. Policies created through the API, the UI or Terraform are listed, as are " +
			"the children materialized from those.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Identifier for this result set, derived from the filters.",
			},
			"policy_type": schema.StringAttribute{
				Optional:            true,
				Validators:          []frameworkvalidator.String{oneOfStringValidator{values: gatewayPolicyTypes}},
				MarkdownDescription: "Return only policies of this type. Omit to list every type.",
			},
			"subject_matcher_key": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Return only policies carrying this subject matcher key. Must be set together with `subject_matcher_value`.",
			},
			"subject_matcher_value": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Return only policies carrying this subject matcher value. Must be set together with `subject_matcher_key`.",
			},
			"policies": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Matching policies.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "Policy ID. Pass it to `terraform import` to bring an unmanaged policy under Terraform.",
					},
					"name": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "Policy name.",
					},
					"description": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "Policy description.",
					},
					"policy_type": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "Policy type, which decides the shape of `config_json`.",
					},
					"subject_matchers": schema.ListNestedAttribute{
						Computed:            true,
						MarkdownDescription: "Subject matchers selecting the requests this policy applies to.",
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"key": schema.StringAttribute{
								Computed:            true,
								MarkdownDescription: "Matcher key.",
							},
							"value": schema.StringAttribute{
								Computed:            true,
								MarkdownDescription: "Matcher value. Blank on a `default_spend_cap`, which applies to every subject of its key.",
							},
						}},
					},
					"config_json": schema.StringAttribute{
						Computed: true,
						MarkdownDescription: "Raw policy config as JSON, since its shape depends on `policy_type`. " +
							"Decode it with `jsondecode`: a spend cap carries `window` and `limit_usd`.",
					},
					"action": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "Enforcement action when the policy is triggered.",
					},
					"priority": schema.Int64Attribute{
						Computed:            true,
						MarkdownDescription: "Policy priority. Lower values take precedence when multiple policies match.",
					},
					"enabled": schema.BoolAttribute{
						Computed:            true,
						MarkdownDescription: "Whether the policy is enforced.",
					},
					"organization_id": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "Organization that owns the policy.",
					},
					"parent_policy_id": schema.StringAttribute{
						Computed: true,
						MarkdownDescription: "Set when the gateway materialized this policy from a default, to that default's ID. " +
							"Such a policy is owned by its parent: editing it, including by managing it with Terraform, detaches it permanently.",
					},
					"current_spend_usd": schema.Float64Attribute{
						Computed:            true,
						MarkdownDescription: "Spend accumulated in the policy's active window. Only populated for spend caps.",
					},
					"created_at": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "Creation timestamp.",
					},
					"updated_at": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "Last update timestamp.",
					},
				}},
			},
		},
	}
}

// ValidateConfig rejects half a matcher filter. The API pairs the key with the
// value in one containment check, so a lone key would silently filter on a
// blank value, which is the shape only default policies have.
func (d *GatewayPoliciesDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	var config gatewayPoliciesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key, value := config.SubjectMatcherKey, config.SubjectMatcherValue
	if key.IsUnknown() || value.IsUnknown() {
		return
	}
	if key.IsNull() != value.IsNull() {
		resp.Diagnostics.AddError(
			"Incomplete Subject Matcher Filter",
			"Set subject_matcher_key and subject_matcher_value together, or neither. "+
				"The API matches them as one key/value pair.",
		)
	}
}

func (d *GatewayPoliciesDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureDataSourceClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *GatewayPoliciesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data gatewayPoliciesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	policies, query, err := d.listPolicies(ctx, data)
	if err != nil {
		resp.Diagnostics.AddError("Unable to List LangSmith Gateway Policies", err.Error())
		return
	}

	data.ID = gatewayPoliciesDataSourceID(query)
	data.Policies = gatewayPolicyDataSourceModels(policies)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// listPolicies turns the configured filters into a list request and returns the
// matching policies alongside the query that produced them.
func (d *GatewayPoliciesDataSource) listPolicies(ctx context.Context, data gatewayPoliciesDataSourceModel) ([]gatewayPolicyAPI, gatewayPolicyListQuery, error) {
	query := gatewayPolicyListQuery{
		PolicyType:          data.PolicyType.ValueString(),
		SubjectMatcherKey:   data.SubjectMatcherKey.ValueString(),
		SubjectMatcherValue: data.SubjectMatcherValue.ValueString(),
	}
	var result []gatewayPolicyAPI
	if err := d.client.Get(ctx, gatewayPoliciesPath, query, &result); err != nil {
		return nil, query, err
	}
	return result, query, nil
}

// gatewayPoliciesDataSourceID identifies the result set by the filters that
// produced it, so two data sources with different filters do not look alike.
func gatewayPoliciesDataSourceID(query gatewayPolicyListQuery) types.String {
	encoded := query.URLQuery().Encode()
	if encoded == "" {
		return types.StringValue("gateway-policies")
	}
	return types.StringValue("gateway-policies?" + encoded)
}

func gatewayPolicyDataSourceModels(values []gatewayPolicyAPI) []gatewayPolicyDataSourceModel {
	out := make([]gatewayPolicyDataSourceModel, 0, len(values))
	for _, value := range values {
		model := gatewayPolicyDataSourceModel{
			ID:              types.StringValue(value.ID),
			Name:            types.StringValue(value.Name),
			Description:     nullableStringPointer(value.Description),
			PolicyType:      types.StringValue(value.PolicyType),
			SubjectMatchers: gatewayPolicyMatcherModelsFromAPI(value.SubjectMatchers),
			ConfigJSON:      nullableString(string(value.Config)),
			Action:          types.StringValue(value.Action),
			Priority:        types.Int64Value(int64(value.Priority)),
			Enabled:         types.BoolValue(value.Enabled),
			OrganizationID:  types.StringValue(value.OrganizationID),
			ParentPolicyID:  nullableStringPointer(value.ParentPolicyID),
			CurrentSpendUSD: types.Float64Null(),
			CreatedAt:       nullableString(value.CreatedAt),
			UpdatedAt:       nullableString(value.UpdatedAt),
		}
		if value.CurrentSpendUSD != nil {
			model.CurrentSpendUSD = types.Float64Value(*value.CurrentSpendUSD)
		}
		out = append(out, model)
	}
	return out
}
