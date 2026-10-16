package provider

import (
	"context"
	"net/netip"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func (r *DataPlaneResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config dataPlaneModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if strings.HasPrefix(config.Name.ValueString(), "internal") {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid Data Plane Name", "The name must not start with internal.")
	}
	if !config.BYOVPC.IsUnknown() && !config.VPCCIDR.IsUnknown() {
		switch {
		case config.BYOVPC.IsNull() && config.VPCCIDR.IsNull():
			resp.Diagnostics.AddAttributeError(path.Root("vpc_cidr"), "Missing VPC Configuration", "Configure either vpc_cidr or byovpc.")
		case !config.BYOVPC.IsNull() && !config.VPCCIDR.IsNull():
			resp.Diagnostics.AddAttributeError(path.Root("vpc_cidr"), "Conflicting VPC Configuration", "Omit vpc_cidr when byovpc is configured; the effective CIDR will be computed.")
		case !config.VPCCIDR.IsNull():
			prefix, err := netip.ParsePrefix(config.VPCCIDR.ValueString())
			if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() || prefix != prefix.Masked() || prefix.Bits() < 16 || prefix.Bits() > 18 {
				resp.Diagnostics.AddAttributeError(path.Root("vpc_cidr"), "Invalid VPC CIDR", "Use an RFC1918 IPv4 network with zero host bits and a /16 to /18 prefix.")
			}
		}
	}
	if !config.BYOVPC.IsNull() && !config.BYOVPC.IsUnknown() {
		seen := make(map[string]bool)
		for _, name := range []string{"public_subnet_ids", "private_app_subnet_ids", "private_db_subnet_ids"} {
			subnets, ok := config.BYOVPC.Attributes()[name].(types.Set)
			if !ok || subnets.IsUnknown() {
				continue
			}
			if name == "public_subnet_ids" && config.PublicLoadBalancer.ValueBool() && len(subnets.Elements()) == 0 {
				resp.Diagnostics.AddAttributeError(path.Root("byovpc").AtName(name), "Missing Public Subnets", "Public subnets are required when public_load_balancer is true.")
			}
			for _, element := range subnets.Elements() {
				id, ok := element.(types.String)
				if !ok || id.IsUnknown() || id.IsNull() {
					continue
				}
				if seen[id.ValueString()] {
					resp.Diagnostics.AddAttributeError(path.Root("byovpc"), "Duplicate Subnet", "Subnet IDs must be distinct across all subnet tiers.")
				}
				seen[id.ValueString()] = true
			}
		}
	}
	short, shortOK := config.TTL.Attributes()["short_days"].(types.Int64)
	long, longOK := config.TTL.Attributes()["long_days"].(types.Int64)
	if shortOK && longOK && !short.IsNull() && !long.IsNull() && !short.IsUnknown() && !long.IsUnknown() && short.ValueInt64() > long.ValueInt64() {
		resp.Diagnostics.AddAttributeError(path.Root("ttl"), "Invalid Retention Period", "short_days must be less than or equal to long_days.")
	}
	for _, operation := range []string{"create", "update", "delete"} {
		if _, err := dataPlaneTimeout(config.Timeouts, operation); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("timeouts").AtName(operation), "Invalid Timeout", err.Error())
		}
	}
}
