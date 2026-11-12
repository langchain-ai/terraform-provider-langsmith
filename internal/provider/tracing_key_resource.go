package provider

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	frameworkvalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

var (
	_ resource.Resource                = &tracingKeyResource{}
	_ resource.ResourceWithConfigure   = &tracingKeyResource{}
	_ resource.ResourceWithImportState = &tracingKeyResource{}
)

func NewTracingKeyResource() resource.Resource { return &tracingKeyResource{} }

type tracingKeyResource struct{ client *langsmith.Client }

type tracingKeyResourceModel struct {
	ID             types.String `tfsdk:"id"`
	Description    types.String `tfsdk:"description"`
	Workspaces     types.Set    `tfsdk:"workspaces"`
	ExpiresAt      types.String `tfsdk:"expires_at"`
	Key            types.String `tfsdk:"key"`
	ShortKey       types.String `tfsdk:"short_key"`
	CreatedAt      types.String `tfsdk:"created_at"`
	CreatedBy      types.String `tfsdk:"created_by"`
	LastUsedAt     types.String `tfsdk:"last_used_at"`
	WorkspaceNames types.Set    `tfsdk:"workspace_names"`
}

type tracingKeyAPIResponse struct {
	serviceKeyAPIResponse
	WorkspaceIDs []string `json:"workspace_ids"`
	IsTracingKey bool     `json:"is_tracing_key"`
	Key          string   `json:"key"`
}

func (r *tracingKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tracing_key"
}

func (r *tracingKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a fixed-scope, ingestion-only LangSmith tracing key. Configure the provider with a control-plane API URL supporting service-account tracing-key management. The caller needs `workspaces:tracing-keys:create` in every selected workspace to create, and `workspaces:manage-keys` in every selected workspace or `organization:service-key:manage` to refresh, import, and delete. Human creators retain their existing management rights. Organization issuance and expiry policies apply. Changing description, workspace scope, or expiry replaces the key. The one-time secret is stored in Terraform state; protect state accordingly.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Tracing key ID."},
			"description": schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, MarkdownDescription: "Key description. Changing this replaces the key."},
			"workspaces": schema.SetAttribute{
				Required: true, ElementType: types.StringType,
				Validators: []frameworkvalidator.Set{
					setvalidator.SizeAtLeast(1),
					setvalidator.ValueStringsAre(stringvalidator.RegexMatches(
						regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`),
						"must be a lowercase canonical UUID with hyphens",
					)),
				},
				PlanModifiers:       []planmodifier.Set{setplanmodifier.RequiresReplace()},
				MarkdownDescription: "Nonempty set of workspace IDs as lowercase canonical UUIDs with hyphens. Scope never expands automatically. Changing this replaces the key.",
			},
			"expires_at": schema.StringAttribute{
				Optional: true,
				Validators: []frameworkvalidator.String{stringvalidator.RegexMatches(
					regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,6})?(Z|[+-]\d{2}:\d{2})$`),
					"must be an RFC3339 timestamp with at most six fractional digits (microsecond precision)",
				)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "Expiry timestamp in RFC3339 format with at most six fractional digits (microsecond precision). Required when organization or deployment policy limits key lifetime. Changing this replaces the key.",
			},
			"key":             schema.StringAttribute{Computed: true, Sensitive: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "One-time secret, available only after creation. Import cannot recover it."},
			"short_key":       schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Redacted key."},
			"created_at":      schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Creation timestamp."},
			"created_by":      schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Human creator when available. Unset for service-account creation."},
			"last_used_at":    schema.StringAttribute{Computed: true, MarkdownDescription: "Last usage timestamp, when available."},
			"workspace_names": schema.SetAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Names of the selected workspaces."},
		},
	}
}

func (r *tracingKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *tracingKeyResource) requireSupport(ctx context.Context, diagnostics *diag.Diagnostics) bool {
	var info struct {
		InstanceFlags struct {
			Supported bool `json:"tracing_keys_service_account_management_supported"`
		} `json:"instance_flags"`
	}
	if err := r.client.Get(ctx, "api/v1/info", nil, &info); err != nil {
		diagnostics.AddError("Unable to verify tracing key support", err.Error())
		return false
	}
	if !info.InstanceFlags.Supported {
		diagnostics.AddError("Tracing key management unsupported", "Upgrade LangSmith and configure the provider with the control-plane API URL. The server must advertise tracing_keys_service_account_management_supported. Existing state is retained.")
		return false
	}
	return true
}

func (r *tracingKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var config tracingKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() || !r.requireSupport(ctx, &resp.Diagnostics) {
		return
	}
	var workspaces []string
	resp.Diagnostics.Append(config.Workspaces.ElementsAs(ctx, &workspaces, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	payload := struct {
		Description string   `json:"description"`
		Workspaces  []string `json:"workspaces"`
		ExpiresAt   *string  `json:"expires_at,omitempty"`
	}{config.Description.ValueString(), workspaces, config.ExpiresAt.ValueStringPointer()}
	var result tracingKeyAPIResponse
	if err := r.client.Post(ctx, "api/v1/orgs/current/tracing-keys", payload, &result, option.WithMaxRetries(0)); err != nil {
		resp.Diagnostics.AddError("Unable to create tracing key", err.Error())
		return
	}
	model, diagnostics := tracingKeyModelFromAPI(ctx, result)
	resp.Diagnostics.Append(diagnostics...)
	if resp.Diagnostics.HasError() {
		return
	}
	model.Key = types.StringValue(result.Key)
	model.ExpiresAt = config.ExpiresAt
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *tracingKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state tracingKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !r.requireSupport(ctx, &resp.Diagnostics) {
		return
	}
	var result tracingKeyAPIResponse
	if err := r.client.Get(ctx, tracingKeyPath(state.ID.ValueString()), nil, &result); err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
		} else {
			resp.Diagnostics.AddError("Unable to read tracing key", err.Error())
		}
		return
	}
	if !result.IsTracingKey || result.ID != state.ID.ValueString() {
		resp.Diagnostics.AddError("Invalid tracing key response", "The server did not return the requested tracing key. Existing state is retained.")
		return
	}
	model, diagnostics := tracingKeyModelFromAPI(ctx, result)
	resp.Diagnostics.Append(diagnostics...)
	if resp.Diagnostics.HasError() {
		return
	}
	model.Key = state.Key
	// Equivalent RFC3339 spellings must not trigger replacement after refresh.
	oldExpiry, oldErr := time.Parse(time.RFC3339Nano, state.ExpiresAt.ValueString())
	newExpiry, newErr := time.Parse(time.RFC3339Nano, model.ExpiresAt.ValueString())
	if oldErr == nil && newErr == nil && oldExpiry.Equal(newExpiry) {
		model.ExpiresAt = state.ExpiresAt
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *tracingKeyResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Tracing keys are immutable", "Changes to description, expiry, or workspaces require replacement.")
}

func (r *tracingKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state tracingKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !r.requireSupport(ctx, &resp.Diagnostics) {
		return
	}
	if err := r.client.Delete(ctx, tracingKeyPath(state.ID.ValueString()), nil, nil); err != nil {
		resp.Diagnostics.AddError("Unable to delete tracing key", err.Error())
	}
}

func (r *tracingKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func tracingKeyPath(id string) string {
	return "api/v1/orgs/current/tracing-keys/" + url.PathEscape(id)
}

func tracingKeyModelFromAPI(ctx context.Context, result tracingKeyAPIResponse) (tracingKeyResourceModel, diag.Diagnostics) {
	workspaces, diagnostics := types.SetValueFrom(ctx, types.StringType, result.WorkspaceIDs)
	names, nameDiagnostics := types.SetValueFrom(ctx, types.StringType, result.WorkspaceNames)
	diagnostics.Append(nameDiagnostics...)
	expiresAt := types.StringPointerValue(result.ExpiresAt)
	if timestamp, err := time.Parse(time.RFC3339Nano, expiresAt.ValueString()); err == nil {
		expiresAt = types.StringValue(timestamp.UTC().Format(time.RFC3339Nano))
	}
	return tracingKeyResourceModel{
		ID: types.StringValue(result.ID), Description: types.StringValue(result.Description),
		Workspaces: workspaces, WorkspaceNames: names,
		ExpiresAt: expiresAt,
		Key:       types.StringNull(), ShortKey: types.StringValue(result.ShortKey),
		CreatedAt: types.StringPointerValue(result.CreatedAt), CreatedBy: types.StringPointerValue(result.CreatedBy),
		LastUsedAt: types.StringPointerValue(result.LastUsedAt),
	}, diagnostics
}
