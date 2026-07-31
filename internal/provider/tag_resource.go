package provider

import (
	"context"
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
)

var (
	_ resource.Resource                = &TagResource{}
	_ resource.ResourceWithImportState = &TagResource{}
)

func NewTagResource() resource.Resource { return &TagResource{} }

// TagResource is the convenience lifecycle for one key and one value. Taggings
// remain separate resources because they have independent ownership.
type TagResource struct{ client *langsmith.Client }

type tagResourceModel struct {
	ID               types.String `tfsdk:"id"`
	TagKeyID         types.String `tfsdk:"tag_key_id"`
	TagValueID       types.String `tfsdk:"tag_value_id"`
	Key              types.String `tfsdk:"key"`
	Value            types.String `tfsdk:"value"`
	KeyDescription   types.String `tfsdk:"key_description"`
	ValueDescription types.String `tfsdk:"value_description"`
}

func (r *TagResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tag"
}

func (r *TagResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Convenience resource that manages one workspace-scoped LangSmith tag key and one value. Tagging resources remain separate. This resource owns the key; deleting it can also delete other values attached to that key outside Terraform. Import with `<tag_key_id>/<tag_value_id>`.",
		Attributes: map[string]schema.Attribute{
			"id":                schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Terraform resource ID; equal to `tag_value_id`."},
			"tag_key_id":        schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Created tag key ID."},
			"tag_value_id":      schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Created tag value ID."},
			"key":               schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}, MarkdownDescription: "Tag key name."},
			"value":             schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}, MarkdownDescription: "Tag value."},
			"key_description":   schema.StringAttribute{Optional: true, MarkdownDescription: "Optional tag key description."},
			"value_description": schema.StringAttribute{Optional: true, MarkdownDescription: "Optional tag value description."},
		},
	}
}

func (r *TagResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *TagResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan tagResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.createTag(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create LangSmith Tag", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *TagResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state tagResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.readTag(ctx, state.TagKeyID.ValueString(), state.TagValueID.ValueString())
	if err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Tag", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *TagResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state tagResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.updateTag(ctx, state.TagKeyID.ValueString(), state.TagValueID.ValueString(), plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update LangSmith Tag", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *TagResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state tagResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := (&TagKeyResource{client: r.client}).deleteTagKey(ctx, state.TagKeyID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to Delete LangSmith Tag", err.Error())
	}
}

func (r *TagResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Invalid Tag Import ID", "Use <tag_key_id>/<tag_value_id>.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tag_key_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tag_value_id"), parts[1])...)
}

func (r *TagResource) createTag(ctx context.Context, plan tagResourceModel) (tagResourceModel, error) {
	keyResource := &TagKeyResource{client: r.client}
	key, err := keyResource.createTagKey(ctx, tagKeyResourceModel{Key: plan.Key, Description: plan.KeyDescription})
	if err != nil {
		return tagResourceModel{}, err
	}
	value, err := (&TagValueResource{client: r.client}).createTagValue(ctx, tagValueResourceModel{TagKeyID: key.ID, Value: plan.Value, Description: plan.ValueDescription})
	if err != nil {
		if cleanupErr := keyResource.deleteTagKey(ctx, key.ID.ValueString()); cleanupErr != nil {
			return tagResourceModel{}, fmt.Errorf("create tag value: %w (cleanup tag key: %v)", err, cleanupErr)
		}
		return tagResourceModel{}, err
	}
	return tagModelFromParts(key, value), nil
}

func (r *TagResource) readTag(ctx context.Context, keyID, valueID string) (tagResourceModel, error) {
	key, err := (&TagKeyResource{client: r.client}).readTagKey(ctx, keyID)
	if err != nil {
		return tagResourceModel{}, err
	}
	value, err := (&TagValueResource{client: r.client}).readTagValue(ctx, keyID, valueID)
	if err != nil {
		return tagResourceModel{}, err
	}
	return tagModelFromParts(key, value), nil
}

func (r *TagResource) updateTag(ctx context.Context, keyID, valueID string, plan tagResourceModel) (tagResourceModel, error) {
	key, err := (&TagKeyResource{client: r.client}).updateTagKey(ctx, keyID, tagKeyResourceModel{Key: plan.Key, Description: plan.KeyDescription})
	if err != nil {
		return tagResourceModel{}, err
	}
	value, err := (&TagValueResource{client: r.client}).updateTagValue(ctx, keyID, valueID, tagValueResourceModel{Value: plan.Value, Description: plan.ValueDescription})
	if err != nil {
		return tagResourceModel{}, err
	}
	return tagModelFromParts(key, value), nil
}

func tagModelFromParts(key tagKeyResourceModel, value tagValueResourceModel) tagResourceModel {
	return tagResourceModel{
		ID: value.ID, TagKeyID: key.ID, TagValueID: value.ID, Key: key.Key, Value: value.Value,
		KeyDescription: key.Description, ValueDescription: value.Description,
	}
}
