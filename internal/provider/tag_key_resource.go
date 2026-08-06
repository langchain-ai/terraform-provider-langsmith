package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	frameworkvalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

var (
	_ resource.Resource                = &TagKeyResource{}
	_ resource.ResourceWithImportState = &TagKeyResource{}
)

const tagKeysPath = "api/v1/workspaces/current/tag-keys"

func NewTagKeyResource() resource.Resource { return &TagKeyResource{} }

type TagKeyResource struct{ client *langsmith.Client }

type tagKeyResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Key         types.String `tfsdk:"key"`
	Description types.String `tfsdk:"description"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

type tagKeyPayload struct {
	Key         string  `json:"key"`
	Description *string `json:"description"`
}

type tagKeyAPI struct {
	ID          string  `json:"id"`
	Key         string  `json:"key"`
	Description *string `json:"description"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

func (r *TagKeyResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tag_key"
}

func (r *TagKeyResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a workspace-scoped LangSmith resource-tag key.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Tag key ID."},
			"key":         schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}, MarkdownDescription: "Tag key name, unique within the workspace."},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Optional tag key description."},
			"created_at":  schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Creation timestamp."},
			"updated_at":  schema.StringAttribute{Computed: true, MarkdownDescription: "Last update timestamp."},
		},
	}
}

func (r *TagKeyResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *TagKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan tagKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.createTagKey(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Tag Key", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *TagKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state tagKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.readTagKey(ctx, state.ID.ValueString())
	if err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Tag Key", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *TagKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state tagKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.updateTagKey(ctx, state.ID.ValueString(), plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Tag Key", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *TagKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state tagKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.deleteTagKey(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to Delete LangSmith Tag Key", err.Error())
	}
}

func (r *TagKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *TagKeyResource) createTagKey(ctx context.Context, plan tagKeyResourceModel) (tagKeyResourceModel, error) {
	var result tagKeyAPI
	if err := r.client.Post(ctx, tagKeysPath, tagKeyPayloadFromModel(plan), &result, option.WithMaxRetries(0)); err != nil {
		return tagKeyResourceModel{}, err
	}
	if result.ID == "" {
		return tagKeyResourceModel{}, errors.New("LangSmith did not return a tag key ID")
	}
	return tagKeyModelFromAPI(result), nil
}

func (r *TagKeyResource) readTagKey(ctx context.Context, id string) (tagKeyResourceModel, error) {
	var result tagKeyAPI
	if err := r.client.Get(ctx, tagKeyPath(id), nil, &result); err != nil {
		return tagKeyResourceModel{}, err
	}
	return tagKeyModelFromAPI(result), nil
}

func (r *TagKeyResource) updateTagKey(ctx context.Context, id string, plan tagKeyResourceModel) (tagKeyResourceModel, error) {
	var result tagKeyAPI
	if err := r.client.Patch(ctx, tagKeyPath(id), tagKeyPayloadFromModel(plan), &result); err != nil {
		return tagKeyResourceModel{}, err
	}
	return tagKeyModelFromAPI(result), nil
}

func (r *TagKeyResource) deleteTagKey(ctx context.Context, id string) error {
	if err := r.client.Delete(ctx, tagKeyPath(id), nil, nil); err != nil && !isLangSmithNotFound(err) {
		return err
	}
	return nil
}

func tagKeyPath(id string) string { return fmt.Sprintf("%s/%s", tagKeysPath, id) }

func tagKeyPayloadFromModel(m tagKeyResourceModel) tagKeyPayload {
	return tagKeyPayload{Key: m.Key.ValueString(), Description: optionalStringPointer(m.Description)}
}

func tagKeyModelFromAPI(api tagKeyAPI) tagKeyResourceModel {
	return tagKeyResourceModel{
		ID: types.StringValue(api.ID), Key: types.StringValue(api.Key), Description: nullableStringPointer(api.Description),
		CreatedAt: nullableString(api.CreatedAt), UpdatedAt: nullableString(api.UpdatedAt),
	}
}

func optionalStringPointer(value types.String) *string {
	if value.IsNull() || value.IsUnknown() {
		return nil
	}
	result := value.ValueString()
	return &result
}

func nullableStringPointer(value *string) types.String {
	if value == nil {
		return types.StringNull()
	}
	return types.StringValue(*value)
}
