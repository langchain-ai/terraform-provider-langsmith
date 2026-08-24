package provider

// Manages a LangSmith Model Configuration.
//
// TODO: support oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
)

// data models

// modelConfigurationModel maps model configuration terraform configuration data.
type modelConfigurationModel struct {
	BaseURL          types.String `tfsdk:"base_url"`
	CreatedAt        types.String `tfsdk:"created_at"`
	EnvVarName       types.String `tfsdk:"env_var_name"`
	ID               types.String `tfsdk:"id"`
	InvocationParams types.String `tfsdk:"invocation_params"`
	Model            types.String `tfsdk:"model"`
	Name             types.String `tfsdk:"name"`
	OrganizationID   types.String `tfsdk:"organization_id"`
	Provider         types.String `tfsdk:"model_provider"`
	Scope            types.String `tfsdk:"scope"`
	UpdatedAt        types.String `tfsdk:"updated_at"`
}

const modelConfigurationsPath = "api/v1/playground-settings"

const (
	modelConfigScopeWorkspace    = "workspace"
	modelConfigScopeOrganization = "organization"

	modelConfigSettingsTypeSimple  = "simple"
	modelConfigSettingsTypeComplex = "complex"
)

var modelConfigScopes = []string{
	modelConfigScopeWorkspace,
	modelConfigScopeOrganization,
}

// modelConfigProviderSpec describes how a provider's model, credential and
// base URL map onto the backend's serialization.
type modelConfigProviderSpec struct {
	// lcClassName is settings.ID's last element (e.g. "ChatOpenAI"). Matching
	// on the class name alone, rather than the full id path.
	lcClassName  string
	modelKwarg   string
	secretKwarg  string
	baseURLKwarg string
}

var modelConfigProviders = map[string]modelConfigProviderSpec{
	"openai": {
		lcClassName:  "ChatOpenAI",
		modelKwarg:   "model",
		secretKwarg:  "openai_api_key",
		baseURLKwarg: "openai_api_base",
	},
	"anthropic": {
		lcClassName:  "ChatAnthropic",
		modelKwarg:   "model",
		secretKwarg:  "anthropic_api_key",
		baseURLKwarg: "base_url",
	},
	"azure_openai": {
		lcClassName: "AzureChatOpenAI",
		modelKwarg:  "model",
		secretKwarg: "openai_api_key",
		// Azure has no dedicated base URL kwarg; it reads azure_endpoint out of
		// additional_kwargs, falling back to the top-level baseUrl (see
		// simple_to_complex in smith-backend's playground_settings endpoint).
		baseURLKwarg: "",
	},
}

var modelConfigProviderNames = slices.Sorted(maps.Keys(modelConfigProviders))

// modelConfigurationSettingsAPI is the "simple" settings shape this resource writes.
type modelConfigurationSettingsAPI struct {
	ModelID          string         `json:"modelId"`
	EnvVarName       string         `json:"envVarName"`
	BaseURL          *string        `json:"baseUrl,omitempty"`
	AdditionalKwargs map[string]any `json:"additional_kwargs,omitempty"`
}

// modelConfigurationCreateAPI defines the request body for creating a model configuration.
type modelConfigurationCreateAPI struct {
	Name         *string         `json:"name"`
	Scope        string          `json:"scope"`
	Settings     json.RawMessage `json:"settings"`
	SettingsType string          `json:"settings_type"`
}

// modelConfigurationUpdateAPI defines the request body for updating a model configuration.
//
// scope is intentionally absent: the API has no field to change it after
// creation (see the RequiresReplace plan modifier on the scope attribute).
type modelConfigurationUpdateAPI struct {
	Name     *string         `json:"name"`
	Settings json.RawMessage `json:"settings"`
}

// modelConfigurationGetAPI maps a PlaygroundSettingsResponse from the platform API.
type modelConfigurationGetAPI struct {
	ID             string          `json:"id"`
	Name           *string         `json:"name"`
	OrganizationID *string         `json:"organization_id"`
	Settings       json.RawMessage `json:"settings"`
	SettingsType   string          `json:"settings_type"`
	CreatedAt      string          `json:"created_at"`
	UpdatedAt      string          `json:"updated_at"`
}

// modelConfigurationSettingsInfo is what can be recovered from either the
// "simple" or "complex" settings shape returned by the API.
type modelConfigurationSettingsInfo struct {
	Provider   string
	Model      string
	EnvVarName string
	BaseURL    *string
}

// data model conversion functions

// terraform model from API responses

// modelConfigurationModelFromAPI maps the API data to the state data.
func modelConfigurationModelFromAPI(api modelConfigurationGetAPI, previous modelConfigurationModel) (modelConfigurationModel, error) {
	next := previous
	next.ID = types.StringValue(api.ID)
	next.Name = nullableStringPointer(api.Name)
	next.CreatedAt = nullableString(api.CreatedAt)
	next.UpdatedAt = nullableString(api.UpdatedAt)
	if api.OrganizationID != nil {
		next.Scope = types.StringValue(modelConfigScopeOrganization)
		next.OrganizationID = types.StringValue(*api.OrganizationID)
	} else {
		next.Scope = types.StringValue(modelConfigScopeWorkspace)
		next.OrganizationID = types.StringNull()
	}

	info, err := modelConfigurationSettingsInfoFromAPI(api.SettingsType, api.Settings)
	if err != nil {
		return modelConfigurationModel{}, err
	}
	next.Provider = types.StringValue(info.Provider)
	next.Model = types.StringValue(info.Model)
	next.EnvVarName = types.StringValue(info.EnvVarName)
	next.BaseURL = nullableStringPointer(info.BaseURL)

	return next, nil
}

// modelConfigurationSettingsInfoFromAPI decodes provider/model/env_var_name/base_url
// out of either the "simple" or "complex" settings shape.
func modelConfigurationSettingsInfoFromAPI(settingsType string, raw json.RawMessage) (modelConfigurationSettingsInfo, error) {
	switch settingsType {
	case modelConfigSettingsTypeSimple:
		var settings modelConfigurationSettingsAPI
		if err := json.Unmarshal(raw, &settings); err != nil {
			return modelConfigurationSettingsInfo{}, fmt.Errorf("decode simple settings: %w", err)
		}
		provider, model, ok := strings.Cut(settings.ModelID, ":")
		if !ok {
			return modelConfigurationSettingsInfo{}, fmt.Errorf("modelId %q is not in \"provider:model\" format", settings.ModelID)
		}
		return modelConfigurationSettingsInfo{
			Provider:   provider,
			Model:      model,
			EnvVarName: settings.EnvVarName,
			BaseURL:    settings.BaseURL,
		}, nil
	case modelConfigSettingsTypeComplex:
		var settings struct {
			ID     []string       `json:"id"`
			Kwargs map[string]any `json:"kwargs"`
		}
		if err := json.Unmarshal(raw, &settings); err != nil {
			return modelConfigurationSettingsInfo{}, fmt.Errorf("decode complex settings: %w", err)
		}
		if len(settings.ID) == 0 {
			return modelConfigurationSettingsInfo{}, fmt.Errorf("complex settings have an empty id path")
		}
		lcClassName := settings.ID[len(settings.ID)-1]
		var provider string
		var spec modelConfigProviderSpec
		for name, s := range modelConfigProviders {
			if s.lcClassName == lcClassName {
				provider, spec = name, s
				break
			}
		}
		if provider == "" {
			return modelConfigurationSettingsInfo{}, fmt.Errorf(
				"settings id path %v does not match a supported provider; supported: %s",
				settings.ID, strings.Join(modelConfigProviderNames, ", "),
			)
		}
		model, ok := settings.Kwargs[spec.modelKwarg].(string)
		if !ok {
			return modelConfigurationSettingsInfo{}, fmt.Errorf(
				"complex settings for provider %q are missing kwargs[%q] (model)", provider, spec.modelKwarg,
			)
		}
		envVarName, err := modelConfigEnvVarNameFromSecretRef(settings.Kwargs[spec.secretKwarg])
		if err != nil {
			return modelConfigurationSettingsInfo{}, fmt.Errorf(
				"complex settings for provider %q, kwargs[%q]: %w", provider, spec.secretKwarg, err,
			)
		}

		info := modelConfigurationSettingsInfo{Provider: provider, Model: model, EnvVarName: envVarName}
		if spec.baseURLKwarg != "" {
			if v, ok := settings.Kwargs[spec.baseURLKwarg].(string); ok {
				info.BaseURL = &v
			}
		}
		if info.BaseURL == nil {
			// azure_openai's endpoint (and any provider's server-added fallback) lands here.
			if v, ok := settings.Kwargs["azure_endpoint"].(string); ok {
				info.BaseURL = &v
			}
		}
		return info, nil
	default:
		return modelConfigurationSettingsInfo{}, fmt.Errorf(
			"settings_type %q is not supported by this provider; supported types: %s, %s",
			settingsType, modelConfigSettingsTypeSimple, modelConfigSettingsTypeComplex,
		)
	}
}

// modelConfigEnvVarNameFromSecretRef extracts the env var name out of a
// LangChain secret reference (`{"lc":1,"type":"secret","id":["ENV_VAR"]}`).
func modelConfigEnvVarNameFromSecretRef(raw any) (string, error) {
	secretRef, ok := raw.(map[string]any)
	if !ok {
		return "", fmt.Errorf("expected a secret reference object, got %T", raw)
	}
	ids, ok := secretRef["id"].([]any)
	if !ok || len(ids) == 0 {
		return "", fmt.Errorf("secret reference has no non-empty \"id\" array")
	}
	envVarName, ok := ids[0].(string)
	if !ok {
		return "", fmt.Errorf("secret reference \"id\"[0] is not a string")
	}
	return envVarName, nil
}

// API request models from terraform models

// modelConfigurationCreateAPIFromModel converts the plan model to the create API request.
func modelConfigurationCreateAPIFromModel(plan modelConfigurationModel) (modelConfigurationCreateAPI, error) {
	settings, err := modelConfigurationSettingsAPIFromModel(plan)
	if err != nil {
		return modelConfigurationCreateAPI{}, err
	}
	return modelConfigurationCreateAPI{
		Name:         plan.Name.ValueStringPointer(),
		Scope:        plan.Scope.ValueString(),
		Settings:     settings,
		SettingsType: modelConfigSettingsTypeSimple,
	}, nil
}

// modelConfigurationUpdateAPIFromModel converts the plan model to the update API request.
func modelConfigurationUpdateAPIFromModel(plan modelConfigurationModel) (modelConfigurationUpdateAPI, error) {
	settings, err := modelConfigurationSettingsAPIFromModel(plan)
	if err != nil {
		return modelConfigurationUpdateAPI{}, err
	}
	return modelConfigurationUpdateAPI{
		Name:     plan.Name.ValueStringPointer(),
		Settings: settings,
	}, nil
}

// modelConfigurationSettingsAPIFromModel builds the "simple" settings JSON sent to the API.
func modelConfigurationSettingsAPIFromModel(plan modelConfigurationModel) (json.RawMessage, error) {
	provider := plan.Provider.ValueString()
	if _, ok := modelConfigProviders[provider]; !ok {
		return nil, fmt.Errorf("unsupported provider %q; supported: %s", provider, strings.Join(modelConfigProviderNames, ", "))
	}

	kwargs, err := modelConfigurationKwargsFromModel(plan.InvocationParams)
	if err != nil {
		return nil, err
	}

	settings := modelConfigurationSettingsAPI{
		ModelID:          provider + ":" + plan.Model.ValueString(),
		EnvVarName:       plan.EnvVarName.ValueString(),
		BaseURL:          stringPtr(plan.BaseURL),
		AdditionalKwargs: kwargs,
	}

	raw, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("marshal settings: %w", err)
	}
	return raw, nil
}

// modelConfigurationKwargsFromModel decodes invocation_params into a kwargs map.
func modelConfigurationKwargsFromModel(invocationParams types.String) (map[string]any, error) {
	raw := stringPtr(invocationParams)
	if raw == nil {
		return map[string]any{}, nil
	}
	canonical, err := canonicalJSONObject(*raw, "invocation_params")
	if err != nil {
		return nil, err
	}
	var kwargs map[string]any
	if err := json.Unmarshal([]byte(canonical), &kwargs); err != nil {
		return nil, fmt.Errorf("decode canonical invocation_params: %w", err)
	}
	return kwargs, nil
}

// Terraform resource implementation

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &modelConfigurationResource{}
	_ resource.ResourceWithConfigure   = &modelConfigurationResource{}
	_ resource.ResourceWithImportState = &modelConfigurationResource{}
)

// NewModelConfigurationResource is a helper function to simplify the provider implementation.
func NewModelConfigurationResource() resource.Resource {
	return &modelConfigurationResource{}
}

// modelConfigurationResource is the resource implementation.
type modelConfigurationResource struct {
	client *langsmith.Client
}

// Metadata returns the resource type name.
func (r *modelConfigurationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_model_configuration"
}

// Schema defines the schema for the resource.
func (r *modelConfigurationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a LangSmith Model Configuration",
		Attributes: map[string]schema.Attribute{
			"base_url": schema.StringAttribute{
				Description: "Base URL override for the provider's API",
				Optional:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp of when the model configuration was created",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"env_var_name": schema.StringAttribute{
				Description: "Name of the environment variable, configured server-side, that holds the provider API key. This is a reference, not the credential itself.",
				Required:    true,
			},
			"id": schema.StringAttribute{
				Description: "The ID of the model configuration",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"invocation_params": schema.StringAttribute{
				Description: "Provider-specific parameters passed to the model at invocation time (e.g. OpenAI's temperature), as a JSON object, typically from `jsonencode(...)`. Write-only: not refreshed from the API on read. After `terraform import`, this attribute is always null in state. If configuration sets it, expect one diff on the next plan that clears after applying; if configuration also omits it, import is diff-free.",
				Optional:    true,
			},
			"model": schema.StringAttribute{
				Description: "The model name (e.g. gpt-4o)",
				Required:    true,
			},
			"name": schema.StringAttribute{
				Description: "The name of the model configuration",
				Required:    true,
			},
			"organization_id": schema.StringAttribute{
				Description: "The ID of the LangSmith organization to which the model configuration belongs. Only set when scope is \"organization\".",
				Computed:    true,
			},
			"model_provider": schema.StringAttribute{
				Description: "The model provider",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.OneOf(modelConfigProviderNames...),
				},
			},
			"scope": schema.StringAttribute{
				Description: "Whether the model configuration belongs to the workspace or the organization. Cannot be changed after creation.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(modelConfigScopeWorkspace),
				Validators: []validator.String{
					stringvalidator.OneOf(modelConfigScopes...),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp of when the model configuration was last updated",
				Computed:    true,
			},
		},
	}
}

// Create creates the resource and sets the initial Terraform state.
func (r *modelConfigurationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan modelConfigurationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiRequest, err := modelConfigurationCreateAPIFromModel(plan)
	if err != nil {
		resp.Diagnostics.AddError("Failed to build model configuration request", err.Error())
		return
	}
	var apiResponse modelConfigurationGetAPI
	if err := r.client.Post(ctx, modelConfigurationsPath, apiRequest, &apiResponse); err != nil {
		resp.Diagnostics.AddError("Failed to create model configuration", err.Error())
		return
	}
	state, err := modelConfigurationModelFromAPI(apiResponse, plan)
	if err != nil {
		resp.Diagnostics.AddError("Failed to decode model configuration", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Read refreshes the Terraform state with the latest data.
func (r *modelConfigurationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var currentState modelConfigurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &currentState)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var apiData modelConfigurationGetAPI
	apiPath := fmt.Sprintf("%s/%s", modelConfigurationsPath, currentState.ID.ValueString())
	if err := r.client.Get(ctx, apiPath, nil, &apiData); err != nil {
		resp.Diagnostics.AddError("Failed to read model configuration", err.Error())
		return
	}
	newState, err := modelConfigurationModelFromAPI(apiData, currentState)
	if err != nil {
		resp.Diagnostics.AddError("Failed to decode model configuration", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// ImportState imports the resource state from an ID.
func (r *modelConfigurationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *modelConfigurationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan modelConfigurationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiRequest, err := modelConfigurationUpdateAPIFromModel(plan)
	if err != nil {
		resp.Diagnostics.AddError("Failed to build model configuration request", err.Error())
		return
	}
	apiPath := fmt.Sprintf("%s/%s", modelConfigurationsPath, plan.ID.ValueString())
	var apiResponse modelConfigurationGetAPI
	if err := r.client.Patch(ctx, apiPath, apiRequest, &apiResponse); err != nil {
		resp.Diagnostics.AddError("Failed to update model configuration", err.Error())
		return
	}
	state, err := modelConfigurationModelFromAPI(apiResponse, plan)
	if err != nil {
		resp.Diagnostics.AddError("Failed to decode model configuration", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *modelConfigurationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state modelConfigurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiPath := fmt.Sprintf("%s/%s", modelConfigurationsPath, state.ID.ValueString())
	if err := r.client.Delete(ctx, apiPath, nil, nil); err != nil && !isLangSmithNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete model configuration", err.Error())
	}
}

// Configure adds the provider configured client to the resource.
func (r *modelConfigurationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
