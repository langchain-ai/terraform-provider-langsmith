package provider

import (
	"context"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	dataPlaneBYOVPCTypes = map[string]attr.Type{
		"vpc_id": types.StringType, "public_subnet_ids": types.SetType{ElemType: types.StringType},
		"private_app_subnet_ids": types.SetType{ElemType: types.StringType}, "private_db_subnet_ids": types.SetType{ElemType: types.StringType},
	}
	dataPlaneTTLTypes      = map[string]attr.Type{"enabled": types.BoolType, "short_days": types.Int64Type, "long_days": types.Int64Type}
	dataPlaneFirewallTypes = map[string]attr.Type{
		"allowed_domains": types.SetType{ElemType: types.StringType}, "allow_http": types.BoolType,
		"allowed_cidrs": types.MapType{ElemType: types.SetType{ElemType: types.Int64Type}},
	}
	dataPlaneOIDCTypes = map[string]attr.Type{
		"is_enabled": types.BoolType, "provider": types.StringType, "issuer_url": types.StringType,
		"audience": types.StringType, "subject_claim": types.StringType, "tenant_claim": types.StringType,
		"tenant_mappings": types.MapType{ElemType: types.StringType}, "email_claim": types.StringType, "groups_claim": types.StringType,
	}
	dataPlaneWorkspaceTypes = map[string]attr.Type{"id": types.StringType, "name": types.StringType}
	dataPlaneTimeoutTypes   = map[string]attr.Type{"create": types.StringType, "update": types.StringType, "delete": types.StringType}
)

func (r *DataPlaneResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an AWS LangSmith data plane in the current organization through the SaaS control-plane API. Requires BYOC enabled and organization administrator access. Creation also creates a workspace; deletion removes linked workspaces and deprovisions infrastructure. Consider `lifecycle { prevent_destroy = true }`. Provisioning inputs require replacement. Create and update wait for `active`; delete waits for removal. Status is refreshed during Terraform reads, not continuously. The AWS role must trust the organization's assigned external ID and have deletion permissions for destroy.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Data plane UUID. Import using this ID."},
			"name": schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 24), stringvalidator.RegexMatches(regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`), "Use lowercase letters, digits and hyphens, starting and ending with a letter or digit.")},
				MarkdownDescription: "Name, unique within the organization. Must not start with `internal`. Changing it replaces the data plane."},
			"region":                       schema.StringAttribute{Required: true, Validators: []validator.String{nonEmptyStringValidator{}}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, MarkdownDescription: "AWS region supported by LangSmith. Changing it requires replacement."},
			"role_arn":                     schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^arn:aws:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]+$`), "Must be an AWS IAM role ARN.")}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, MarkdownDescription: "Customer AWS IAM role used for provisioning. Changing it requires replacement."},
			"cloud":                        schema.StringAttribute{Computed: true, MarkdownDescription: "Cloud provider reported by the API, currently `AWS`."},
			"vpc_cidr":                     schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured()}, MarkdownDescription: "RFC1918 IPv4 network with a /16 to /18 prefix. Required when `byovpc` is omitted; must be omitted from configuration when `byovpc` is set. For BYOVPC, computed as the effective VPC CIDR. Changing it requires replacement."},
			"byovpc":                       dataPlaneBYOVPCSchema(),
			"byoiam_enabled":               dataPlaneCreationBool("Use customer-managed IAM roles created by the LangSmith BYOIAM module. Defaults to false."),
			"public_load_balancer":         dataPlaneCreationBool("Use a public load balancer. Defaults to false. With BYOVPC, requires public subnets."),
			"eks_api_privatelink_disabled": dataPlaneCreationBool("Use a public EKS API endpoint restricted to LangSmith control-plane egress IPs instead of managed PrivateLink. Defaults to false."),
			"additional_tags": schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType,
				Default:       mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})),
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()}, Validators: []validator.Map{mapvalidator.SizeAtMost(40)},
				MarkdownDescription: "Additional AWS resource tags. At most 40; system-owned and AWS/controller-reserved keys are rejected by the API. Changing tags requires replacement."},
			"maintenance_window": schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Two-hour weekly maintenance window in UTC, such as `sun:03:00-sun:05:00`. Omit to adopt the server value."},
			"ttl": schema.SingleNestedAttribute{Optional: true, Computed: true, MarkdownDescription: "Blob retention settings. Omitted fields adopt server values.", Attributes: map[string]schema.Attribute{
				"enabled":    schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "Enable retention."},
				"short_days": schema.Int64Attribute{Optional: true, Computed: true, Validators: []validator.Int64{int64validator.AtLeast(1)}, MarkdownDescription: "Short retention period in days; must not exceed long_days."},
				"long_days":  schema.Int64Attribute{Optional: true, Computed: true, Validators: []validator.Int64{int64validator.AtLeast(1)}, MarkdownDescription: "Long retention period in days."},
			}},
			"firewall": schema.SingleNestedAttribute{Optional: true, Computed: true, MarkdownDescription: "Outbound network firewall settings. Omitted fields adopt server values.", Attributes: map[string]schema.Attribute{
				"allowed_domains": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Validators: []validator.Set{setvalidator.SizeAtMost(500)}, MarkdownDescription: "Allowed domains; must include `.aws.langchain-byoc.com`. A leading dot matches subdomains."},
				"allow_http":      schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "Allow HTTP in addition to HTTPS."},
				"allowed_cidrs":   schema.MapAttribute{Optional: true, Computed: true, ElementType: types.SetType{ElemType: types.Int64Type}, Validators: []validator.Map{mapvalidator.SizeAtMost(500)}, MarkdownDescription: "Map of canonical IPv4 CIDRs (excluding /0) to sets of 1–20 TCP ports (1–65535). An empty map clears the rules."},
			}},
			"fleet_oidc":        dataPlaneOIDCSchema(),
			"api_url":           schema.StringAttribute{Computed: true, MarkdownDescription: "Data plane API URL. The provider continues to manage this resource through the control plane."},
			"status":            schema.StringAttribute{Computed: true, MarkdownDescription: "Last observed lifecycle status: requested, provisioning, provisioning_failed, active, updating, inactive, deprovisioning, deleted, or revoked."},
			"created_at":        schema.StringAttribute{Computed: true, MarkdownDescription: "Creation timestamp."},
			"status_updated_at": schema.StringAttribute{Computed: true, MarkdownDescription: "Timestamp of the last lifecycle status change."},
			"workspaces": schema.SetNestedAttribute{Computed: true, MarkdownDescription: "Associated workspaces, including the workspace automatically created with the data plane.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"id":   schema.StringAttribute{Computed: true, MarkdownDescription: "Workspace UUID."},
				"name": schema.StringAttribute{Computed: true, MarkdownDescription: "Workspace name."},
			}}},
			"timeouts": schema.SingleNestedAttribute{Optional: true, MarkdownDescription: "Maximum duration for each lifecycle operation, including polling. Durations use Go syntax such as `2h` or `30m`.", Attributes: map[string]schema.Attribute{
				"create": schema.StringAttribute{Optional: true, MarkdownDescription: "Create timeout. Defaults to 2h."},
				"update": schema.StringAttribute{Optional: true, MarkdownDescription: "Update timeout. Defaults to 1h."},
				"delete": schema.StringAttribute{Optional: true, MarkdownDescription: "Delete timeout. Defaults to 2h."},
			}},
		},
	}
}

func dataPlaneCreationBool(description string) schema.BoolAttribute {
	return schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()}, MarkdownDescription: description + " Changing it requires replacement."}
}

func dataPlaneBYOVPCSchema() schema.SingleNestedAttribute {
	subnets := func(required bool, description string) schema.SetAttribute {
		attribute := schema.SetAttribute{Required: required, Optional: !required, Computed: !required, ElementType: types.StringType, MarkdownDescription: description,
			Validators: []validator.Set{setvalidator.SizeAtMost(3), setvalidator.ValueStringsAre(stringvalidator.RegexMatches(regexp.MustCompile(`^subnet-([0-9a-f]{8}|[0-9a-f]{17})$`), "Must be an AWS subnet ID."))}}
		if required {
			attribute.Validators = append(attribute.Validators, setvalidator.SizeAtLeast(2))
		} else {
			attribute.Default = setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{}))
		}
		return attribute
	}
	return schema.SingleNestedAttribute{Optional: true, PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplace()},
		MarkdownDescription: "Deploy into a customer-managed VPC instead of creating one. Subnet IDs must be distinct across tiers. Any change requires replacement.", Attributes: map[string]schema.Attribute{
			"vpc_id":                 schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^vpc-([0-9a-f]{8}|[0-9a-f]{17})$`), "Must be an AWS VPC ID.")}, MarkdownDescription: "Customer-managed VPC ID."},
			"public_subnet_ids":      subnets(false, "Up to three public subnet IDs. Required for a public load balancer."),
			"private_app_subnet_ids": subnets(true, "Two or three private application subnet IDs."),
			"private_db_subnet_ids":  subnets(true, "Two or three private database subnet IDs."),
		}}
}

func dataPlaneOIDCSchema() schema.SingleNestedAttribute {
	attributes := map[string]schema.Attribute{
		"is_enabled":      schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "Enable external OIDC authentication for Fleet."},
		"tenant_mappings": schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, MarkdownDescription: "Map external tenant claims to workspace UUIDs belonging to this data plane. An empty map clears the mappings."},
	}
	for name, description := range map[string]string{
		"provider": "OIDC provider identifier.", "issuer_url": "OIDC issuer URL.", "audience": "Expected token audience.",
		"subject_claim": "Subject claim name.", "tenant_claim": "Tenant claim name.", "email_claim": "Email claim name.", "groups_claim": "Groups claim name.",
	} {
		attributes[name] = schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: description}
	}
	return schema.SingleNestedAttribute{Optional: true, Computed: true, Attributes: attributes, MarkdownDescription: "Fleet external OIDC settings. Omitted fields adopt server values. Configure issuer, audience and required claims before enabling."}
}
