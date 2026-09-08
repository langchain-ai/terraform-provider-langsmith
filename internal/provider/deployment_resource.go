package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	frameworkvalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

const deploymentsPath = "v2/deployments"

var (
	_ resource.Resource                = &DeploymentResource{}
	_ resource.ResourceWithImportState = &DeploymentResource{}
)

type DeploymentResource struct {
	client       *langsmith.Client
	pollInterval time.Duration
	waitTimeout  time.Duration
}

type deploymentResourceModel struct {
	ID                          types.String                     `tfsdk:"id"`
	Name                        types.String                     `tfsdk:"name"`
	Source                      types.String                     `tfsdk:"source"`
	DisplayName                 types.String                     `tfsdk:"display_name"`
	SourceConfig                *deploymentSourceConfigModel     `tfsdk:"source_config"`
	SourceRevisionConfig        *sourceRevisionConfigModel       `tfsdk:"source_revision_config"`
	Secrets                     types.Map                        `tfsdk:"secrets"`
	SecretsVersion              types.String                     `tfsdk:"secrets_version"`
	SecretReferences            []deploymentSecretReferenceModel `tfsdk:"secret_references"`
	Shareable                   types.Bool                       `tfsdk:"shareable"`
	RouteThroughGateway         types.Bool                       `tfsdk:"route_through_gateway"`
	TenantID                    types.String                     `tfsdk:"tenant_id"`
	CreatedAt                   types.String                     `tfsdk:"created_at"`
	UpdatedAt                   types.String                     `tfsdk:"updated_at"`
	Status                      types.String                     `tfsdk:"status"`
	LatestRevisionID            types.String                     `tfsdk:"latest_revision_id"`
	ActiveRevisionID            types.String                     `tfsdk:"active_revision_id"`
	TracerSessionID             types.String                     `tfsdk:"tracer_session_id"`
	URL                         types.String                     `tfsdk:"url"`
	LatestRevisionStatus        types.String                     `tfsdk:"latest_revision_status"`
	LatestRevisionStatusMessage types.String                     `tfsdk:"latest_revision_status_message"`
}

type deploymentSourceConfigModel struct {
	IntegrationID  types.String                   `tfsdk:"integration_id"`
	RepoURL        types.String                   `tfsdk:"repo_url"`
	DeploymentType types.String                   `tfsdk:"deployment_type"`
	BuildOnPush    types.Bool                     `tfsdk:"build_on_push"`
	CustomURL      types.String                   `tfsdk:"custom_url"`
	ListenerID     types.String                   `tfsdk:"listener_id"`
	ListenerConfig *deploymentListenerConfigModel `tfsdk:"listener_config"`
	InstallCommand types.String                   `tfsdk:"install_command"`
	BuildCommand   types.String                   `tfsdk:"build_command"`
	TemplateID     types.String                   `tfsdk:"template_id"`
	ResourceSpec   *deploymentResourceSpecModel   `tfsdk:"resource_spec"`
}

type deploymentListenerConfigModel struct {
	K8sNamespace types.String `tfsdk:"k8s_namespace"`
}

type deploymentResourceSpecModel struct {
	MinScale           types.Int64   `tfsdk:"min_scale"`
	MaxScale           types.Int64   `tfsdk:"max_scale"`
	CPU                types.Float64 `tfsdk:"cpu"`
	CPULimit           types.Float64 `tfsdk:"cpu_limit"`
	MemoryMB           types.Int64   `tfsdk:"memory_mb"`
	MemoryLimitMB      types.Int64   `tfsdk:"memory_limit_mb"`
	Labels             types.Map     `tfsdk:"labels"`
	Annotations        types.Map     `tfsdk:"annotations"`
	ServiceAccountName types.String  `tfsdk:"service_account_name"`
}

type sourceRevisionConfigModel struct {
	RepoRef             types.String `tfsdk:"repo_ref"`
	LanggraphConfigPath types.String `tfsdk:"langgraph_config_path"`
	ImageURI            types.String `tfsdk:"image_uri"`
	SourceTarballPath   types.String `tfsdk:"source_tarball_path"`
}

type deploymentSecretReferenceModel struct {
	Name       types.String `tfsdk:"name"`
	SecretName types.String `tfsdk:"secret_name"`
	SecretKey  types.String `tfsdk:"secret_key"`
}

type deploymentAPI struct {
	ID                   string                         `json:"id"`
	Name                 string                         `json:"name"`
	Source               string                         `json:"source"`
	DisplayName          *string                        `json:"display_name"`
	SourceConfig         map[string]any                 `json:"source_config"`
	SourceRevisionConfig map[string]any                 `json:"source_revision_config"`
	SecretReferences     []deploymentSecretReferenceAPI `json:"secret_references"`
	Shareable            bool                           `json:"shareable"`
	RouteThroughGateway  bool                           `json:"route_through_gateway"`
	TenantID             string                         `json:"tenant_id"`
	CreatedAt            string                         `json:"created_at"`
	UpdatedAt            string                         `json:"updated_at"`
	Status               string                         `json:"status"`
	LatestRevisionID     *string                        `json:"latest_revision_id"`
	ActiveRevisionID     *string                        `json:"active_revision_id"`
	TracerSessionID      *string                        `json:"tracer_session_id"`
	URL                  *string                        `json:"url"`
}

type deploymentResourceRevisionAPI struct {
	ID            string  `json:"id"`
	Status        string  `json:"status"`
	StatusMessage *string `json:"status_message"`
}

type deploymentSecretReferenceAPI struct {
	Name       string `json:"name"`
	SecretName string `json:"secret_name"`
	SecretKey  string `json:"secret_key"`
}

func NewDeploymentResource() resource.Resource {
	return &DeploymentResource{}
}

func (r *DeploymentResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deployment"
}

func (r *DeploymentResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	client, ok := configureControlPlaneClient(req.ProviderData, &resp.Diagnostics, "Resource")
	if ok {
		r.client = client
	}
}

func (r *DeploymentResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	computedString := func(description string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: description}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the desired state of a LangSmith deployment. Deployment revisions are created and tracked by the service.",
		Attributes: map[string]schema.Attribute{
			"id":                     computedString("Deployment UUID."),
			"name":                   schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}},
			"source":                 schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: []frameworkvalidator.String{oneOfStringValidator{values: []string{"github", "external_docker", "internal_docker", "internal_source", "internal_template"}}}, MarkdownDescription: "Deployment source."},
			"display_name":           schema.StringAttribute{Optional: true, Computed: true},
			"source_config":          schema.SingleNestedAttribute{Required: true, Attributes: sourceConfigSchema()},
			"source_revision_config": schema.SingleNestedAttribute{Required: true, Attributes: sourceRevisionConfigSchema()},
			"secrets":                schema.MapAttribute{Optional: true, Sensitive: true, WriteOnly: true, ElementType: types.StringType, MarkdownDescription: "Write-only environment variable values. Change secrets_version whenever this map changes."},
			"secrets_version":        schema.StringAttribute{Optional: true, MarkdownDescription: "Opaque trigger for applying a new secrets map as a revision."},
			"secret_references": schema.ListNestedAttribute{Optional: true, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}}, "secret_name": schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}}, "secret_key": schema.StringAttribute{Required: true, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}},
			}}},
			"shareable":                      schema.BoolAttribute{Optional: true, Computed: true},
			"route_through_gateway":          schema.BoolAttribute{Optional: true, Computed: true},
			"tenant_id":                      computedString("Owning tenant UUID."),
			"created_at":                     computedString("Creation timestamp."),
			"updated_at":                     computedString("Last update timestamp."),
			"status":                         computedString("Deployment status."),
			"latest_revision_id":             computedString("Latest revision UUID."),
			"active_revision_id":             computedString("Active revision UUID."),
			"tracer_session_id":              computedString("Tracing project UUID."),
			"url":                            computedString("Serving URL."),
			"latest_revision_status":         computedString("Latest revision status."),
			"latest_revision_status_message": computedString("Latest revision status detail."),
		},
	}
}

func sourceConfigSchema() map[string]schema.Attribute {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	return map[string]schema.Attribute{
		"integration_id":  schema.StringAttribute{Optional: true, PlanModifiers: replace, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}},
		"repo_url":        schema.StringAttribute{Optional: true, PlanModifiers: replace, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}},
		"deployment_type": schema.StringAttribute{Optional: true, PlanModifiers: replace, Validators: []frameworkvalidator.String{oneOfStringValidator{values: []string{"dev_free", "dev", "prod", "dev_zero", "dev_free_zero"}}}},
		"build_on_push":   schema.BoolAttribute{Optional: true},
		"custom_url":      schema.StringAttribute{Optional: true},
		"listener_id":     schema.StringAttribute{Optional: true, PlanModifiers: replace, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}},
		"listener_config": schema.SingleNestedAttribute{Optional: true, Attributes: map[string]schema.Attribute{"k8s_namespace": schema.StringAttribute{Optional: true, PlanModifiers: replace}}},
		"install_command": schema.StringAttribute{Optional: true},
		"build_command":   schema.StringAttribute{Optional: true},
		"template_id":     schema.StringAttribute{Optional: true, PlanModifiers: replace, Validators: []frameworkvalidator.String{nonEmptyStringValidator{}}},
		"resource_spec": schema.SingleNestedAttribute{Optional: true, Attributes: map[string]schema.Attribute{
			"min_scale": schema.Int64Attribute{Optional: true}, "max_scale": schema.Int64Attribute{Optional: true},
			"cpu": schema.Float64Attribute{Optional: true}, "cpu_limit": schema.Float64Attribute{Optional: true},
			"memory_mb": schema.Int64Attribute{Optional: true}, "memory_limit_mb": schema.Int64Attribute{Optional: true},
			"labels":               schema.MapAttribute{Optional: true, ElementType: types.StringType},
			"annotations":          schema.MapAttribute{Optional: true, ElementType: types.StringType},
			"service_account_name": schema.StringAttribute{Optional: true},
		}},
	}
}

func sourceRevisionConfigSchema() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"repo_ref":              schema.StringAttribute{Optional: true},
		"langgraph_config_path": schema.StringAttribute{Optional: true},
		"image_uri":             schema.StringAttribute{Optional: true},
		"source_tarball_path":   schema.StringAttribute{Optional: true},
	}
}

func (r *DeploymentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, config deploymentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.Secrets = config.Secrets
	model, err := r.create(ctx, plan)
	model.Secrets = types.MapNull(types.StringType)
	if err != nil {
		if !model.ID.IsNull() && !model.ID.IsUnknown() && model.ID.ValueString() != "" {
			resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
		}
		resp.Diagnostics.AddError("Unable to Create LangSmith Deployment", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *DeploymentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state deploymentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, err := r.read(ctx, state.ID.ValueString(), state)
	if err != nil {
		if isLangSmithNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read LangSmith Deployment", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *DeploymentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, config deploymentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.Secrets = config.Secrets
	model, err := r.update(ctx, state, plan)
	model.Secrets = types.MapNull(types.StringType)
	if err != nil {
		if !model.ID.IsNull() && !model.ID.IsUnknown() && model.ID.ValueString() != "" {
			resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
		}
		resp.Diagnostics.AddError("Unable to Update LangSmith Deployment", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *DeploymentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state deploymentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.delete(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to Delete LangSmith Deployment", err.Error())
	}
}

func (r *DeploymentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func deploymentPath(id string) string {
	return fmt.Sprintf("%s/%s", deploymentsPath, url.PathEscape(id))
}
func deploymentRevisionsPath(id string) string {
	return fmt.Sprintf("%s/revisions", deploymentPath(id))
}
func deploymentRevisionPath(id, revisionID string) string {
	return fmt.Sprintf("%s/%s", deploymentRevisionsPath(id), url.PathEscape(revisionID))
}

func (r *DeploymentResource) create(ctx context.Context, plan deploymentResourceModel) (deploymentResourceModel, error) {
	var result deploymentAPI
	if err := r.client.Post(ctx, deploymentsPath, createPayload(plan), &result); err != nil {
		return plan, err
	}
	interim := deploymentModelFromAPI(result, deploymentResourceRevisionAPI{}, plan)
	if result.ID == "" || result.LatestRevisionID == nil {
		return interim, errors.New("LangSmith did not return deployment and revision IDs")
	}
	if !plan.DisplayName.IsNull() && !plan.DisplayName.IsUnknown() {
		var ignored deploymentAPI
		if err := r.client.Patch(ctx, deploymentPath(result.ID), map[string]any{"display_name": plan.DisplayName.ValueString()}, &ignored); err != nil {
			return interim, err
		}
	}
	revision, err := r.waitForRevision(ctx, result.ID, *result.LatestRevisionID)
	if err != nil {
		interim.LatestRevisionStatus = nullableString(revision.Status)
		interim.LatestRevisionStatusMessage = nullableStringPointer(revision.StatusMessage)
		return interim, err
	}
	return r.read(ctx, result.ID, plan)
}

func (r *DeploymentResource) read(ctx context.Context, id string, previous deploymentResourceModel) (deploymentResourceModel, error) {
	var result deploymentAPI
	if err := r.client.Get(ctx, deploymentPath(id), nil, &result); err != nil {
		return previous, err
	}
	var revision deploymentResourceRevisionAPI
	if result.LatestRevisionID != nil {
		if err := r.client.Get(ctx, deploymentRevisionPath(id, *result.LatestRevisionID), nil, &revision); err != nil {
			return previous, err
		}
	}
	return deploymentModelFromAPI(result, revision, previous), nil
}

func (r *DeploymentResource) update(ctx context.Context, state, plan deploymentResourceModel) (deploymentResourceModel, error) {
	id := state.ID.ValueString()
	mutable := mutablePayload(state, plan)
	if len(mutable) > 0 {
		var ignored deploymentAPI
		if err := r.client.Patch(ctx, deploymentPath(id), mutable, &ignored); err != nil {
			return plan, err
		}
	}
	if revisionChanged(state, plan) {
		var revision deploymentResourceRevisionAPI
		if err := r.client.Post(ctx, deploymentRevisionsPath(id), revisionPayload(plan, !plan.Secrets.IsNull() && !plan.Secrets.IsUnknown()), &revision); err != nil {
			return plan, err
		}
		partial := plan
		partial.ID = state.ID
		partial.Secrets = types.MapNull(types.StringType)
		partial.LatestRevisionID = nullableString(revision.ID)
		partial.LatestRevisionStatus = nullableString(revision.Status)
		partial.LatestRevisionStatusMessage = nullableStringPointer(revision.StatusMessage)
		partial.ActiveRevisionID = state.ActiveRevisionID
		if revision.ID == "" {
			return partial, errors.New("LangSmith did not return a revision ID")
		}
		waited, err := r.waitForRevision(ctx, id, revision.ID)
		if err != nil {
			partial.LatestRevisionStatus = nullableString(waited.Status)
			partial.LatestRevisionStatusMessage = nullableStringPointer(waited.StatusMessage)
			return partial, err
		}
	}
	return r.read(ctx, id, plan)
}

func (r *DeploymentResource) delete(ctx context.Context, id string) error {
	if err := r.client.Delete(ctx, deploymentPath(id), nil, nil); err != nil {
		if isLangSmithNotFound(err) {
			return nil
		}
		return err
	}
	interval, timeout := r.pollSettings()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		var result deploymentAPI
		if err := r.client.Get(ctx, deploymentPath(id), nil, &result); err != nil {
			if isLangSmithNotFound(err) {
				return nil
			}
			if ctx.Err() != nil {
				return fmt.Errorf("waiting for deployment %s deletion: %w", id, ctx.Err())
			}
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for deployment %s deletion: %w", id, ctx.Err())
		case <-time.After(interval):
		}
	}
}

func (r *DeploymentResource) pollSettings() (time.Duration, time.Duration) {
	interval := r.pollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	timeout := r.waitTimeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return interval, timeout
}

func (r *DeploymentResource) waitForRevision(ctx context.Context, id, revisionID string) (deploymentResourceRevisionAPI, error) {
	interval, timeout := r.pollSettings()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		var revision deploymentResourceRevisionAPI
		if err := r.client.Get(ctx, deploymentRevisionPath(id, revisionID), nil, &revision); err != nil {
			return revision, err
		}
		switch revision.Status {
		case "DEPLOYED":
			return revision, nil
		case "CREATE_FAILED", "BUILD_FAILED", "DEPLOY_FAILED", "SKIPPED", "INTERRUPTED", "UNKNOWN":
			return revision, fmt.Errorf("revision %s reached terminal status %s: %s", revisionID, revision.Status, deploymentStringPointer(revision.StatusMessage))
		}
		select {
		case <-ctx.Done():
			return revision, fmt.Errorf("waiting for revision %s: %w", revisionID, ctx.Err())
		case <-time.After(interval):
		}
	}
}

func createPayload(m deploymentResourceModel) map[string]any {
	p := map[string]any{"name": m.Name.ValueString(), "source": m.Source.ValueString(), "source_config": sourceConfigPayload(m.SourceConfig), "source_revision_config": sourceRevisionPayload(m.SourceRevisionConfig), "secrets": secretsPayload(m.Secrets)}
	if refs := secretReferencesPayload(m.SecretReferences); refs != nil {
		p["secret_references"] = refs
	}
	putBool(p, "shareable", m.Shareable)
	putBool(p, "route_through_gateway", m.RouteThroughGateway)
	return p
}

func mutablePayload(old, plan deploymentResourceModel) map[string]any {
	p := map[string]any{}
	if !old.DisplayName.Equal(plan.DisplayName) {
		p["display_name"] = nullableValue(plan.DisplayName)
	}
	oldBuild, newBuild := types.BoolNull(), types.BoolNull()
	if old.SourceConfig != nil {
		oldBuild = old.SourceConfig.BuildOnPush
	}
	if plan.SourceConfig != nil {
		newBuild = plan.SourceConfig.BuildOnPush
	}
	oldCustomURL, newCustomURL := types.StringNull(), types.StringNull()
	if old.SourceConfig != nil {
		oldCustomURL = old.SourceConfig.CustomURL
	}
	if plan.SourceConfig != nil {
		newCustomURL = plan.SourceConfig.CustomURL
	}
	if !oldBuild.Equal(newBuild) || !oldCustomURL.Equal(newCustomURL) {
		sourceConfig := map[string]any{}
		if !oldBuild.Equal(newBuild) {
			sourceConfig["build_on_push"] = nullableBool(newBuild)
		}
		if !oldCustomURL.Equal(newCustomURL) {
			sourceConfig["custom_url"] = nullableValue(newCustomURL)
		}
		p["source_config"] = sourceConfig
	}
	return p
}

func revisionChanged(old, plan deploymentResourceModel) bool {
	if !reflect.DeepEqual(old.SourceRevisionConfig, plan.SourceRevisionConfig) || !old.SecretsVersion.Equal(plan.SecretsVersion) || !reflect.DeepEqual(old.SecretReferences, plan.SecretReferences) || !old.Shareable.Equal(plan.Shareable) || !old.RouteThroughGateway.Equal(plan.RouteThroughGateway) {
		return true
	}
	return !reflect.DeepEqual(revisionSourceConfig(old.SourceConfig), revisionSourceConfig(plan.SourceConfig))
}

func revisionSourceConfig(s *deploymentSourceConfigModel) map[string]any {
	if s == nil {
		return nil
	}
	p := map[string]any{}
	putString(p, "install_command", s.InstallCommand)
	putString(p, "build_command", s.BuildCommand)
	if s.ResourceSpec != nil {
		p["resource_spec"] = resourceSpecPayload(s.ResourceSpec)
	}
	return p
}

func revisionPayload(m deploymentResourceModel, includeSecrets bool) map[string]any {
	p := map[string]any{"source_revision_config": sourceRevisionPayload(m.SourceRevisionConfig)}
	if sourceConfig := revisionSourceConfig(m.SourceConfig); len(sourceConfig) > 0 {
		p["source_config"] = sourceConfig
	}
	if includeSecrets {
		p["secrets"] = secretsPayload(m.Secrets)
	}
	if refs := secretReferencesPayload(m.SecretReferences); refs != nil {
		p["secret_references"] = refs
	}
	putBool(p, "shareable", m.Shareable)
	putBool(p, "route_through_gateway", m.RouteThroughGateway)
	return p
}

func sourceConfigPayload(s *deploymentSourceConfigModel) map[string]any {
	p := revisionSourceConfig(s)
	if p == nil {
		return map[string]any{}
	}
	putString(p, "integration_id", s.IntegrationID)
	putString(p, "repo_url", s.RepoURL)
	putString(p, "deployment_type", s.DeploymentType)
	putBool(p, "build_on_push", s.BuildOnPush)
	putString(p, "custom_url", s.CustomURL)
	putString(p, "listener_id", s.ListenerID)
	putString(p, "template_id", s.TemplateID)
	if s.ListenerConfig != nil {
		q := map[string]any{}
		putString(q, "k8s_namespace", s.ListenerConfig.K8sNamespace)
		p["listener_config"] = q
	}
	return p
}

func sourceRevisionPayload(s *sourceRevisionConfigModel) map[string]any {
	p := map[string]any{}
	if s == nil {
		return p
	}
	putString(p, "repo_ref", s.RepoRef)
	putString(p, "langgraph_config_path", s.LanggraphConfigPath)
	putString(p, "image_uri", s.ImageURI)
	putString(p, "source_tarball_path", s.SourceTarballPath)
	return p
}

func resourceSpecPayload(s *deploymentResourceSpecModel) map[string]any {
	p := map[string]any{}
	putInt(p, "min_scale", s.MinScale)
	putInt(p, "max_scale", s.MaxScale)
	putFloat(p, "cpu", s.CPU)
	putFloat(p, "cpu_limit", s.CPULimit)
	putInt(p, "memory_mb", s.MemoryMB)
	putInt(p, "memory_limit_mb", s.MemoryLimitMB)
	if v := stringMap(s.Labels); v != nil {
		p["labels"] = v
	}
	if v := stringMap(s.Annotations); v != nil {
		p["annotations"] = v
	}
	putString(p, "service_account_name", s.ServiceAccountName)
	return p
}

func secretsPayload(v types.Map) []map[string]string {
	values := stringMap(v)
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]map[string]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]string{"name": k, "value": values[k]})
	}
	return out
}

func secretReferencesPayload(refs []deploymentSecretReferenceModel) []deploymentSecretReferenceAPI {
	if refs == nil {
		return nil
	}
	out := make([]deploymentSecretReferenceAPI, len(refs))
	for i, ref := range refs {
		out[i] = deploymentSecretReferenceAPI{ref.Name.ValueString(), ref.SecretName.ValueString(), ref.SecretKey.ValueString()}
	}
	return out
}

func deploymentModelFromAPI(api deploymentAPI, revision deploymentResourceRevisionAPI, previous deploymentResourceModel) deploymentResourceModel {
	next := previous
	next.ID = types.StringValue(api.ID)
	next.Name = types.StringValue(api.Name)
	next.Source = types.StringValue(api.Source)
	next.DisplayName = nullableStringPointer(api.DisplayName)
	sourceConfig := sourceConfigModelFromAPI(api.SourceConfig)
	if next.SourceConfig != nil {
		if _, ok := api.SourceConfig["install_command"]; !ok {
			sourceConfig.InstallCommand = next.SourceConfig.InstallCommand
		}
		if _, ok := api.SourceConfig["build_command"]; !ok {
			sourceConfig.BuildCommand = next.SourceConfig.BuildCommand
		}
	}
	next.SourceConfig = sourceConfig
	if api.SourceRevisionConfig != nil {
		next.SourceRevisionConfig = sourceRevisionModelFromAPI(api.SourceRevisionConfig)
	}
	if len(api.SecretReferences) > 0 || previous.SecretReferences != nil {
		next.SecretReferences = secretReferenceModelsFromAPI(api.SecretReferences)
	}
	next.Shareable = types.BoolValue(api.Shareable)
	next.RouteThroughGateway = types.BoolValue(api.RouteThroughGateway)
	next.TenantID = nullableString(api.TenantID)
	next.CreatedAt = nullableString(api.CreatedAt)
	next.UpdatedAt = nullableString(api.UpdatedAt)
	next.Status = nullableString(api.Status)
	next.LatestRevisionID = nullableStringPointer(api.LatestRevisionID)
	next.ActiveRevisionID = nullableStringPointer(api.ActiveRevisionID)
	next.TracerSessionID = nullableStringPointer(api.TracerSessionID)
	next.URL = nullableStringPointer(api.URL)
	next.LatestRevisionStatus = nullableString(revision.Status)
	next.LatestRevisionStatusMessage = nullableStringPointer(revision.StatusMessage)
	next.Secrets = types.MapNull(types.StringType)
	return next
}

func sourceConfigModelFromAPI(api map[string]any) *deploymentSourceConfigModel {
	if api == nil {
		return &deploymentSourceConfigModel{}
	}
	model := &deploymentSourceConfigModel{
		IntegrationID:  apiString(api, "integration_id"),
		RepoURL:        apiString(api, "repo_url"),
		DeploymentType: apiString(api, "deployment_type"),
		BuildOnPush:    apiBool(api, "build_on_push"),
		CustomURL:      apiString(api, "custom_url"),
		ListenerID:     apiString(api, "listener_id"),
		InstallCommand: apiString(api, "install_command"),
		BuildCommand:   apiString(api, "build_command"),
		TemplateID:     apiString(api, "template_id"),
	}
	if listener, ok := api["listener_config"].(map[string]any); ok {
		model.ListenerConfig = &deploymentListenerConfigModel{K8sNamespace: apiString(listener, "k8s_namespace")}
	}
	if spec, ok := api["resource_spec"].(map[string]any); ok {
		model.ResourceSpec = &deploymentResourceSpecModel{
			MinScale: apiInt(spec, "min_scale"), MaxScale: apiInt(spec, "max_scale"), CPU: apiFloat(spec, "cpu"), CPULimit: apiFloat(spec, "cpu_limit"),
			MemoryMB: apiInt(spec, "memory_mb"), MemoryLimitMB: apiInt(spec, "memory_limit_mb"), Labels: apiMap(spec, "labels"), Annotations: apiMap(spec, "annotations"), ServiceAccountName: apiString(spec, "service_account_name"),
		}
	}
	return model
}

func sourceRevisionModelFromAPI(api map[string]any) *sourceRevisionConfigModel {
	return &sourceRevisionConfigModel{RepoRef: apiString(api, "repo_ref"), LanggraphConfigPath: apiString(api, "langgraph_config_path"), ImageURI: apiString(api, "image_uri"), SourceTarballPath: apiString(api, "source_tarball_path")}
}

func secretReferenceModelsFromAPI(api []deploymentSecretReferenceAPI) []deploymentSecretReferenceModel {
	result := make([]deploymentSecretReferenceModel, len(api))
	for i, ref := range api {
		result[i] = deploymentSecretReferenceModel{Name: types.StringValue(ref.Name), SecretName: types.StringValue(ref.SecretName), SecretKey: types.StringValue(ref.SecretKey)}
	}
	return result
}

func apiString(api map[string]any, key string) types.String {
	if value, ok := api[key].(string); ok {
		return types.StringValue(value)
	}
	return types.StringNull()
}
func apiBool(api map[string]any, key string) types.Bool {
	if value, ok := api[key].(bool); ok {
		return types.BoolValue(value)
	}
	return types.BoolNull()
}
func apiInt(api map[string]any, key string) types.Int64 {
	if value, ok := api[key].(float64); ok {
		return types.Int64Value(int64(value))
	}
	return types.Int64Null()
}
func apiFloat(api map[string]any, key string) types.Float64 {
	if value, ok := api[key].(float64); ok {
		return types.Float64Value(value)
	}
	return types.Float64Null()
}
func apiMap(api map[string]any, key string) types.Map {
	value, ok := api[key].(map[string]any)
	if !ok {
		return types.MapNull(types.StringType)
	}
	elements := make(map[string]types.String, len(value))
	for k, raw := range value {
		if s, ok := raw.(string); ok {
			elements[k] = types.StringValue(s)
		}
	}
	converted := make(map[string]attr.Value, len(elements))
	for k, v := range elements {
		converted[k] = v
	}
	return types.MapValueMust(types.StringType, converted)
}

func stringMap(v types.Map) map[string]string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	out := map[string]string{}
	for k, value := range v.Elements() {
		if s, ok := value.(types.String); ok {
			out[k] = s.ValueString()
		}
	}
	return out
}
func putString(p map[string]any, key string, v types.String) {
	if !v.IsNull() && !v.IsUnknown() {
		p[key] = v.ValueString()
	}
}
func putBool(p map[string]any, key string, v types.Bool) {
	if !v.IsNull() && !v.IsUnknown() {
		p[key] = v.ValueBool()
	}
}
func putInt(p map[string]any, key string, v types.Int64) {
	if !v.IsNull() && !v.IsUnknown() {
		p[key] = v.ValueInt64()
	}
}
func putFloat(p map[string]any, key string, v types.Float64) {
	if !v.IsNull() && !v.IsUnknown() {
		p[key] = v.ValueFloat64()
	}
}
func nullableValue(v types.String) any {
	if v.IsNull() {
		return nil
	}
	return v.ValueString()
}
func nullableBool(v types.Bool) any {
	if v.IsNull() {
		return nil
	}
	return v.ValueBool()
}
func deploymentStringPointer(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
