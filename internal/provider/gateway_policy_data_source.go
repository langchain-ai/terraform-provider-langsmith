package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/langchain-ai/langsmith-go"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ datasource.DataSource              = &gatewayPolicyDataSource{}
	_ datasource.DataSourceWithConfigure = &gatewayPolicyDataSource{}
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

// Configure adds the provider configured client to the data source.
func (d *gatewayPolicyDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureDataSourceClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}
