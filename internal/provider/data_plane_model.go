package provider

import (
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

type dataPlaneModel struct {
	ID                        types.String `tfsdk:"id"`
	Name                      types.String `tfsdk:"name"`
	Region                    types.String `tfsdk:"region"`
	RoleARN                   types.String `tfsdk:"role_arn"`
	Cloud                     types.String `tfsdk:"cloud"`
	VPCCIDR                   types.String `tfsdk:"vpc_cidr"`
	BYOVPC                    types.Object `tfsdk:"byovpc"`
	BYOIAMEnabled             types.Bool   `tfsdk:"byoiam_enabled"`
	PublicLoadBalancer        types.Bool   `tfsdk:"public_load_balancer"`
	EKSAPIPrivateLinkDisabled types.Bool   `tfsdk:"eks_api_privatelink_disabled"`
	AdditionalTags            types.Map    `tfsdk:"additional_tags"`
	MaintenanceWindow         types.String `tfsdk:"maintenance_window"`
	TTL                       types.Object `tfsdk:"ttl"`
	Firewall                  types.Object `tfsdk:"firewall"`
	FleetOIDC                 types.Object `tfsdk:"fleet_oidc"`
	APIURL                    types.String `tfsdk:"api_url"`
	Status                    types.String `tfsdk:"status"`
	CreatedAt                 types.String `tfsdk:"created_at"`
	StatusUpdatedAt           types.String `tfsdk:"status_updated_at"`
	Workspaces                types.Set    `tfsdk:"workspaces"`
	Timeouts                  types.Object `tfsdk:"timeouts"`
}

type dataPlaneAPI struct {
	ID                string                    `json:"id"`
	Name              string                    `json:"name"`
	Region            string                    `json:"region"`
	APIURL            string                    `json:"api_url"`
	Status            string                    `json:"status"`
	CreatedAt         string                    `json:"created_at"`
	StatusUpdatedAt   string                    `json:"status_updated_at"`
	MaintenanceWindow string                    `json:"maintenance_window"`
	Provisioning      *dataPlaneProvisioningAPI `json:"provisioning_settings"`
	TTL               map[string]any            `json:"ttl"`
	Firewall          map[string]any            `json:"firewall"`
	FleetOIDC         map[string]any            `json:"fleet_oidc"`
	Workspaces        []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"workspaces"`
}

type dataPlaneProvisioningAPI struct {
	Cloud                     string            `json:"cloud"`
	RoleARN                   string            `json:"role_arn"`
	BYOIAMEnabled             bool              `json:"is_byoiam_enabled"`
	AdditionalTags            map[string]string `json:"additional_tags"`
	VPCCIDR                   string            `json:"vpc_cidr"`
	BYOVPC                    map[string]any    `json:"byovpc"`
	PublicLoadBalancer        bool              `json:"is_public_load_balancer"`
	EKSAPIPrivateLinkDisabled bool              `json:"is_eks_api_privatelink_disabled"`
}

func dataPlaneModelFromAPI(api dataPlaneAPI, previous dataPlaneModel) (dataPlaneModel, error) {
	next := previous
	next.ID, next.Name, next.Region = types.StringValue(api.ID), types.StringValue(api.Name), types.StringValue(api.Region)
	next.APIURL, next.Status = types.StringValue(api.APIURL), types.StringValue(api.Status)
	next.CreatedAt, next.StatusUpdatedAt = types.StringValue(api.CreatedAt), types.StringValue(api.StatusUpdatedAt)
	next.MaintenanceWindow = types.StringValue(api.MaintenanceWindow)
	for _, field := range []struct {
		target *types.Object
		fields map[string]attr.Type
		values map[string]any
	}{
		{&next.TTL, dataPlaneTTLTypes, api.TTL}, {&next.Firewall, dataPlaneFirewallTypes, api.Firewall}, {&next.FleetOIDC, dataPlaneOIDCTypes, api.FleetOIDC},
	} {
		value, err := dataPlaneObjectFromAPI(field.fields, field.values)
		if err != nil {
			return previous, err
		}
		*field.target = value
	}
	workspaces := make([]attr.Value, 0, len(api.Workspaces))
	for _, workspace := range api.Workspaces {
		workspaces = append(workspaces, types.ObjectValueMust(dataPlaneWorkspaceTypes, map[string]attr.Value{"id": types.StringValue(workspace.ID), "name": types.StringValue(workspace.Name)}))
	}
	next.Workspaces = types.SetValueMust(types.ObjectType{AttrTypes: dataPlaneWorkspaceTypes}, workspaces)
	if p := api.Provisioning; p != nil {
		next.Cloud, next.RoleARN, next.VPCCIDR = types.StringValue(p.Cloud), types.StringValue(p.RoleARN), types.StringValue(p.VPCCIDR)
		next.BYOIAMEnabled, next.PublicLoadBalancer = types.BoolValue(p.BYOIAMEnabled), types.BoolValue(p.PublicLoadBalancer)
		next.EKSAPIPrivateLinkDisabled = types.BoolValue(p.EKSAPIPrivateLinkDisabled)
		tags := make(map[string]attr.Value, len(p.AdditionalTags))
		for key, value := range p.AdditionalTags {
			tags[key] = types.StringValue(value)
		}
		next.AdditionalTags = types.MapValueMust(types.StringType, tags)
		var err error
		next.BYOVPC, err = dataPlaneObjectFromAPI(dataPlaneBYOVPCTypes, p.BYOVPC)
		if err != nil {
			return previous, err
		}
	} else {
		// Failed reservations may not have saved provisioning settings. Retain
		// known creation inputs without leaving unknown computed values in state.
		if next.Cloud.IsUnknown() {
			next.Cloud = types.StringNull()
		}
		if next.VPCCIDR.IsUnknown() {
			next.VPCCIDR = types.StringNull()
		}
	}
	return next, nil
}

// Decode only the public fields in our schema. Additional server fields are
// ignored; unexpected types are errors rather than silently erasing state.
func dataPlaneObjectFromAPI(fields map[string]attr.Type, values map[string]any) (types.Object, error) {
	if values == nil {
		return types.ObjectNull(fields), nil
	}
	attributes := make(map[string]attr.Value, len(fields))
	for name, typ := range fields {
		value, err := dataPlaneValueFromAPI(typ, values[name])
		if err != nil {
			return types.ObjectNull(fields), fmt.Errorf("decoding data plane field %s: %w", name, err)
		}
		attributes[name] = value
	}
	return types.ObjectValueMust(fields, attributes), nil
}

func dataPlaneValueFromAPI(typ attr.Type, raw any) (attr.Value, error) {
	switch typ := typ.(type) {
	case basetypes.StringType:
		if raw == nil {
			return types.StringNull(), nil
		}
		if value, ok := raw.(string); ok {
			return types.StringValue(value), nil
		}
	case basetypes.BoolType:
		if raw == nil {
			return types.BoolNull(), nil
		}
		if value, ok := raw.(bool); ok {
			return types.BoolValue(value), nil
		}
	case basetypes.Int64Type:
		if raw == nil {
			return types.Int64Null(), nil
		}
		if value, ok := raw.(float64); ok && value == float64(int64(value)) {
			return types.Int64Value(int64(value)), nil
		}
	case types.SetType:
		items, ok := raw.([]any)
		if !ok && raw != nil {
			break
		}
		values := make([]attr.Value, 0, len(items))
		for _, item := range items {
			value, err := dataPlaneValueFromAPI(typ.ElemType, item)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return types.SetValueMust(typ.ElemType, values), nil
	case types.MapType:
		items, ok := raw.(map[string]any)
		if !ok && raw != nil {
			break
		}
		values := make(map[string]attr.Value, len(items))
		for key, item := range items {
			value, err := dataPlaneValueFromAPI(typ.ElemType, item)
			if err != nil {
				return nil, err
			}
			values[key] = value
		}
		return types.MapValueMust(typ.ElemType, values), nil
	}
	return nil, fmt.Errorf("unexpected %T for %s", raw, typ)
}

// Omitted/unknown values are not updates; known empty collections explicitly clear.
func dataPlaneValuePayload(value attr.Value) (any, bool) {
	if value == nil || value.IsNull() || value.IsUnknown() {
		return nil, false
	}
	switch value := value.(type) {
	case types.String:
		return value.ValueString(), true
	case types.Bool:
		return value.ValueBool(), true
	case types.Int64:
		return value.ValueInt64(), true
	case types.Set:
		items := make([]any, 0, len(value.Elements()))
		for _, element := range value.Elements() {
			item, known := dataPlaneValuePayload(element)
			if !known {
				return nil, false
			}
			items = append(items, item)
		}
		return items, true
	case types.Map:
		items := make(map[string]any, len(value.Elements()))
		for key, element := range value.Elements() {
			item, known := dataPlaneValuePayload(element)
			if !known {
				return nil, false
			}
			items[key] = item
		}
		return items, true
	case types.Object:
		items := make(map[string]any)
		for key, element := range value.Attributes() {
			if item, known := dataPlaneValuePayload(element); known {
				items[key] = item
			}
		}
		return items, len(items) > 0
	}
	return nil, false
}

func dataPlaneCreatePayload(plan dataPlaneModel) map[string]any {
	payload := map[string]any{"name": plan.Name.ValueString(), "region": plan.Region.ValueString(), "role_arn": plan.RoleARN.ValueString(),
		"byoiam_enabled": plan.BYOIAMEnabled.ValueBool(), "public_load_balancer": plan.PublicLoadBalancer.ValueBool(), "eks_api_privatelink_disabled": plan.EKSAPIPrivateLinkDisabled.ValueBool()}
	if byovpc, ok := dataPlaneValuePayload(plan.BYOVPC); ok {
		payload["byovpc"] = byovpc
	} else {
		payload["vpc_cidr"] = plan.VPCCIDR.ValueString()
	}
	keys := make([]string, 0, len(plan.AdditionalTags.Elements()))
	for key := range plan.AdditionalTags.Elements() {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	tags := make([]map[string]string, 0, len(keys))
	tagValues := stringMap(plan.AdditionalTags)
	for _, key := range keys {
		tags = append(tags, map[string]string{"key": key, "value": tagValues[key]})
	}
	payload["additional_tags"] = tags
	return payload
}

func dataPlaneUpdatePayload(previous, plan dataPlaneModel) map[string]any {
	payload := make(map[string]any)
	if !plan.MaintenanceWindow.Equal(previous.MaintenanceWindow) {
		if value, ok := dataPlaneValuePayload(plan.MaintenanceWindow); ok {
			payload["maintenance_window"] = value
		}
	}
	for _, field := range []struct {
		name          string
		before, after types.Object
	}{
		{"ttl", previous.TTL, plan.TTL}, {"firewall", previous.Firewall, plan.Firewall}, {"fleet_oidc", previous.FleetOIDC, plan.FleetOIDC},
	} {
		changes := make(map[string]any)
		for name, value := range field.after.Attributes() {
			if old := field.before.Attributes()[name]; old != nil && value.Equal(old) {
				continue
			}
			if raw, ok := dataPlaneValuePayload(value); ok {
				changes[name] = raw
			}
		}
		if len(changes) > 0 {
			payload[field.name] = changes
		}
	}
	return payload
}
