package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type deploymentSecretAPI struct {
	Name  string  `json:"name"`
	Value *string `json:"value"`
}

func (r *DeploymentResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var secrets types.Map
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("secrets"), &secrets)...)
	var previous types.String
	if !req.State.Raw.IsNull() {
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("secrets_hash"), &previous)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	hash, err := deploymentSecretsHash(secrets)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("secrets"), "Invalid Deployment Secrets", err.Error())
		return
	}
	if secrets.IsNull() {
		hash = previous
		if req.State.Raw.IsNull() {
			hash = types.StringUnknown()
		}
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("secrets_hash"), hash)...)
	if !secrets.IsNull() && !hash.Equal(previous) {
		// Write-only edits do not cause the framework to invalidate computed
		// fields. A revision will change them even when every other input matches.
		for _, name := range []string{"updated_at", "status", "latest_revision_id", "active_revision_id", "latest_revision_status"} {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(name), types.StringUnknown())...)
		}
	}
}

func deploymentSecretsHash(secrets types.Map) (types.String, error) {
	if secrets.IsNull() {
		return types.StringNull(), nil
	}
	if secrets.IsUnknown() {
		return types.StringUnknown(), nil
	}
	values := make(map[string]string, len(secrets.Elements()))
	unknown := false
	for name, value := range secrets.Elements() {
		text, ok := value.(types.String)
		if !ok || text.IsNull() || name == "" {
			return types.StringNull(), errors.New("environment entries must have non-empty names and non-null string values")
		}
		unknown = unknown || text.IsUnknown()
		values[name] = text.ValueString()
	}
	if unknown {
		return types.StringUnknown(), nil
	}
	return deploymentEnvironmentHash(values), nil
}

func deploymentEnvironmentHash(values map[string]string) types.String {
	// encoding/json sorts map keys and preserves string boundaries, so key
	// order cannot cause drift and ambiguous concatenations cannot collide.
	encoded, _ := json.Marshal(values)
	return types.StringValue(fmt.Sprintf("%x", sha256.Sum256(encoded)))
}

func deploymentAPISecretsHash(secrets []deploymentSecretAPI) (types.String, error) {
	values := make(map[string]string, len(secrets))
	for _, secret := range secrets {
		_, duplicate := values[secret.Name]
		if secret.Name == "" || secret.Value == nil || duplicate {
			return types.StringNull(), errors.New("the deployment API returned an incomplete or duplicate environment entry; cannot compare secrets")
		}
		values[secret.Name] = *secret.Value
	}
	return deploymentEnvironmentHash(values), nil
}

func setDeploymentSecrets(plan *deploymentResourceModel, config deploymentResourceModel) error {
	plan.Secrets = config.Secrets
	if config.Secrets.IsNull() {
		return nil
	}
	hash, err := deploymentSecretsHash(config.Secrets)
	if err != nil {
		return err
	}
	if hash.IsUnknown() {
		return errors.New("environment values must be known at apply time")
	}
	if !plan.SecretsHash.IsNull() && !plan.SecretsHash.IsUnknown() && !plan.SecretsHash.Equal(hash) {
		return errors.New("environment values changed after planning; create a new plan before applying")
	}
	plan.SecretsHash = hash
	return nil
}
