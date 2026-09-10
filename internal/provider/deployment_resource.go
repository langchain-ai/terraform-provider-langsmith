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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
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
	_ resource.ResourceWithModifyPlan  = &DeploymentResource{}
)

type DeploymentResource struct {
	client       *langsmith.Client
	pollInterval time.Duration
	waitTimeout  time.Duration
}

type deploymentResourceModel struct {
	ID                   types.String                     `tfsdk:"id"`
	Name                 types.String                     `tfsdk:"name"`
	Source               types.String                     `tfsdk:"source"`
	DisplayName          types.String                     `tfsdk:"display_name"`
	SourceConfig         *deploymentSourceConfigModel     `tfsdk:"source_config"`
	SourceRevisionConfig *sourceRevisionConfigModel       `tfsdk:"source_revision_config"`
	EnvironmentVariables types.Map                        `tfsdk:"environment_variables"`
	Secrets              types.Map                        `tfsdk:"secrets"`
	SecretsVersion       types.String                     `tfsdk:"secrets_version"`
	SecretsHash          types.String                     `tfsdk:"secrets_hash"`
	SecretReferences     []deploymentSecretReferenceModel `tfsdk:"secret_references"`
	TenantID             types.String                     `tfsdk:"tenant_id"`
	CreatedAt            types.String                     `tfsdk:"created_at"`
	UpdatedAt            types.String                     `tfsdk:"updated_at"`
	Status               types.String                     `tfsdk:"status"`
	LatestRevisionID     types.String                     `tfsdk:"latest_revision_id"`
	ActiveRevisionID     types.String                     `tfsdk:"active_revision_id"`
	LatestRevisionStatus types.String                     `tfsdk:"latest_revision_status"`
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
	Secrets              []deploymentSecretAPI          `json:"secrets"`
	SecretReferences     []deploymentSecretReferenceAPI `json:"secret_references"`
	TenantID             string                         `json:"tenant_id"`
	CreatedAt            string                         `json:"created_at"`
	UpdatedAt            string                         `json:"updated_at"`
	Status               string                         `json:"status"`
	LatestRevisionID     *string                        `json:"latest_revision_id"`
	ActiveRevisionID     *string                        `json:"active_revision_id"`
}

type deploymentResourceRevisionAPI struct {
	ID     string `json:"id"`
	Status string `json:"status"`
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
	if data := configureProviderData(req.ProviderData, &resp.Diagnostics); data != nil {
		r.client = data.ControlPlaneClient
	}
}

// Optional+Computed fields adopt API defaults. Desired-only inputs retain
// Terraform's configuration because the API may omit or normalize them.
// Public environment values refresh only previously classified keys.
func (r *DeploymentResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	immutable := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	computedString := func(description string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: description}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the desired state of a LangSmith deployment. Deployment revisions are created and tracked by the service; use the `langsmith_deployment_revision` data source to read one.\n\nThe v2 API has one environment map. Terraform separates it into ordinary `environment_variables`, which appear in plans and state, and sensitive write-only `secrets`. Their union replaces the entire API environment whenever either map is configured. Keys must not overlap. To migrate an existing `secrets` map, move ordinary entries into `environment_variables` without changing the combined keys or values; this records the public values in state without creating a revision.\n\nImport an existing deployment by UUID using the same workspace and control-plane URL. GitHub imports retain the configured branch when the API returns it and exclude the built image URI from writable inputs. Import does not populate either environment map because the API does not distinguish public values from secrets. Omit `environment_variables`, `secrets`, and `secrets_version` to preserve the existing environment without copying values into state. Configuring both maps after import records the public map in state; if their combined digest matches the remote environment, applying that plan creates no revision. Subsequent plans are empty until configuration or remote values change. Review the plan after import before applying changes.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       immutable,
				MarkdownDescription: "Deployment UUID.",
			},
			"name": schema.StringAttribute{
				Required:            true,
				PlanModifiers:       replace,
				Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
				MarkdownDescription: "Deployment name. A LangSmith tracing project of the same name is created alongside it. Changing this replaces the deployment.",
			},
			"source": schema.StringAttribute{
				Required:            true,
				PlanModifiers:       replace,
				Validators:          []frameworkvalidator.String{oneOfStringValidator{values: []string{"github", "external_docker", "internal_docker", "internal_source", "internal_template"}}},
				MarkdownDescription: "Where the deployment builds from: `github`, `external_docker`, `internal_docker`, `internal_source`, or `internal_template`. Self-hosted installs support `external_docker`. Changing this replaces the deployment.",
			},
			"display_name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
				MarkdownDescription: "Human-readable name. The service has no way to clear a display name once set, so removing this argument leaves the last value in place.",
			},
			"source_config": schema.SingleNestedAttribute{
				Required:            true,
				Attributes:          sourceConfigSchema(),
				MarkdownDescription: "Configuration that applies to the deployment as a whole.",
			},
			"source_revision_config": schema.SingleNestedAttribute{
				Required:            true,
				Attributes:          sourceRevisionConfigSchema(),
				MarkdownDescription: "Configuration for the code or image a revision builds from. Changing any argument here creates a new revision.",
			},
			"environment_variables": schema.MapAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Non-sensitive environment variables, stored in plaintext in Terraform plans and state. Together with `secrets`, this is the complete desired environment; updates replace the combined map. Keys must not overlap. Refresh reads only keys previously declared here and never discovers other API environment values. Keep credentials in `secrets`. Omit both maps to preserve the existing environment without managing it.",
			},
			"secrets": schema.MapAttribute{
				Optional:            true,
				Sensitive:           true,
				WriteOnly:           true,
				ElementType:         types.StringType,
				MarkdownDescription: "Write-only sensitive environment variable values, merged with `environment_variables` before sending to the deployment. Supply the complete environment across both maps; keys must not overlap. The provider hashes the combined map to detect configuration changes and remote drift without storing secret values. Omit both maps to relinquish management and preserve existing values. Omitting only this map while `environment_variables` remains configured removes secret-only entries on the next revision. The v2 API currently treats an empty combined map on update as unchanged, so the provider rejects those updates. An empty map is allowed on initial creation.",
			},
			"secrets_version": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional manual trigger for creating a revision carrying the current combined environment. Environment changes are detected automatically through `secrets_hash`; this argument is not required. Removing the trigger does not create a revision.",
			},
			"secrets_hash": schema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "SHA-256 digest of the combined `environment_variables` and `secrets` maps, encoded as JSON with sorted keys. Used to compare configured values with values returned by the v2 API during refresh. Secret values remain write-only; ordinary environment values are also stored in state. APIs that omit secrets cannot report remote environment drift. A deterministic digest can still permit guesses if the entire map is predictable; protect access to state.",
			},
			"secret_references": schema.ListNestedAttribute{
				Optional:            true,
				MarkdownDescription: "References to existing Kubernetes Secrets to expose as environment variables. Only applicable to the `external_docker` source. Set this to `[]` to remove all references; removing the argument entirely leaves the previous revision's references in place.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{
						Required:            true,
						Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
						MarkdownDescription: "Name of the environment variable to populate.",
					},
					"secret_name": schema.StringAttribute{
						Required:            true,
						Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
						MarkdownDescription: "Name of an existing Kubernetes Secret in the deployment's namespace.",
					},
					"secret_key": schema.StringAttribute{
						Required:            true,
						Validators:          []frameworkvalidator.String{nonEmptyStringValidator{}},
						MarkdownDescription: "Key within that Secret to read the value from.",
					},
				}},
			},
			"tenant_id": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       immutable,
				MarkdownDescription: "Owning workspace (tenant) UUID.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				PlanModifiers:       immutable,
				MarkdownDescription: "Creation timestamp.",
			},
			"updated_at":             computedString("Last update timestamp."),
			"status":                 computedString("Deployment status, one of `AWAITING_DATABASE`, `READY`, `UNUSED`, `AWAITING_DELETE`, `AWAITING_FINAL_DELETE`, or `UNKNOWN`."),
			"latest_revision_id":     computedString("UUID of the most recently created revision."),
			"active_revision_id":     computedString("UUID of the revision currently serving traffic."),
			"latest_revision_status": computedString("Status of the most recently created revision."),
		},
	}
}

func sourceConfigSchema() map[string]schema.Attribute {
	// Optional+Computed adopts server values when an argument is omitted.
	serverOwnedString := func(description string, replaces bool, validators ...frameworkvalidator.String) schema.StringAttribute {
		modifiers := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
		if replaces {
			modifiers = append(modifiers, stringplanmodifier.RequiresReplace())
		}
		return schema.StringAttribute{
			Optional:            true,
			Computed:            true,
			PlanModifiers:       modifiers,
			Validators:          validators,
			MarkdownDescription: description,
		}
	}
	return map[string]schema.Attribute{
		"integration_id":  serverOwnedString("UUID of the GitHub integration to build through. Only applicable to the `github` source. Changing this replaces the deployment.", true, nonEmptyStringValidator{}),
		"repo_url":        serverOwnedString("URL of the repository to build from. Only applicable to the `github` source. Changing this replaces the deployment.", true, nonEmptyStringValidator{}),
		"deployment_type": serverOwnedString("Deployment tier: `dev_free`, `dev`, `prod`, `dev_zero`, or `dev_free_zero`. The service defaults this to `prod`. Changing it replaces the deployment.", true, oneOfStringValidator{values: []string{"dev_free", "dev", "prod", "dev_zero", "dev_free_zero"}}),
		"listener_id":     serverOwnedString("UUID of the listener to bind the deployment to. Changing this replaces the deployment.", true, nonEmptyStringValidator{}),
		"template_id":     serverOwnedString("Identifier of the LangChain template to deploy. Only applicable to the `internal_template` source. Changing this replaces the deployment.", true, nonEmptyStringValidator{}),
		"custom_url":      serverOwnedString("Custom hostname to serve the deployment on. The service has no way to clear this once set, so removing the argument leaves the last value in place.", false),
		"build_on_push": schema.BoolAttribute{
			Optional:            true,
			Computed:            true,
			PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			MarkdownDescription: "Rebuild automatically when the tracked git ref moves. Must be `false` when `source_revision_config.repo_ref` names a tag. The service has no way to clear this once set, so removing the argument leaves the last value in place.",
		},
		"install_command": schema.StringAttribute{
			Optional:            true,
			MarkdownDescription: "Command used to install dependencies during a JS build. Retained as desired configuration because older API versions do not return it.",
		},
		"build_command": schema.StringAttribute{
			Optional:            true,
			MarkdownDescription: "Command used to build a JS deployment. Retained as desired configuration because older API versions do not return it.",
		},
		"listener_config": schema.SingleNestedAttribute{
			Optional:            true,
			MarkdownDescription: "Listener settings. The service does not report these back, so Terraform is their source of truth.",
			Attributes: map[string]schema.Attribute{
				"k8s_namespace": schema.StringAttribute{
					Optional:            true,
					PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
					MarkdownDescription: "Kubernetes namespace to deploy into. Changing this replaces the deployment.",
				},
			},
		},
		"resource_spec": schema.SingleNestedAttribute{
			Optional:            true,
			MarkdownDescription: "Compute resources for the deployment. Configured fields are merged with the current API resource specification before each revision, preserving unconfigured fields, defaults, and fields this schema does not model. Removing an argument relinquishes management of that field and preserves its current value. Changing any argument creates a new revision.",
			Attributes: map[string]schema.Attribute{
				"min_scale": schema.Int64Attribute{
					Optional:            true,
					MarkdownDescription: "Minimum replica count. Only `dev_zero` deployment types may scale to 0.",
				},
				"max_scale": schema.Int64Attribute{
					Optional:            true,
					MarkdownDescription: "Maximum replica count.",
				},
				"cpu": schema.Float64Attribute{
					Optional:            true,
					MarkdownDescription: "CPU request, in cores.",
				},
				"cpu_limit": schema.Float64Attribute{
					Optional:            true,
					MarkdownDescription: "CPU limit, in cores.",
				},
				"memory_mb": schema.Int64Attribute{
					Optional:            true,
					MarkdownDescription: "Memory request, in MiB.",
				},
				"memory_limit_mb": schema.Int64Attribute{
					Optional:            true,
					MarkdownDescription: "Memory limit, in MiB.",
				},
				"labels": schema.MapAttribute{
					Optional:            true,
					ElementType:         types.StringType,
					MarkdownDescription: "Kubernetes labels to apply to the deployment's pods.",
				},
				"annotations": schema.MapAttribute{
					Optional:            true,
					ElementType:         types.StringType,
					MarkdownDescription: "Kubernetes annotations to apply to the deployment's pods.",
				},
				"service_account_name": schema.StringAttribute{
					Optional:            true,
					MarkdownDescription: "Kubernetes service account to run the deployment under.",
				},
			},
		},
	}
}

func sourceRevisionConfigSchema() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"repo_ref": schema.StringAttribute{
			Optional:            true,
			MarkdownDescription: "Git ref to build: a branch name, or a full ref path for a tag. Tags require `source_config.build_on_push` to be `false`. Only applicable to the `github` source.",
		},
		"langgraph_config_path": schema.StringAttribute{
			Optional:            true,
			MarkdownDescription: "Path to `langgraph.json` within the repository. Required for the `github` and `internal_source` sources.",
		},
		"image_uri": schema.StringAttribute{
			Optional:            true,
			MarkdownDescription: "Docker image to deploy, as `<name>:<tag>`. Only applicable to the `external_docker` source.",
		},
		"source_tarball_path": schema.StringAttribute{
			Optional:            true,
			MarkdownDescription: "Object path of an uploaded source tarball, obtained from the deployment's upload-url endpoint. Only applicable to the `internal_source` source, and only for a deployment that already exists.",
		},
	}
}

func (r *DeploymentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, config deploymentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := setDeploymentSecrets(&plan, config); err != nil {
		resp.Diagnostics.AddError("Invalid Deployment Secrets", err.Error())
		return
	}
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
	if err := setDeploymentSecrets(&plan, config); err != nil {
		resp.Diagnostics.AddError("Invalid Deployment Secrets", err.Error())
		return
	}
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
	// Display names require a separate PATCH. Preserve the ID if it fails.
	if !plan.DisplayName.IsNull() && !plan.DisplayName.IsUnknown() {
		var ignored deploymentAPI
		if err := r.client.Patch(ctx, deploymentPath(result.ID), map[string]any{"display_name": plan.DisplayName.ValueString()}, &ignored); err != nil {
			return interim, err
		}
	}
	revision, err := r.waitForRevision(ctx, result.ID, *result.LatestRevisionID)
	if err != nil {
		interim.LatestRevisionStatus = nullableString(revision.Status)
		return interim, err
	}
	// Keep the created ID if the final read fails; the plan's ID is unknown.
	model, err := r.read(ctx, result.ID, plan)
	if err != nil {
		interim.LatestRevisionStatus = nullableString(revision.Status)
		return interim, err
	}
	return model, nil
}

func (r *DeploymentResource) read(ctx context.Context, id string, previous deploymentResourceModel) (deploymentResourceModel, error) {
	var result deploymentAPI
	if err := r.client.Get(ctx, deploymentPath(id), nil, &result); err != nil {
		return previous, err
	}
	var revision deploymentResourceRevisionAPI
	if result.LatestRevisionID != nil {
		// Only a deployment 404 may remove the resource from state.
		if err := r.client.Get(ctx, deploymentRevisionPath(id, *result.LatestRevisionID), nil, &revision); err != nil && !isLangSmithNotFound(err) {
			return previous, err
		}
	}
	model := deploymentModelFromAPI(result, revision, previous)
	if result.Secrets != nil {
		hash, err := deploymentAPISecretsHash(result.Secrets)
		if err != nil {
			return previous, err
		}
		model.SecretsHash = hash
		if !previous.EnvironmentVariables.IsNull() && !previous.EnvironmentVariables.IsUnknown() {
			// The API does not distinguish public and secret entries. Only keys
			// already classified as public may be copied into ordinary state.
			public := make(map[string]attr.Value)
			declared := previous.EnvironmentVariables.Elements()
			for _, entry := range result.Secrets {
				if _, ok := declared[entry.Name]; ok {
					public[entry.Name] = types.StringValue(*entry.Value)
				}
			}
			model.EnvironmentVariables = types.MapValueMust(types.StringType, public)
		}
	}
	return model, nil
}

func (r *DeploymentResource) update(ctx context.Context, state, plan deploymentResourceModel) (deploymentResourceModel, error) {
	id := state.ID.ValueString()
	needsRevision := revisionChanged(state, plan)
	var payload map[string]any
	var revision deploymentResourceRevisionAPI
	if needsRevision {
		if (!plan.Secrets.IsNull() || !plan.EnvironmentVariables.IsNull()) && len(secretsPayload(plan.EnvironmentVariables, plan.Secrets)) == 0 {
			return state, errors.New("the v2 deployment API does not clear secrets when given an empty combined map; omit both environment_variables and secrets to preserve existing values, or supply a non-empty combined map; clearing the entire environment requires an API fix")
		}
		var err error
		payload, err = r.revisionUpdatePayload(ctx, id, plan)
		if err != nil {
			return state, err
		}
	}
	if mutable := mutablePayload(state, plan); len(mutable) > 0 {
		var ignored deploymentAPI
		if err := r.client.Patch(ctx, deploymentPath(id), mutable, &ignored); err != nil {
			return state, err
		}
	}
	if needsRevision {
		if err := r.client.Post(ctx, deploymentRevisionsPath(id), payload, &revision); err != nil {
			return state, err
		}
		if revision.ID == "" {
			return r.applied(ctx, id, state, plan, revision), errors.New("LangSmith did not return a revision ID")
		}
		waited, err := r.waitForRevision(ctx, id, revision.ID)
		if waited.ID != "" {
			revision.ID = waited.ID
		}
		if waited.Status != "" {
			revision.Status = waited.Status
		}
		if err != nil {
			return r.applied(ctx, id, state, plan, revision), err
		}
	}
	model, err := r.read(ctx, id, plan)
	if err != nil {
		return r.applied(ctx, id, state, plan, revision), err
	}
	return model, nil
}

func (r *DeploymentResource) revisionUpdatePayload(ctx context.Context, id string, plan deploymentResourceModel) (map[string]any, error) {
	payload := revisionPayload(plan)
	if plan.SourceConfig == nil || plan.SourceConfig.ResourceSpec == nil {
		return payload, nil
	}
	var current deploymentAPI
	if err := r.client.Get(ctx, deploymentPath(id), nil, &current); err != nil {
		return nil, fmt.Errorf("reading current resource specification before revision: %w", err)
	}
	// The API replaces the entire object, including fields Terraform does not
	// model. Read immediately before writing so omitted fields keep their values.
	merged := map[string]any{}
	if existing, ok := current.SourceConfig["resource_spec"].(map[string]any); ok {
		for key, value := range existing {
			merged[key] = value
		}
	}
	for key, value := range resourceSpecPayload(plan.SourceConfig.ResourceSpec) {
		merged[key] = value
	}
	sourceConfig := revisionSourceConfig(plan.SourceConfig)
	sourceConfig["resource_spec"] = merged
	payload["source_config"] = sourceConfig
	return payload, nil
}

// Preserve accepted revision inputs and known computed values if polling fails,
// so retries do not create another revision.
func (r *DeploymentResource) applied(ctx context.Context, id string, state, plan deploymentResourceModel, revision deploymentResourceRevisionAPI) deploymentResourceModel {
	model, err := r.read(ctx, id, plan)
	if err != nil {
		model = state
		model.SecretsVersion = plan.SecretsVersion
		model.SecretsHash = plan.SecretsHash
		model.EnvironmentVariables = plan.EnvironmentVariables
		model.SecretReferences = plan.SecretReferences
		model.SourceRevisionConfig = plan.SourceRevisionConfig
		if model.SourceConfig != nil && plan.SourceConfig != nil {
			merged := *model.SourceConfig
			merged.InstallCommand = plan.SourceConfig.InstallCommand
			merged.BuildCommand = plan.SourceConfig.BuildCommand
			merged.ListenerConfig = plan.SourceConfig.ListenerConfig
			merged.ResourceSpec = plan.SourceConfig.ResourceSpec
			model.SourceConfig = &merged
		}
	}
	if revision.ID != "" {
		model.LatestRevisionID = types.StringValue(revision.ID)
	}
	if revision.Status != "" {
		model.LatestRevisionStatus = types.StringValue(revision.Status)
	}
	return model
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

// SKIPPED revisions were superseded; INTERRUPTED and UNKNOWN can still progress.
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
		case "DEPLOYED", "SKIPPED":
			return revision, nil
		case "CREATE_FAILED", "BUILD_FAILED", "DEPLOY_FAILED":
			return revision, fmt.Errorf("revision %s failed with status %s", revisionID, revision.Status)
		}
		select {
		case <-ctx.Done():
			return revision, fmt.Errorf("waiting for revision %s: %w", revisionID, ctx.Err())
		case <-time.After(interval):
		}
	}
}

func createPayload(m deploymentResourceModel) map[string]any {
	p := map[string]any{
		"name":                   m.Name.ValueString(),
		"source":                 m.Source.ValueString(),
		"source_config":          sourceConfigPayload(m.SourceConfig),
		"source_revision_config": sourceRevisionPayload(m.SourceRevisionConfig),
		"secrets":                secretsPayload(m.EnvironmentVariables, m.Secrets),
	}
	if m.SecretReferences != nil {
		p["secret_references"] = secretReferencesPayload(m.SecretReferences)
	}
	return p
}

func mutablePayload(state, plan deploymentResourceModel) map[string]any {
	p := map[string]any{}
	if changedValue(state.DisplayName, plan.DisplayName) {
		p["display_name"] = plan.DisplayName.ValueString()
	}
	sourceConfig := map[string]any{}
	if state.SourceConfig != nil && plan.SourceConfig != nil {
		if !plan.SourceConfig.BuildOnPush.IsUnknown() && !state.SourceConfig.BuildOnPush.Equal(plan.SourceConfig.BuildOnPush) && !plan.SourceConfig.BuildOnPush.IsNull() {
			sourceConfig["build_on_push"] = plan.SourceConfig.BuildOnPush.ValueBool()
		}
		if changedValue(state.SourceConfig.CustomURL, plan.SourceConfig.CustomURL) {
			sourceConfig["custom_url"] = plan.SourceConfig.CustomURL.ValueString()
		}
	}
	if len(sourceConfig) > 0 {
		p["source_config"] = sourceConfig
	}
	return p
}

// Null cannot clear an API field; unknown carries no value to send.
func changedValue(state, plan types.String) bool {
	if plan.IsUnknown() || plan.IsNull() {
		return false
	}
	return !state.Equal(plan)
}

func revisionChanged(state, plan deploymentResourceModel) bool {
	if changedValue(state.SecretsVersion, plan.SecretsVersion) || changedValue(state.SecretsHash, plan.SecretsHash) {
		return true
	}
	if !reflect.DeepEqual(state.SourceRevisionConfig, plan.SourceRevisionConfig) {
		return true
	}
	if !reflect.DeepEqual(state.SecretReferences, plan.SecretReferences) {
		return true
	}
	return !reflect.DeepEqual(revisionSourceConfig(state.SourceConfig), revisionSourceConfig(plan.SourceConfig))
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

func revisionPayload(m deploymentResourceModel) map[string]any {
	p := map[string]any{"source_revision_config": sourceRevisionPayload(m.SourceRevisionConfig)}
	if sourceConfig := revisionSourceConfig(m.SourceConfig); len(sourceConfig) > 0 {
		p["source_config"] = sourceConfig
	}
	// Either configured map manages the complete environment. Omitting both
	// carries the previous revision's values over; an empty update is rejected.
	if (!m.Secrets.IsNull() || !m.EnvironmentVariables.IsNull()) && !m.Secrets.IsUnknown() && !m.EnvironmentVariables.IsUnknown() {
		p["secrets"] = secretsPayload(m.EnvironmentVariables, m.Secrets)
	}
	if m.SecretReferences != nil {
		p["secret_references"] = secretReferencesPayload(m.SecretReferences)
	}
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

func secretsPayload(maps ...types.Map) []map[string]string {
	values := map[string]string{}
	for _, m := range maps {
		for key, value := range stringMap(m) {
			values[key] = value
		}
	}
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
	out := make([]deploymentSecretReferenceAPI, 0, len(refs))
	for _, ref := range refs {
		out = append(out, deploymentSecretReferenceAPI{ref.Name.ValueString(), ref.SecretName.ValueString(), ref.SecretKey.ValueString()})
	}
	return out
}

func deploymentModelFromAPI(api deploymentAPI, revision deploymentResourceRevisionAPI, previous deploymentResourceModel) deploymentResourceModel {
	next := previous
	next.ID = types.StringValue(api.ID)
	next.Name = types.StringValue(api.Name)
	next.Source = types.StringValue(api.Source)
	next.DisplayName = nullableStringPointer(api.DisplayName)

	adopt := previous.Name.IsNull() || previous.Name.IsUnknown()
	sourceConfig := sourceConfigModelFromAPI(api.SourceConfig)
	if !adopt && previous.SourceConfig != nil {
		// Some API versions omit commands and listener settings. Resource specs
		// include server defaults beyond Terraform's configured fields.
		sourceConfig.InstallCommand = previous.SourceConfig.InstallCommand
		sourceConfig.BuildCommand = previous.SourceConfig.BuildCommand
		sourceConfig.ListenerConfig = previous.SourceConfig.ListenerConfig
		sourceConfig.ResourceSpec = previous.SourceConfig.ResourceSpec
	}
	next.SourceConfig = sourceConfig

	// Import adopts observed revision inputs; later reads preserve desired inputs.
	if adopt && api.SourceRevisionConfig != nil {
		next.SourceRevisionConfig = sourceRevisionModelFromAPI(api)
	}
	// An empty imported list stays null to match an omitted argument.
	if (adopt && len(api.SecretReferences) > 0) || (!adopt && previous.SecretReferences != nil) {
		next.SecretReferences = secretReferenceModelsFromAPI(api.SecretReferences)
	}

	next.TenantID = nullableString(api.TenantID)
	next.CreatedAt = nullableString(api.CreatedAt)
	next.UpdatedAt = nullableString(api.UpdatedAt)
	next.Status = nullableString(api.Status)
	next.LatestRevisionID = nullableStringPointer(api.LatestRevisionID)
	next.ActiveRevisionID = nullableStringPointer(api.ActiveRevisionID)
	next.LatestRevisionStatus = nullableString(revision.Status)
	next.Secrets = types.MapNull(types.StringType)
	if next.SecretsHash.IsUnknown() {
		next.SecretsHash = types.StringNull()
	}
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

func sourceRevisionModelFromAPI(api deploymentAPI) *sourceRevisionConfigModel {
	model := &sourceRevisionConfigModel{}
	switch api.Source {
	case "github":
		model.RepoRef = apiString(api.SourceConfig, "repo_branch")
		if model.RepoRef.IsNull() {
			model.RepoRef = apiString(api.SourceRevisionConfig, "repo_ref")
		}
		model.LanggraphConfigPath = apiString(api.SourceRevisionConfig, "langgraph_config_path")
	case "external_docker", "internal_docker":
		model.ImageURI = apiString(api.SourceRevisionConfig, "image_uri")
	case "internal_source":
		model.LanggraphConfigPath = apiString(api.SourceRevisionConfig, "langgraph_config_path")
		model.SourceTarballPath = apiString(api.SourceRevisionConfig, "source_tarball_path")
	}
	return model
}

func secretReferenceModelsFromAPI(api []deploymentSecretReferenceAPI) []deploymentSecretReferenceModel {
	result := make([]deploymentSecretReferenceModel, 0, len(api))
	for _, ref := range api {
		result = append(result, deploymentSecretReferenceModel{Name: types.StringValue(ref.Name), SecretName: types.StringValue(ref.SecretName), SecretKey: types.StringValue(ref.SecretKey)})
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
	converted := make(map[string]attr.Value, len(value))
	for k, raw := range value {
		if s, ok := raw.(string); ok {
			converted[k] = types.StringValue(s)
		}
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
