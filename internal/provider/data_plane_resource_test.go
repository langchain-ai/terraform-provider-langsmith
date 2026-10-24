package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

const testDataPlaneID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

const dataPlaneFixture = `{
 "id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "name":"test-plane", "region":"us-east-1",
 "api_url":"https://plane.example.com", "status":"active", "created_at":"2026-09-23T00:00:00Z", "status_updated_at":"2026-09-23T01:00:00Z",
 "maintenance_window":"sun:03:00-sun:05:00",
 "provisioning_settings":{"cloud":"AWS","role_arn":"arn:aws:iam::123456789012:role/test", "is_byoiam_enabled":false,
   "additional_tags":{},"vpc_cidr":"10.20.0.0/16", "is_public_load_balancer":false,"is_eks_api_privatelink_disabled":false},
 "ttl":{"enabled":true,"short_days":14,"long_days":400},
 "firewall":{"allowed_domains":[".aws.langchain-byoc.com","example.com"],"allow_http":false,"allowed_cidrs":{"10.0.0.0/8":[443,8443]}},
 "fleet_oidc":{"is_enabled":false,"provider":"custom","issuer_url":"https://issuer.example.com","audience":"fleet",
   "subject_claim":"sub","tenant_claim":"tenant","tenant_mappings":{"external":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},"email_claim":"email","groups_claim":"groups"},
 "workspaces":[{"id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","name":"test-plane"}]
}`

func dataPlaneTestAPI(t *testing.T) dataPlaneAPI {
	t.Helper()
	var api dataPlaneAPI
	if err := json.Unmarshal([]byte(dataPlaneFixture), &api); err != nil {
		t.Fatal(err)
	}
	return api
}

func dataPlaneTestModel(t *testing.T) dataPlaneModel {
	t.Helper()
	model, err := dataPlaneModelFromAPI(dataPlaneTestAPI(t), dataPlaneModel{Timeouts: types.ObjectNull(dataPlaneTimeoutTypes)})
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func dataPlaneTestResource(t *testing.T, handler http.HandlerFunc) *DataPlaneResource {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client := langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("offline"), option.WithTenantID("provider-workspace"), option.WithMaxRetries(2))
	return &DataPlaneResource{client: client, pollInterval: time.Millisecond}
}

func writeDataPlaneTestResponse(t *testing.T, w http.ResponseWriter, api dataPlaneAPI) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(api); err != nil {
		t.Error(err)
	}
}

func TestDataPlaneCreateAllFieldsAndWait(t *testing.T) {
	api := dataPlaneTestAPI(t)
	plan := dataPlaneTestModel(t)
	plan.BYOIAMEnabled, plan.PublicLoadBalancer, plan.EKSAPIPrivateLinkDisabled = types.BoolValue(true), types.BoolValue(true), types.BoolValue(true)
	plan.AdditionalTags = types.MapValueMust(types.StringType, map[string]attr.Value{"Team": types.StringValue("platform")})
	plan.MaintenanceWindow = types.StringValue("sat:03:00-sat:05:00")
	var methods []string
	gets := 0
	r := dataPlaneTestResource(t, func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, req.Method)
		if req.Header.Get("X-Tenant-Id") != "provider-workspace" {
			t.Error("provider authentication context was lost")
		}
		switch req.Method {
		case http.MethodPost:
			if req.URL.Path != "/"+dataPlanesPath {
				t.Errorf("create path = %s", req.URL.Path)
			}
			var payload map[string]any
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			want := map[string]any{"name": "test-plane", "region": "us-east-1", "role_arn": "arn:aws:iam::123456789012:role/test", "vpc_cidr": "10.20.0.0/16",
				"byoiam_enabled": true, "public_load_balancer": true, "eks_api_privatelink_disabled": true, "additional_tags": []any{map[string]any{"key": "Team", "value": "platform"}}}
			if !reflect.DeepEqual(payload, want) {
				t.Errorf("create payload = %#v, want %#v", payload, want)
			}
			api.Provisioning.BYOIAMEnabled, api.Provisioning.PublicLoadBalancer, api.Provisioning.EKSAPIPrivateLinkDisabled = true, true, true
			api.Provisioning.AdditionalTags = map[string]string{"Team": "platform"}
			api.Status = "requested"
			w.WriteHeader(http.StatusAccepted)
		case http.MethodGet:
			if req.URL.Path != "/"+dataPlanePath(testDataPlaneID) {
				t.Errorf("get path = %s", req.URL.Path)
			}
			gets++
			api.Status = "active"
			if gets == 1 {
				api.Status = "provisioning"
			}
		case http.MethodPatch:
			var payload map[string]any
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if !reflect.DeepEqual(payload, map[string]any{"maintenance_window": "sat:03:00-sat:05:00"}) {
				t.Errorf("patch = %#v", payload)
			}
			api.MaintenanceWindow, api.Status = "sat:03:00-sat:05:00", "updating"
		default:
			t.Errorf("unexpected method %s", req.Method)
		}
		writeDataPlaneTestResponse(t, w, api)
	})
	result, err := r.create(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.ID.ValueString() != testDataPlaneID || result.Status.ValueString() != "active" || result.MaintenanceWindow != plan.MaintenanceWindow {
		t.Fatalf("unexpected result %#v", result)
	}
	if !reflect.DeepEqual(methods, []string{"POST", "GET", "GET", "PATCH", "GET"}) {
		t.Fatalf("requests = %v", methods)
	}
}

func TestDataPlaneUpdateExplicitClearsAndPartialSettings(t *testing.T) {
	api := dataPlaneTestAPI(t)
	state, plan := dataPlaneTestModel(t), dataPlaneTestModel(t)
	plan.TTL = types.ObjectValueMust(dataPlaneTTLTypes, map[string]attr.Value{"enabled": types.BoolValue(false), "short_days": types.Int64Unknown(), "long_days": types.Int64Null()})
	plan.Firewall = types.ObjectValueMust(dataPlaneFirewallTypes, map[string]attr.Value{"allow_http": types.BoolValue(true), "allowed_domains": types.SetUnknown(types.StringType), "allowed_cidrs": types.MapValueMust(types.SetType{ElemType: types.Int64Type}, map[string]attr.Value{})})
	oidc := plan.FleetOIDC.Attributes()
	oidc["tenant_mappings"] = types.MapValueMust(types.StringType, map[string]attr.Value{})
	plan.FleetOIDC = types.ObjectValueMust(dataPlaneOIDCTypes, oidc)
	patches := 0
	r := dataPlaneTestResource(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPatch {
			patches++
			var payload map[string]any
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			want := map[string]any{"ttl": map[string]any{"enabled": false}, "firewall": map[string]any{"allow_http": true, "allowed_cidrs": map[string]any{}}, "fleet_oidc": map[string]any{"tenant_mappings": map[string]any{}}}
			if !reflect.DeepEqual(payload, want) {
				t.Errorf("patch = %#v, want %#v", payload, want)
			}
			api.TTL["enabled"], api.Firewall["allow_http"] = false, true
			api.Firewall["allowed_cidrs"], api.FleetOIDC["tenant_mappings"] = map[string]any{}, map[string]any{}
		}
		writeDataPlaneTestResponse(t, w, api)
	})
	result, err := r.update(t.Context(), state, plan)
	if err != nil {
		t.Fatal(err)
	}
	if patches != 1 || result.TTL.Attributes()["short_days"] != types.Int64Value(14) {
		t.Fatalf("patches=%d ttl=%v", patches, result.TTL)
	}
}

func TestDataPlaneWaitPreservesLatestState(t *testing.T) {
	for _, tc := range []struct {
		status  string
		timeout bool
	}{{"provisioning_failed", false}, {"revoked", false}, {"inactive", false}, {"provisioning", true}} {
		t.Run(tc.status, func(t *testing.T) {
			api := dataPlaneTestAPI(t)
			api.Status = tc.status
			r := dataPlaneTestResource(t, func(w http.ResponseWriter, req *http.Request) { writeDataPlaneTestResponse(t, w, api) })
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
			defer cancel()
			result, err := r.wait(ctx, dataPlaneTestModel(t), false)
			if err == nil || result.ID.ValueString() != testDataPlaneID || result.Status.ValueString() != tc.status {
				t.Fatalf("status=%s err=%v", result.Status, err)
			}
			if tc.timeout && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected deadline, got %v", err)
			}
		})
	}
}

func TestDataPlaneCreateFailureRetainsState(t *testing.T) {
	for _, testCase := range []string{"provisioning_failed", "missing provisioning", "malformed settings"} {
		t.Run(testCase, func(t *testing.T) { testDataPlaneCreateFailureRetainsState(t, testCase) })
	}
}

func testDataPlaneCreateFailureRetainsState(t *testing.T, testCase string) {
	api := dataPlaneTestAPI(t)
	api.Status = "provisioning_failed"
	switch testCase {
	case "missing provisioning":
		api.Provisioning = nil
	case "malformed settings":
		api.TTL["enabled"] = "invalid boolean"
	}
	r := dataPlaneTestResource(t, func(w http.ResponseWriter, req *http.Request) { writeDataPlaneTestResponse(t, w, api) })
	var schema resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schema)
	plan := tfsdk.Plan{Schema: schema.Schema}
	model := dataPlaneTestModel(t)
	model.ID = types.StringUnknown()
	model.Cloud = types.StringUnknown()
	model.TTL = types.ObjectUnknown(dataPlaneTTLTypes)
	if d := plan.Set(t.Context(), &model); d.HasError() {
		t.Fatal(d)
	}
	resp := resource.CreateResponse{State: tfsdk.State{Schema: schema.Schema}}
	r.Create(t.Context(), resource.CreateRequest{Plan: plan}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected provisioning error")
	}
	if !resp.State.Raw.IsFullyKnown() {
		t.Fatal("failure state contains unknown values")
	}
	if d := resp.State.Get(t.Context(), &model); d.HasError() {
		t.Fatal(d)
	}
	if model.ID.ValueString() != testDataPlaneID || model.Status.ValueString() != "provisioning_failed" {
		t.Fatalf("lost failed resource: %#v", model)
	}
	if model.RoleARN.ValueString() != "arn:aws:iam::123456789012:role/test" {
		t.Fatal("lost the creation inputs needed for recovery")
	}
}

func TestDataPlaneReadRemovesOnlyMissingOrDeleted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		code    int
		status  string
		removed bool
	}{
		{"missing", 404, "", true}, {"deleted", 200, "deleted", true}, {"forbidden", 403, "", false}, {"server error", 500, "", false}, {"revoked", 200, "revoked", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := dataPlaneTestAPI(t)
			api.Status = tc.status
			r := dataPlaneTestResource(t, func(w http.ResponseWriter, req *http.Request) {
				w.WriteHeader(tc.code)
				writeDataPlaneTestResponse(t, w, api)
			})
			var schema resource.SchemaResponse
			r.Schema(t.Context(), resource.SchemaRequest{}, &schema)
			state := tfsdk.State{Schema: schema.Schema}
			model := dataPlaneTestModel(t)
			if d := state.Set(t.Context(), &model); d.HasError() {
				t.Fatal(d)
			}
			resp := resource.ReadResponse{State: state}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &resp)
			if resp.State.Raw.IsNull() != tc.removed {
				t.Fatalf("removed=%v diagnostics=%v", resp.State.Raw.IsNull(), resp.Diagnostics)
			}
			if tc.code >= 400 && tc.code != 404 && !resp.Diagnostics.HasError() {
				t.Fatal("expected API error")
			}
		})
	}
}

func TestDataPlaneDeleteWaitsAndResumes(t *testing.T) {
	for _, initial := range []string{"active", "provisioning_failed", "deprovisioning", "updating", "deleted", "revoked"} {
		t.Run(initial, func(t *testing.T) {
			api := dataPlaneTestAPI(t)
			api.Status = initial
			deletes, gets := 0, 0
			r := dataPlaneTestResource(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == http.MethodDelete {
					deletes++
					api.Status = "deprovisioning"
					w.WriteHeader(http.StatusAccepted)
					return
				}
				gets++
				if api.Status == "deprovisioning" && gets >= 3 {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if api.Status == "updating" && gets > 1 {
					api.Status = "active"
				}
				writeDataPlaneTestResponse(t, w, api)
			})
			err := r.delete(t.Context(), dataPlaneTestModel(t))
			if (err != nil) != (initial == "revoked") {
				t.Fatalf("delete error=%v", err)
			}
			want := 1
			if initial == "deprovisioning" || initial == "deleted" || initial == "revoked" {
				want = 0
			}
			if deletes != want {
				t.Fatalf("DELETE calls=%d want=%d", deletes, want)
			}
		})
	}
}

func TestDataPlaneMutationsNeverRetry(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			r := dataPlaneTestResource(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == method {
					calls++
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				writeDataPlaneTestResponse(t, w, dataPlaneTestAPI(t))
			})
			model := dataPlaneTestModel(t)
			var err error
			switch method {
			case http.MethodPost:
				_, err = r.create(t.Context(), model)
			case http.MethodPatch:
				plan := model
				plan.MaintenanceWindow = types.StringValue("fri:00:00-fri:02:00")
				_, err = r.update(t.Context(), model, plan)
			case http.MethodDelete:
				err = r.delete(t.Context(), model)
			}
			if err == nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestDataPlaneImportRejectsNonUUID(t *testing.T) {
	r := &DataPlaneResource{}
	for _, id := range []string{"", "../elsewhere", "workspace/" + testDataPlaneID, testDataPlaneID} {
		var schema resource.SchemaResponse
		r.Schema(t.Context(), resource.SchemaRequest{}, &schema)
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: schema.Schema, Raw: tftypes.NewValue(schema.Schema.Type().TerraformType(t.Context()), nil)}}
		r.ImportState(t.Context(), resource.ImportStateRequest{ID: id}, &resp)
		if resp.Diagnostics.HasError() != (id != testDataPlaneID) {
			t.Errorf("id=%q diagnostics=%v", id, resp.Diagnostics)
		}
	}
}

func TestDataPlaneValidation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*dataPlaneModel)
		errorText string
	}{
		{"valid", func(m *dataPlaneModel) {}, ""},
		{"missing vpc", func(m *dataPlaneModel) { m.VPCCIDR = types.StringNull() }, "Configure either"},
		{"invalid cidr", func(m *dataPlaneModel) { m.VPCCIDR = types.StringValue("8.8.0.0/16") }, "RFC1918"},
		{"reserved name", func(m *dataPlaneModel) { m.Name = types.StringValue("internal-test") }, "must not start"},
		{"unknown vpc", func(m *dataPlaneModel) { m.VPCCIDR = types.StringUnknown() }, ""},
		{"invalid timeout", func(m *dataPlaneModel) {
			m.Timeouts = types.ObjectValueMust(dataPlaneTimeoutTypes, map[string]attr.Value{"create": types.StringValue("0s"), "update": types.StringNull(), "delete": types.StringNull()})
		}, "positive duration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &DataPlaneResource{}
			var schema resource.SchemaResponse
			r.Schema(t.Context(), resource.SchemaRequest{}, &schema)
			model := dataPlaneTestModel(t)
			tc.mutate(&model)
			state := tfsdk.State{Schema: schema.Schema}
			if d := state.Set(t.Context(), &model); d.HasError() {
				t.Fatal(d)
			}
			var resp resource.ValidateConfigResponse
			r.ValidateConfig(t.Context(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: schema.Schema, Raw: state.Raw}}, &resp)
			if resp.Diagnostics.HasError() != (tc.errorText != "") || (tc.errorText != "" && !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), tc.errorText)) {
				t.Fatalf("diagnostics=%v", resp.Diagnostics)
			}
		})
	}
}
