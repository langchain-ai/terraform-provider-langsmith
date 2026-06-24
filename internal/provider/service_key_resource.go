package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var (
	_ resource.Resource                = &ServiceKeyResource{}
	_ resource.ResourceWithImportState = &ServiceKeyResource{}
)

var errServiceKeyNotFound = errors.New("service key not found")

const serviceKeysPath = "api/v1/orgs/current/service-keys"

func serviceKeyResourcePath(id string) string {
	return fmt.Sprintf("%s/%s", serviceKeysPath, id)
}

func NewServiceKeyResource() resource.Resource {
	return &ServiceKeyResource{}
}

type ServiceKeyResource struct {
	client *langsmith.Client
}

type serviceKeyResourceModel struct {
	ID                   types.String `tfsdk:"id"`
	Description          types.String `tfsdk:"description"`
	ExpiresAt            types.String `tfsdk:"expires_at"`
	Workspaces           []string     `tfsdk:"workspaces"`
	DefaultWorkspaceID   types.String `tfsdk:"default_workspace_id"`
	RoleID               types.String `tfsdk:"role_id"`
	OrgRoleID            types.String `tfsdk:"org_role_id"`
	Key                  types.String `tfsdk:"key"`
	ShortKey             types.String `tfsdk:"short_key"`
	CreatedAt            types.String `tfsdk:"created_at"`
	LastUsedAt           types.String `tfsdk:"last_used_at"`
	AccessScope          types.String `tfsdk:"access_scope"`
	WorkspaceNames       []string     `tfsdk:"workspace_names"`
	DefaultWorkspaceName types.String `tfsdk:"default_workspace_name"`
}

// serviceKeyCreatePayload is the POST /orgs/current/service-keys request body.
// Optional fields use omitempty so unset values are not sent to the API.
type serviceKeyCreatePayload struct {
	Description        string   `json:"description,omitempty"`
	ExpiresAt          string   `json:"expires_at,omitempty"`
	Workspaces         []string `json:"workspaces,omitempty"`
	DefaultWorkspaceID string   `json:"default_workspace_id,omitempty"`
	RoleID             string   `json:"role_id,omitempty"`
	OrgRoleID          string   `json:"org_role_id,omitempty"`
}

// serviceKeyUpdatePayload is the PATCH request body. Only role assignment is
// updatable in place; pointers let us send explicit null to clear a role.
type serviceKeyUpdatePayload struct {
	RoleID    *string `json:"role_id"`
	OrgRoleID *string `json:"org_role_id"`
}

// serviceKeyAPI models the service-key response. Key is only ever populated by
// the create response; list/patch/delete responses omit it.
type serviceKeyAPI struct {
	ID                   string   `json:"id"`
	Key                  string   `json:"key"`
	ShortKey             string   `json:"short_key"`
	Description          string   `json:"description"`
	CreatedAt            string   `json:"created_at"`
	LastUsedAt           string   `json:"last_used_at"`
	ExpiresAt            string   `json:"expires_at"`
	AccessScope          string   `json:"access_scope"`
	RoleID               string   `json:"role_id"`
	OrgRoleID            string   `json:"org_role_id"`
	WorkspaceNames       []string `json:"workspace_names"`
	DefaultWorkspaceName string   `json:"default_workspace_name"`
}

func (r *ServiceKeyResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_key"
}

func (r *ServiceKeyResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an organization-scoped LangSmith service key.\n\n" +
			"The secret (`key`) is returned by LangSmith only once, at creation, and is " +
			"therefore stored in Terraform state. Use encrypted remote state and consider " +
			"writing `key` straight into a secret manager. Managing org service keys requires " +
			"the provider to be authenticated with an organization-admin-capable credential.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Service key ID.",
			},
			"description": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("Default API key"),
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "Human-readable description. Changing it replaces the key.",
			},
			"expires_at": schema.StringAttribute{
				Optional:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "Optional expiry as an RFC3339 timestamp. Changing it replaces the key.",
			},
			"workspaces": schema.SetAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				PlanModifiers:       []planmodifier.Set{setplanmodifier.RequiresReplace()},
				MarkdownDescription: "Optional set of workspace IDs to scope the key to. Omit for an organization-wide key. Changing it replaces the key.",
			},
			"default_workspace_id": schema.StringAttribute{
				Optional:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "Optional default workspace ID for the key. Changing it replaces the key.",
			},
			"role_id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Optional workspace role ID assigned to the key. Updatable in place.",
			},
			"org_role_id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Optional organization role ID assigned to the key. Updatable in place.",
			},
			"key": schema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "The secret service key. Returned only at creation and stored in state. Not recoverable on import.",
			},
			"short_key": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Masked key prefix.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Creation timestamp.",
			},
			"last_used_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp the key was last used, if ever.",
			},
			"access_scope": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Access scope of the key (organization or workspace).",
			},
			"workspace_names": schema.ListAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Names of the workspaces the key is scoped to.",
			},
			"default_workspace_name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Name of the default workspace for the key.",
			},
		},
	}
}

func (r *ServiceKeyResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *ServiceKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	model, err := r.createServiceKey(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Service Key", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *ServiceKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serviceKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	model, err := r.readServiceKey(ctx, state.ID.ValueString(), state)
	if err != nil {
		if isLangSmithNotFound(err) || errors.Is(err, errServiceKeyNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Service Key", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *ServiceKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan serviceKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state serviceKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := firstNonEmpty(state.ID.ValueString(), plan.ID.ValueString())
	if id == "" {
		resp.Diagnostics.AddError("Unable to Update LangSmith Service Key", "Missing service key ID in plan and state.")
		return
	}

	model, err := r.updateServiceKey(ctx, id, plan, state)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Service Key", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *ServiceKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serviceKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.deleteServiceKey(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to Delete LangSmith Service Key", err.Error())
		return
	}
}

// ImportState hydrates metadata from the key ID. The secret (key) is never
// returned by the API after creation, so it stays null on imported resources.
func (r *ServiceKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *ServiceKeyResource) createServiceKey(ctx context.Context, plan serviceKeyResourceModel) (serviceKeyResourceModel, error) {
	var result serviceKeyAPI
	// The create response is the only place the secret is returned, so capture it
	// directly instead of re-reading through the list endpoint.
	if err := r.client.Post(ctx, serviceKeysPath, serviceKeyCreatePayloadFromModel(plan), &result); err != nil {
		return serviceKeyResourceModel{}, err
	}
	if result.ID == "" {
		return serviceKeyResourceModel{}, errors.New("LangSmith did not return a service key ID")
	}
	if result.Key == "" {
		return serviceKeyResourceModel{}, errors.New("LangSmith did not return the service key secret")
	}
	return serviceKeyModelFromAPI(result, plan), nil
}

func (r *ServiceKeyResource) readServiceKey(ctx context.Context, id string, previous serviceKeyResourceModel) (serviceKeyResourceModel, error) {
	keys, err := r.listServiceKeys(ctx)
	if err != nil {
		return serviceKeyResourceModel{}, err
	}
	found, err := findServiceKeyByID(keys, id)
	if err != nil {
		return serviceKeyResourceModel{}, err
	}
	return serviceKeyModelFromAPI(found, previous), nil
}

func (r *ServiceKeyResource) updateServiceKey(ctx context.Context, id string, plan serviceKeyResourceModel, state serviceKeyResourceModel) (serviceKeyResourceModel, error) {
	var result serviceKeyAPI
	if err := r.client.Patch(ctx, serviceKeyResourcePath(id), serviceKeyUpdatePayloadFromModel(plan), &result); err != nil {
		return serviceKeyResourceModel{}, err
	}
	// Re-read so the refreshed role assignment is reflected; preserve the secret
	// and config-only inputs by carrying prior state forward.
	return r.readServiceKey(ctx, id, state)
}

func (r *ServiceKeyResource) deleteServiceKey(ctx context.Context, id string) error {
	if err := r.client.Delete(ctx, serviceKeyResourcePath(id), nil, nil); err != nil {
		if isLangSmithNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

func (r *ServiceKeyResource) listServiceKeys(ctx context.Context) ([]serviceKeyAPI, error) {
	var keys []serviceKeyAPI
	if err := r.client.Get(ctx, serviceKeysPath, nil, &keys); err != nil {
		return nil, err
	}
	return keys, nil
}

func serviceKeyCreatePayloadFromModel(m serviceKeyResourceModel) serviceKeyCreatePayload {
	return serviceKeyCreatePayload{
		Description:        stringValue(m.Description),
		ExpiresAt:          stringValue(m.ExpiresAt),
		Workspaces:         m.Workspaces,
		DefaultWorkspaceID: stringValue(m.DefaultWorkspaceID),
		RoleID:             stringValue(m.RoleID),
		OrgRoleID:          stringValue(m.OrgRoleID),
	}
}

func serviceKeyUpdatePayloadFromModel(m serviceKeyResourceModel) serviceKeyUpdatePayload {
	return serviceKeyUpdatePayload{
		RoleID:    stringPointerOrNil(m.RoleID),
		OrgRoleID: stringPointerOrNil(m.OrgRoleID),
	}
}

// serviceKeyModelFromAPI maps an API response onto the resource model. It starts
// from the prior model so that fields the API never returns — the secret (key),
// the configured workspace IDs, default_workspace_id, and expires_at — are
// preserved, and overwrites the fields the API does return.
func serviceKeyModelFromAPI(api serviceKeyAPI, previous serviceKeyResourceModel) serviceKeyResourceModel {
	next := previous
	next.ID = types.StringValue(api.ID)
	next.ShortKey = nullableString(api.ShortKey)
	next.Description = types.StringValue(api.Description)
	next.CreatedAt = nullableString(api.CreatedAt)
	next.LastUsedAt = nullableString(api.LastUsedAt)
	next.AccessScope = nullableString(api.AccessScope)
	next.RoleID = nullableString(api.RoleID)
	next.OrgRoleID = nullableString(api.OrgRoleID)
	next.WorkspaceNames = api.WorkspaceNames
	next.DefaultWorkspaceName = nullableString(api.DefaultWorkspaceName)
	if api.Key != "" {
		next.Key = types.StringValue(api.Key)
	}
	return next
}

func findServiceKeyByID(keys []serviceKeyAPI, id string) (serviceKeyAPI, error) {
	for _, key := range keys {
		if key.ID == id {
			return key, nil
		}
	}
	return serviceKeyAPI{}, fmt.Errorf("%w: %s", errServiceKeyNotFound, id)
}

func stringPointerOrNil(value types.String) *string {
	v := stringValue(value)
	if v == "" {
		return nil
	}
	return &v
}
