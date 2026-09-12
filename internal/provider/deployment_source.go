package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

func validateDeploymentSource(source string) error {
	switch source {
	case "github", "external_docker", "internal_template":
		return nil
	case "internal_docker", "internal_source":
		return fmt.Errorf("deployment source %q requires CLI image pushes or source uploads and cannot be managed by Terraform; use the CLI to manage this deployment", source)
	default:
		return fmt.Errorf("unsupported deployment source %q; Terraform supports github, external_docker, and internal_template", source)
	}
}

type deploymentSourceValidator struct{}

func (deploymentSourceValidator) Description(context.Context) string {
	return "Deployment source must be github, external_docker, or internal_template. Sources internal_docker and internal_source require the CLI."
}

func (v deploymentSourceValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (deploymentSourceValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if err := validateDeploymentSource(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Unsupported Deployment Source", err.Error())
	}
}
