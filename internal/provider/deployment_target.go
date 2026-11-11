package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.ResourceWithConfigValidators = &DeploymentResource{}

var errDeploymentTargetMismatch = errors.New("the deployment does not belong to the specified agent environment")

var deploymentAgentTypes = map[string]attr.Type{
	"agent_id": types.StringType, "environment": types.StringType,
}

type deploymentAgentAPI struct {
	AgentID     string `json:"agent_id"`
	Environment string `json:"environment"`
}

func deploymentAgentSchema() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Optional:            true,
		MarkdownDescription: "Agent and environment to deploy into. Configure either agent or name. Adding the matching target to a name-based deployment preserves it. Changing an existing target replaces the deployment and can delete managed runtime data. Retained as configured; changes to the association outside Terraform are not detected.",
		Attributes: map[string]schema.Attribute{
			"agent_id": schema.StringAttribute{
				Required: true, Validators: []validator.String{nonEmptyStringValidator{}},
				MarkdownDescription: "Public agent ID in the deployment workspace.",
			},
			"environment": schema.StringAttribute{
				Required: true, Validators: []validator.String{stringvalidator.OneOf("local", "development", "staging", "production")},
				MarkdownDescription: "Agent environment: local, development, staging, or production.",
			},
		},
	}
}

func (r *DeploymentResource) modifyAgentTargetPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() {
		return
	}
	var state deploymentResourceModel
	var agent types.Object
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("agent"), &agent)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.Agent.Equal(agent) {
		return
	}
	if !state.Agent.IsNull() {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("agent"))
		return
	}
	target := deploymentAgentFromObject(agent)
	if agent.IsUnknown() || target.AgentID == "" || target.Environment == "" {
		return
	}
	if err := r.verifyAgentTarget(ctx, state.ID.ValueString(), target, state.WorkspaceID); errors.Is(err, errDeploymentTargetMismatch) {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("agent"))
	} else if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("agent"), "Unable to Adopt Deployment Target", err.Error())
	}
}

func (r *DeploymentResource) verifyAgentTarget(ctx context.Context, id string, target deploymentAgentAPI, workspaceID types.String) error {
	var result struct {
		Resources []deploymentAPI `json:"resources"`
	}
	requestPath := deploymentsPath + "?" + url.Values{"deployment_ids": {id}, "agent_id": {target.AgentID}, "agent_environment": {target.Environment}}.Encode()
	if err := r.client.Get(ctx, requestPath, nil, &result, workspaceOpts(workspaceID)...); err != nil {
		return err
	}
	if result.Resources == nil {
		return errors.New("the deployment API did not return resources for target verification")
	}
	if !slices.ContainsFunc(result.Resources, func(deployment deploymentAPI) bool { return deployment.ID == id }) {
		return errDeploymentTargetMismatch
	}
	return nil
}

func (r *DeploymentResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(path.MatchRoot("name"), path.MatchRoot("agent")),
	}
}

func deploymentAgentFromObject(value types.Object) deploymentAgentAPI {
	attributes := value.Attributes()
	agentID, _ := attributes["agent_id"].(types.String)
	environment, _ := attributes["environment"].(types.String)
	return deploymentAgentAPI{
		AgentID:     agentID.ValueString(),
		Environment: environment.ValueString(),
	}
}

func deploymentAgentObject(agent deploymentAgentAPI) types.Object {
	return types.ObjectValueMust(deploymentAgentTypes, map[string]attr.Value{
		"agent_id": types.StringValue(agent.AgentID), "environment": types.StringValue(agent.Environment),
	})
}

// A deployment GET hides deleting rows before their environment can be reused.
func (r *DeploymentResource) waitForAgentEnvironment(ctx context.Context, target deploymentAgentAPI, workspaceID types.String) (err error) {
	interval, timeout := r.pollSettings()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("agent %s environment %s is still being cleaned up; retry terraform apply: %w", target.AgentID, target.Environment, err)
		}
	}()
	for {
		pending := false
		for _, status := range []string{"AWAITING_DELETE", "AWAITING_FINAL_DELETE"} {
			var result struct {
				Resources []deploymentAPI `json:"resources"`
			}
			requestPath := deploymentsPath + "?" + url.Values{"agent_id": {target.AgentID}, "agent_environment": {target.Environment}, "status": {status}}.Encode()
			if err := r.client.Get(ctx, requestPath, nil, &result, workspaceOpts(workspaceID)...); err != nil {
				return err
			}
			if result.Resources == nil {
				return fmt.Errorf("the deployment API did not return deletion status for agent %s environment %s", target.AgentID, target.Environment)
			}
			pending = pending || len(result.Resources) > 0
		}
		if !pending {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
