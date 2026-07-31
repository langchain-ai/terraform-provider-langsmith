package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	frameworkvalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var (
	_                  resource.Resource                = &TaggingResource{}
	_                  resource.ResourceWithImportState = &TaggingResource{}
	errTaggingNotFound                                  = errors.New("tagging not found")
)

const taggingsPath = "api/v1/workspaces/current/taggings"

var taggableResourceTypes = []string{"agent", "dashboard", "dataset", "deployment", "evaluator", "experiment", "fleet_integration", "mcp_server", "project", "prompt", "queue", "sandbox", "skill"}

func NewTaggingResource() resource.Resource { return &TaggingResource{} }

type TaggingResource struct{ client *langsmith.Client }

type taggingResourceModel struct {
	ID           types.String `tfsdk:"id"`
	TagValueID   types.String `tfsdk:"tag_value_id"`
	ResourceType types.String `tfsdk:"resource_type"`
	ResourceID   types.String `tfsdk:"resource_id"`
	CreatedAt    types.String `tfsdk:"created_at"`
}

type taggingAPI struct {
	ID           string `json:"id"`
	TagValueID   string `json:"tag_value_id"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	CreatedAt    string `json:"created_at"`
}
type tagValueWithTaggingsAPI struct {
	tagValueAPI
	Taggings []taggingAPI `json:"taggings"`
}
type tagKeyWithTaggingsAPI struct {
	Values []tagValueWithTaggingsAPI `json:"values"`
}

func (r *TaggingResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tagging"
}
func (r *TaggingResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Tags a LangSmith resource with a workspace-scoped tag value. Changes replace the tagging. Import with `<tagging_id>/<tag_value_id>/<resource_type>/<resource_id>`.", Attributes: map[string]schema.Attribute{
		"id":            schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Tagging ID."},
		"tag_value_id":  schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}, MarkdownDescription: "Tag value ID to apply."},
		"resource_type": schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Validators: []frameworkvalidator.String{oneOfStringValidator{values: taggableResourceTypes}}, MarkdownDescription: "Type of resource being tagged."},
		"resource_id":   schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}, MarkdownDescription: "ID of the resource being tagged."},
		"created_at":    schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Creation timestamp."},
	}}
}
func (r *TaggingResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
func (r *TaggingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan taggingResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.createTagging(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Tagging", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}
func (r *TaggingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state taggingResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.readTagging(ctx, state)
	if err != nil {
		if errors.Is(err, errTaggingNotFound) || isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Tagging", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}
func (r *TaggingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Unable to Update LangSmith Tagging", "Taggings are replace-only; all configurable attributes require replacement.")
}
func (r *TaggingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state taggingResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.deleteTagging(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to Delete LangSmith Tagging", err.Error())
	}
}
func (r *TaggingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 4 {
		resp.Diagnostics.AddError("Invalid Tagging Import ID", "Use <tagging_id>/<tag_value_id>/<resource_type>/<resource_id>.")
		return
	}
	for i, name := range []string{"id", "tag_value_id", "resource_type", "resource_id"} {
		if parts[i] == "" {
			resp.Diagnostics.AddError("Invalid Tagging Import ID", "Import ID components must not be empty.")
			return
		}
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(name), parts[i])...)
	}
}

func (r *TaggingResource) createTagging(ctx context.Context, plan taggingResourceModel) (taggingResourceModel, error) {
	var result taggingAPI
	if err := r.client.Post(ctx, taggingsPath, taggingPayloadFromModel(plan), &result); err != nil {
		return taggingResourceModel{}, err
	}
	if result.ID == "" {
		return taggingResourceModel{}, errors.New("LangSmith did not return a tagging ID")
	}
	return taggingModelFromAPI(result), nil
}
func (r *TaggingResource) readTagging(ctx context.Context, state taggingResourceModel) (taggingResourceModel, error) {
	params := url.Values{}
	params.Set("resource_type", state.ResourceType.ValueString())
	params.Set("resource_id", state.ResourceID.ValueString())
	var result []tagKeyWithTaggingsAPI
	if err := r.client.Get(ctx, "api/v1/workspaces/current/tags/resource?"+params.Encode(), nil, &result); err != nil {
		return taggingResourceModel{}, err
	}
	for _, key := range result {
		for _, value := range key.Values {
			for _, tagging := range value.Taggings {
				if tagging.ID == state.ID.ValueString() {
					return taggingModelFromAPI(tagging), nil
				}
			}
		}
	}
	return taggingResourceModel{}, errTaggingNotFound
}
func (r *TaggingResource) deleteTagging(ctx context.Context, id string) error {
	if err := r.client.Delete(ctx, fmt.Sprintf("%s/%s", taggingsPath, id), nil, nil); err != nil && !isLangSmithNotFound(err) {
		return err
	}
	return nil
}
func taggingPayloadFromModel(m taggingResourceModel) taggingAPI {
	return taggingAPI{TagValueID: m.TagValueID.ValueString(), ResourceType: m.ResourceType.ValueString(), ResourceID: m.ResourceID.ValueString()}
}
func taggingModelFromAPI(api taggingAPI) taggingResourceModel {
	return taggingResourceModel{ID: types.StringValue(api.ID), TagValueID: types.StringValue(api.TagValueID), ResourceType: types.StringValue(api.ResourceType), ResourceID: types.StringValue(api.ResourceID), CreatedAt: nullableString(api.CreatedAt)}
}

type oneOfStringValidator struct{ values []string }

func (v oneOfStringValidator) Description(ctx context.Context) string {
	return "value must be one of: " + strings.Join(v.values, ", ")
}
func (v oneOfStringValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v oneOfStringValidator) ValidateString(ctx context.Context, req frameworkvalidator.StringRequest, resp *frameworkvalidator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, value := range v.values {
		if req.ConfigValue.ValueString() == value {
			return
		}
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid String Value", v.Description(ctx))
}
