package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &gatewayPolicyDataSource{}
	_ datasource.DataSourceWithConfigure = &gatewayPolicyDataSource{}
)

type (
	gatewayPolicyAction         string
	gatewayPolicyType           string
	gatewayPolicySpendCapWindow string
)

const (
	gatewayPolicyActionBlock gatewayPolicyAction = "block"

	gatewayPolicyTypeSpendCap gatewayPolicyType = "spend_cap"

	gatewayPolicySpendCapWindowHour  gatewayPolicySpendCapWindow = "hour"
	gatewayPolicySpendCapWindowDay   gatewayPolicySpendCapWindow = "day"
	gatewayPolicySpendCapWindowWeek  gatewayPolicySpendCapWindow = "week"
	gatewayPolicySpendCapWindowMonth gatewayPolicySpendCapWindow = "month"
)

// NewGatewayPolicyDataSource is a helper function to simplify the provider implementation.
func NewGatewayPolicyDataSource() datasource.DataSource {
	return &gatewayPolicyDataSource{}
}

// gatewayPolicyDataSource is the data source implementation.
type gatewayPolicyDataSource struct {
	client *langsmith.Client
}

// Metadata returns the data source type name.
func (d *gatewayPolicyDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gateway_policy"
}

// Schema defines the schema for the data source.
func (d *gatewayPolicyDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads a LangSmith Gateway Policy",
		Attributes: map[string]schema.Attribute{
			"action": schema.StringAttribute{
				Description: "The action to perform when the policy is violated",
				Computed:    true,
			},
			"config": schema.SingleNestedAttribute{
				Description: "The config of the gateway policy. Exactly one typed child is set.",
				Computed:    true,
				Attributes: map[string]schema.Attribute{
					"spend_cap": schema.SingleNestedAttribute{
						Description: "Spend-cap config when policy_type is spend_cap.",
						Computed:    true,
						Attributes: map[string]schema.Attribute{
							"window": schema.StringAttribute{
								Description: "The time window for the spend cap",
								Computed:    true,
							},
							"limit_usd": schema.Float64Attribute{
								Description: "The spend cap amount in USD",
								Computed:    true,
							},
						},
					},
				},
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp of when the gateway policy was created",
				Computed:    true,
			},
			"created_by": schema.StringAttribute{
				Description: "The ID of the user who created the gateway policy",
				Computed:    true,
			},
			// current_spend_usd is omitted
			// current_usage is omitted
			"description": schema.StringAttribute{
				Description: "The description of the gateway policy",
				Computed:    true,
			},
			"enabled": schema.BoolAttribute{
				Description: "Whether the gateway policy is enabled",
				Computed:    true,
			},
			"id": schema.StringAttribute{
				Description: "The ID of the gateway policy",
				Required:    true,
			},
			"is_system_generated": schema.BoolAttribute{
				Description: "Whether the gateway policy is system generated",
				Computed:    true,
			},
			"name": schema.StringAttribute{
				Description: "The name of the gateway policy",
				Computed:    true,
			},
			"organization_id": schema.StringAttribute{
				Description: "The ID of the LangSmith organization to which the gateway policy belongs",
				Computed:    true,
			},
			"parent_policy_id": schema.StringAttribute{
				Description: "The ID of the parent policy",
				Computed:    true,
			},
			"policy_type": schema.StringAttribute{
				Description: "The type of the gateway policy. Must match the type with the config",
				Computed:    true,
			},
			"priority": schema.Int64Attribute{
				Description: "The priority of the gateway policy",
				Computed:    true,
			},
			"subject_matchers": schema.ListNestedAttribute{
				Description: "The subject matchers of the gateway policy",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"key": schema.StringAttribute{
							Description: "The key of the subject matcher",
							Computed:    true,
						},
						"value": schema.StringAttribute{
							Description: "The value of the subject matcher",
							Computed:    true,
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

// Read refreshes the Terraform state with the latest data.
func (d *gatewayPolicyDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	// get the ID from the user's input in their terraform configuration.
	var configData gatewayPolicyModel
	diags := req.Config.Get(ctx, &configData)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// get the policy from the API by ID.
	var apiData gatewayPolicyGetAPI
	path := fmt.Sprintf("api/v1/platform/gateway-policies/%s", configData.ID.ValueString())
	if err := d.client.Get(ctx, path, nil, &apiData); err != nil {
		resp.Diagnostics.AddError("Failed to read gateway policy", err.Error())
		return
	}
	// map the API data to the state data.
	state, err := gatewayPolicyModelFromAPI(apiData)
	if err != nil {
		resp.Diagnostics.AddError("Failed to decode gateway policy", err.Error())
		return
	}
	// set the state data.
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

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

// gatewayPolicyConfigModelFromAPI maps the API policy's `config“ to `config` in the terraform configuration.
func gatewayPolicyConfigModelFromAPI(policyType string, raw json.RawMessage) (*gatewayPolicyConfigModel, error) {
	switch policyType {
	case string(gatewayPolicyTypeSpendCap):
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
	default:
		return nil, nil
	}
}

// Configure adds the provider configured client to the data source.
func (d *gatewayPolicyDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureDataSourceClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}
