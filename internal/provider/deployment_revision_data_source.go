package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	frameworkvalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

var _ datasource.DataSource = &DeploymentRevisionDataSource{}

func NewDeploymentRevisionDataSource() datasource.DataSource {
	return &DeploymentRevisionDataSource{}
}

type DeploymentRevisionDataSource struct {
	client *langsmith.Client
}

type deploymentRevisionDataSourceModel struct {
	deploymentRevisionModel
	DeploymentID types.String `tfsdk:"deployment_id"`
	RevisionID   types.String `tfsdk:"revision_id"`
}

type deploymentRevisionModel struct {
	ID                   types.String                         `tfsdk:"id"`
	CreatedAt            types.String                         `tfsdk:"created_at"`
	UpdatedAt            types.String                         `tfsdk:"updated_at"`
	Status               types.String                         `tfsdk:"status"`
	Source               types.String                         `tfsdk:"source"`
	SourceRevisionConfig *deploymentRevisionSourceConfigModel `tfsdk:"source_revision_config"`
}

type deploymentRevisionSourceConfigModel struct {
	RepoRef             types.String `tfsdk:"repo_ref"`
	LangGraphConfigPath types.String `tfsdk:"langgraph_config_path"`
	ImageURI            types.String `tfsdk:"image_uri"`
	SourceTarballPath   types.String `tfsdk:"source_tarball_path"`
	RepoCommitSHA       types.String `tfsdk:"repo_commit_sha"`
	DeepAgentsVersion   types.String `tfsdk:"deepagents_version"`
	TrackedPackages     types.List   `tfsdk:"tracked_packages"`
}

type deploymentRevisionAPI struct {
	ID                   string                            `json:"id"`
	CreatedAt            string                            `json:"created_at"`
	UpdatedAt            string                            `json:"updated_at"`
	Status               string                            `json:"status"`
	Source               string                            `json:"source"`
	SourceRevisionConfig deploymentRevisionSourceConfigAPI `json:"source_revision_config"`
}

type deploymentRevisionSourceConfigAPI struct {
	RepoRef             *string  `json:"repo_ref"`
	LangGraphConfigPath *string  `json:"langgraph_config_path"`
	ImageURI            *string  `json:"image_uri"`
	SourceTarballPath   *string  `json:"source_tarball_path"`
	RepoCommitSHA       *string  `json:"repo_commit_sha"`
	DeepAgentsVersion   *string  `json:"deepagents_version"`
	TrackedPackages     []string `json:"tracked_packages"`
}

func (d *DeploymentRevisionDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deployment_revision"
}

func (d *DeploymentRevisionDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attributes := deploymentRevisionAttributes()
	attributes["deployment_id"] = schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}, MarkdownDescription: "Deployment ID."}
	attributes["revision_id"] = schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}, MarkdownDescription: "Revision ID."}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads a deployment revision.",
		Attributes:          attributes,
	}
}

func deploymentRevisionAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id":                     schema.StringAttribute{Computed: true, MarkdownDescription: "Revision ID."},
		"created_at":             schema.StringAttribute{Computed: true, MarkdownDescription: "Revision creation time."},
		"updated_at":             schema.StringAttribute{Computed: true, MarkdownDescription: "Revision last update time."},
		"status":                 schema.StringAttribute{Computed: true, MarkdownDescription: "Revision status."},
		"source":                 schema.StringAttribute{Computed: true, MarkdownDescription: "Revision source."},
		"source_revision_config": deploymentRevisionSourceConfigSchema(),
	}
}

func deploymentRevisionSourceConfigSchema() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Computed:            true,
		MarkdownDescription: "Source configuration for the revision.",
		Attributes: map[string]schema.Attribute{
			"repo_ref":              schema.StringAttribute{Computed: true},
			"langgraph_config_path": schema.StringAttribute{Computed: true},
			"image_uri":             schema.StringAttribute{Computed: true},
			"source_tarball_path":   schema.StringAttribute{Computed: true},
			"repo_commit_sha":       schema.StringAttribute{Computed: true},
			"deepagents_version":    schema.StringAttribute{Computed: true},
			"tracked_packages":      schema.ListAttribute{Computed: true, ElementType: types.StringType},
		},
	}
}

func (d *DeploymentRevisionDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	client, ok := configureDataSourceClient(req.ProviderData, &resp.Diagnostics)
	if !ok {
		return
	}
	d.client = client
}

func (d *DeploymentRevisionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data deploymentRevisionDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	revision, err := d.readRevision(ctx, data.DeploymentID.ValueString(), data.RevisionID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Deployment Revision", err.Error())
		return
	}

	model, diags := deploymentRevisionModelFromAPI(ctx, revision)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	data.deploymentRevisionModel = model
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (d *DeploymentRevisionDataSource) readRevision(ctx context.Context, deploymentID, revisionID string) (deploymentRevisionAPI, error) {
	var revision deploymentRevisionAPI
	err := d.client.Get(ctx, deploymentRevisionPath(deploymentID, revisionID), nil, &revision)
	return revision, err
}

func deploymentRevisionModelFromAPI(ctx context.Context, revision deploymentRevisionAPI) (deploymentRevisionModel, diag.Diagnostics) {
	trackedPackages, diags := types.ListValueFrom(ctx, types.StringType, revision.SourceRevisionConfig.TrackedPackages)
	return deploymentRevisionModel{
		ID:        types.StringValue(revision.ID),
		CreatedAt: types.StringValue(revision.CreatedAt),
		UpdatedAt: types.StringValue(revision.UpdatedAt),
		Status:    types.StringValue(revision.Status),
		Source:    types.StringValue(revision.Source),
		SourceRevisionConfig: &deploymentRevisionSourceConfigModel{
			RepoRef:             nullableStringPointer(revision.SourceRevisionConfig.RepoRef),
			LangGraphConfigPath: nullableStringPointer(revision.SourceRevisionConfig.LangGraphConfigPath),
			ImageURI:            nullableStringPointer(revision.SourceRevisionConfig.ImageURI),
			SourceTarballPath:   nullableStringPointer(revision.SourceRevisionConfig.SourceTarballPath),
			RepoCommitSHA:       nullableStringPointer(revision.SourceRevisionConfig.RepoCommitSHA),
			DeepAgentsVersion:   nullableStringPointer(revision.SourceRevisionConfig.DeepAgentsVersion),
			TrackedPackages:     trackedPackages,
		},
	}, diags
}
