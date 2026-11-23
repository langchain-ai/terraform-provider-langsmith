package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	frameworkvalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var (
	_ resource.Resource                = &ConnectionResource{}
	_ resource.ResourceWithImportState = &ConnectionResource{}
)

const connectionsPath = "v1/connections"

func connectionResourcePath(id string) string {
	return fmt.Sprintf("%s/%s", connectionsPath, id)
}

const (
	connectionKindTunnel = "tunnel"
	connectionKindRelay  = "relay"
)

func NewTunnelResource() resource.Resource {
	return &ConnectionResource{kind: connectionKindTunnel}
}

func NewRelayResource() resource.Resource {
	return &ConnectionResource{kind: connectionKindRelay}
}

// ConnectionResource backs langsmith_tunnel and langsmith_relay, which share the
// /v1/connections API and differ only in the connection kind.
type ConnectionResource struct {
	client *langsmith.Client
	kind   string
}

type connectionResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	Status      types.String `tfsdk:"status"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

type connectionCreatePayload struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Kind        string  `json:"kind"`
}

type connectionUpdatePayload struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Enabled     *bool   `json:"enabled,omitempty"`
}

type connectionAPI struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Kind        string  `json:"kind"`
	Status      string  `json:"status"`
	Enabled     bool    `json:"enabled"`
	Personal    bool    `json:"personal"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

func (r *ConnectionResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.kind
}

func (r *ConnectionResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	summary := "Manages an organization LangSmith tunnel, which lets LangSmith reach named services on your network. Run the connector with `langsmith tunnel connect <id>`."
	idDescription := "Tunnel ID; pass it to `langsmith tunnel connect`."
	if r.kind == connectionKindRelay {
		summary = "Manages an organization LangSmith relay, which sends sandbox traffic for chosen hosts out through your network. Run the connector with `langsmith relay connect <id>`."
		idDescription = "Relay ID; pass it to `langsmith relay connect` and to sandbox proxy relay rules (`relay_id`)."
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: summary + " Import with `<id>`; personal connections are rejected.\n\n" +
			"Private network connections must be enabled for the organization, and the API key needs `organization:infrastructure:manage`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: idDescription,
			},
			"name": schema.StringAttribute{
				Required:            true,
				Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}, stringvalidator.LengthAtMost(256)},
				MarkdownDescription: "Connection name, unique among the organization's tunnels and relays. Renaming updates in place.",
			},
			"description": schema.StringAttribute{
				Optional:            true,
				Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
				MarkdownDescription: "Optional description.",
			},
			"enabled": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Whether the connection accepts traffic. Defaults to `true`.",
			},
			"status": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Connector status reported by LangSmith: `pending`, `active` or `inactive`.",
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
		},
	}
}

func (r *ConnectionResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *ConnectionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan connectionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	model, err := r.createConnection(ctx, plan)
	if !model.ID.IsNull() && model.ID.ValueString() != "" {
		resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith "+r.title(), err.Error())
	}
}

func (r *ConnectionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state connectionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	model, err := r.readConnection(ctx, state.ID.ValueString())
	if err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith "+r.title(), err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *ConnectionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state connectionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	model, err := r.updateConnection(ctx, state.ID.ValueString(), plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith "+r.title(), err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *ConnectionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state connectionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.deleteConnection(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to Delete LangSmith "+r.title(), err.Error())
	}
}

func (r *ConnectionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID == "" {
		resp.Diagnostics.AddError("Invalid "+r.title()+" Import ID", "Use <id>.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *ConnectionResource) createConnection(ctx context.Context, plan connectionResourceModel) (connectionResourceModel, error) {
	var result connectionAPI
	payload := connectionCreatePayload{
		Name:        plan.Name.ValueString(),
		Description: optionalStringPointer(plan.Description),
		Kind:        r.kind,
	}
	if err := r.client.Post(ctx, connectionsPath, payload, &result); err != nil {
		return connectionResourceModel{}, err
	}
	if result.ID == "" {
		return connectionResourceModel{}, errors.New("LangSmith did not return a connection ID")
	}
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() && plan.Enabled.ValueBool() != result.Enabled {
		enabled := plan.Enabled.ValueBool()
		created := connectionModelFromAPI(result)
		if err := r.client.Patch(ctx, connectionResourcePath(result.ID), connectionUpdatePayload{Enabled: &enabled}, &result); err != nil {
			return created, fmt.Errorf("connection %s was created but setting enabled=%t failed: %w", result.ID, enabled, err)
		}
	}
	return connectionModelFromAPI(result), nil
}

func (r *ConnectionResource) readConnection(ctx context.Context, id string) (connectionResourceModel, error) {
	var result connectionAPI
	if err := r.client.Get(ctx, connectionResourcePath(id), nil, &result); err != nil {
		return connectionResourceModel{}, err
	}
	if result.Kind != r.kind {
		return connectionResourceModel{}, fmt.Errorf("connection %s is a %s, not a %s; manage it with langsmith_%s", id, result.Kind, r.kind, result.Kind)
	}
	if result.Personal {
		return connectionResourceModel{}, fmt.Errorf("connection %s is a member's personal connection; only organization connections can be managed with Terraform", id)
	}
	return connectionModelFromAPI(result), nil
}

func (r *ConnectionResource) updateConnection(ctx context.Context, id string, plan connectionResourceModel) (connectionResourceModel, error) {
	name := plan.Name.ValueString()
	description := plan.Description.ValueString()
	payload := connectionUpdatePayload{Name: &name, Description: &description}
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
		enabled := plan.Enabled.ValueBool()
		payload.Enabled = &enabled
	}
	var result connectionAPI
	if err := r.client.Patch(ctx, connectionResourcePath(id), payload, &result); err != nil {
		return connectionResourceModel{}, err
	}
	return connectionModelFromAPI(result), nil
}

func (r *ConnectionResource) deleteConnection(ctx context.Context, id string) error {
	if err := r.client.Delete(ctx, connectionResourcePath(id), nil, nil); err != nil && !isLangSmithNotFound(err) {
		return err
	}
	return nil
}

func (r *ConnectionResource) title() string {
	if r.kind == connectionKindRelay {
		return "Relay"
	}
	return "Tunnel"
}

func connectionModelFromAPI(api connectionAPI) connectionResourceModel {
	description := types.StringNull()
	if api.Description != nil && *api.Description != "" {
		description = types.StringValue(*api.Description)
	}
	return connectionResourceModel{
		ID:          types.StringValue(api.ID),
		Name:        types.StringValue(api.Name),
		Description: description,
		Enabled:     types.BoolValue(api.Enabled),
		Status:      nullableString(api.Status),
		CreatedAt:   nullableString(api.CreatedAt),
		UpdatedAt:   nullableString(api.UpdatedAt),
	}
}
