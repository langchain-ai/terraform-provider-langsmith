package provider

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestGatewayPolicyModelAccessConfigAPIFromModel(t *testing.T) {
	plan := gatewayPolicyModel{
		Config: &gatewayPolicyConfigModel{
			ModelAccess: &gatewayPolicyModelAccessConfigModel{
				Providers: []gatewayPolicyModelAccessProviderModel{
					{
						Provider:      types.StringValue("openai"),
						Access:        types.StringValue("selected"),
						AllowedModels: []types.String{types.StringValue("gpt-5.4")},
					},
					{
						Provider: types.StringValue("anthropic"),
						Access:   types.StringValue("all"),
					},
				},
			},
		},
	}

	policyType, raw, err := gatewayPolicyConfigAPIFromModel(plan)
	if err != nil {
		t.Fatal(err)
	}
	var got gatewayPolicyModelAccessConfigAPI
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := gatewayPolicyModelAccessConfigAPI{
		Providers: []gatewayPolicyModelAccessProviderAPI{
			{Provider: "openai", Access: "selected", AllowedModels: []string{"gpt-5.4"}},
			{Provider: "anthropic", Access: "all"},
		},
	}
	if policyType != gatewayPolicyTypeModelAccess {
		t.Errorf("policy type = %q, want %q", policyType, gatewayPolicyTypeModelAccess)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("config = %#v, want %#v", got, want)
	}
}

func TestGatewayPolicyModelAccessConfigModelFromAPI(t *testing.T) {
	got, err := gatewayPolicyConfigModelFromAPI(gatewayPolicyTypeModelAccess, json.RawMessage(`{"providers":[{"provider":"openai","access":"selected","allowed_models":["gpt-5.4"]},{"provider":"anthropic","access":"all"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := &gatewayPolicyConfigModel{
		ModelAccess: &gatewayPolicyModelAccessConfigModel{
			Providers: []gatewayPolicyModelAccessProviderModel{
				{
					Provider:      types.StringValue("openai"),
					Access:        types.StringValue("selected"),
					AllowedModels: []types.String{types.StringValue("gpt-5.4")},
				},
				{
					Provider: types.StringValue("anthropic"),
					Access:   types.StringValue("all"),
				},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("config = %#v, want %#v", got, want)
	}
}
