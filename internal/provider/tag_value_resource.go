package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	_ resource.Resource                = &TagValueResource{}
	_ resource.ResourceWithImportState = &TagValueResource{}
)

func NewTagValueResource() resource.Resource { return &TagValueResource{} }

type TagValueResource struct{ client *langsmith.Client }

type tagValueResourceModel struct {
	ID          types.String `tfsdk:"id"`
	TagKeyID    types.String `tfsdk:"tag_key_id"`
	Value       types.String `tfsdk:"value"`
	Description types.String `tfsdk:"description"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

type tagValuePayload struct {
	Value       string  `json:"value"`
	Description *string `json:"description"`
}

type tagValueAPI struct {
	ID          string  `json:"id"`
	TagKeyID    string  `json:"tag_key_id"`
	Value       string  `json:"value"`
	Description *string `json:"description"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

func (r *TagValueResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tag_value"
}

func (r *TagValueResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manages a value belonging to a workspace-scoped LangSmith resource-tag key. Import with `<tag_key_id>/<tag_value_id>`.", Attributes: map[string]schema.Attribute{
		"id":          schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Tag value ID."},
		"tag_key_id":  schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}, MarkdownDescription: "Parent tag key ID."},
		"value":       schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}, MarkdownDescription: "Tag value, unique within its key."},
		"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Optional tag value description."},
		"created_at":  schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Creation timestamp."},
		"updated_at":  schema.StringAttribute{Computed: true, MarkdownDescription: "Last update timestamp."},
	}}
}

func (r *TagValueResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *TagValueResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan tagValueResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.createTagValue(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Tag Value", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *TagValueResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state tagValueResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.readTagValue(ctx, state.TagKeyID.ValueString(), state.ID.ValueString())
	if err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Tag Value", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *TagValueResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state tagValueResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.updateTagValue(ctx, state.TagKeyID.ValueString(), state.ID.ValueString(), plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Tag Value", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *TagValueResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state tagValueResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.deleteTagValue(ctx, state.TagKeyID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to Delete LangSmith Tag Value", err.Error())
	}
}

func (r *TagValueResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Invalid Tag Value Import ID", "Use <tag_key_id>/<tag_value_id>.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tag_key_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}

func (r *TagValueResource) createTagValue(ctx context.Context, plan tagValueResourceModel) (tagValueResourceModel, error) {
	var result tagValueAPI
	if err := r.client.Post(ctx, tagValuesPath(plan.TagKeyID.ValueString()), tagValuePayloadFromModel(plan), &result, option.WithMaxRetries(0)); err != nil {
		return tagValueResourceModel{}, err
	}
	if result.ID == "" {
		return tagValueResourceModel{}, errors.New("LangSmith did not return a tag value ID")
	}
	return tagValueModelFromAPI(result), nil
}

func (r *TagValueResource) readTagValue(ctx context.Context, keyID, valueID string) (tagValueResourceModel, error) {
	var result tagValueAPI
	if err := r.client.Get(ctx, tagValuePath(keyID, valueID), nil, &result); err != nil {
		return tagValueResourceModel{}, err
	}
	return tagValueModelFromAPI(result), nil
}

func (r *TagValueResource) updateTagValue(ctx context.Context, keyID, valueID string, plan tagValueResourceModel) (tagValueResourceModel, error) {
	var result tagValueAPI
	if err := r.client.Patch(ctx, tagValuePath(keyID, valueID), tagValuePayloadFromModel(plan), &result); err != nil {
		return tagValueResourceModel{}, err
	}
	return tagValueModelFromAPI(result), nil
}

func (r *TagValueResource) deleteTagValue(ctx context.Context, keyID, valueID string) error {
	if err := r.client.Delete(ctx, tagValuePath(keyID, valueID), nil, nil); err != nil && !isLangSmithNotFound(err) {
		return err
	}
	return nil
}

func tagValuesPath(keyID string) string { return fmt.Sprintf("%s/%s/tag-values", tagKeysPath, keyID) }
func tagValuePath(keyID, valueID string) string {
	return fmt.Sprintf("%s/%s", tagValuesPath(keyID), valueID)
}
func tagValuePayloadFromModel(m tagValueResourceModel) tagValuePayload {
	return tagValuePayload{Value: m.Value.ValueString(), Description: optionalStringPointer(m.Description)}
}
func tagValueModelFromAPI(api tagValueAPI) tagValueResourceModel {
	return tagValueResourceModel{ID: types.StringValue(api.ID), TagKeyID: types.StringValue(api.TagKeyID), Value: types.StringValue(api.Value), Description: nullableStringPointer(api.Description), CreatedAt: nullableString(api.CreatedAt), UpdatedAt: nullableString(api.UpdatedAt)}
}
