package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestDeploymentSourceValidation(t *testing.T) {
	ctx := context.Background()
	var schemaResponse resource.SchemaResponse
	(&DeploymentResource{}).Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
	attribute, ok := schemaResponse.Schema.Attributes["source"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("source schema type = %T", schemaResponse.Schema.Attributes["source"])
	}
	for _, source := range []string{"github", "external_docker", "internal_template", "internal_docker", "internal_source", "invalid"} {
		t.Run(source, func(t *testing.T) {
			var response validator.StringResponse
			for _, v := range attribute.Validators {
				v.ValidateString(ctx, validator.StringRequest{Path: path.Root("source"), ConfigValue: types.StringValue(source)}, &response)
			}
			unsupported := source == "internal_docker" || source == "internal_source" || source == "invalid"
			if response.Diagnostics.HasError() != unsupported {
				t.Fatalf("source validation diagnostics = %v", response.Diagnostics)
			}
			if source == "internal_docker" || source == "internal_source" {
				if !strings.Contains(response.Diagnostics[0].Detail(), "CLI") {
					t.Fatalf("missing CLI guidance: %v", response.Diagnostics)
				}
			}
		})
	}
	for _, value := range []types.String{types.StringNull(), types.StringUnknown()} {
		var response validator.StringResponse
		for _, v := range attribute.Validators {
			v.ValidateString(ctx, validator.StringRequest{Path: path.Root("source"), ConfigValue: value}, &response)
		}
		if response.Diagnostics.HasError() {
			t.Fatalf("unresolved source rejected: %v", response.Diagnostics)
		}
	}
}

// Sources that were unknown during planning must also be rejected at apply.
func TestDeploymentCLISourcesRejectWrites(t *testing.T) {
	for _, source := range []string{"internal_docker", "internal_source"} {
		for _, operation := range []string{"create", "update"} {
			t.Run(source+"/"+operation, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					requests.Add(1)
					http.Error(w, "unexpected API request", http.StatusBadRequest)
				}))
				defer server.Close()
				state, plan := testDeploymentModel(), testDeploymentModel()
				state.ID = types.StringValue(testDeploymentID)
				state.Source, plan.Source = types.StringValue(source), types.StringValue(source)
				plan.DisplayName = types.StringValue("Renamed")
				plan.SourceRevisionConfig.ImageURI = types.StringValue("image:v2")
				r := testDeploymentResource(server)
				var result deploymentResourceModel
				var err error
				if operation == "create" {
					result, err = r.create(context.Background(), plan)
				} else {
					result, err = r.update(context.Background(), state, plan)
					if !result.DisplayName.Equal(state.DisplayName) {
						t.Error("rejected update changed state")
					}
				}
				if err == nil || !strings.Contains(err.Error(), "CLI") {
					t.Errorf("expected CLI-directed rejection, got %v", err)
				}
				if requests.Load() != 0 {
					t.Errorf("unsupported source made %d API requests", requests.Load())
				}
			})
		}
	}
}

func TestDeploymentImportRejectsCLISources(t *testing.T) {
	for _, source := range []string{"github", "external_docker", "internal_template", "internal_docker", "internal_source"} {
		t.Run(source, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet || req.URL.Path != "/v2/deployments/"+testDeploymentID {
					t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
					http.NotFound(w, req)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(deploymentAPI{ID: testDeploymentID, Source: source})
			}))
			defer server.Close()
			ctx := context.Background()
			r := testDeploymentResource(server)
			var schemaResponse resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
			response := resource.ImportStateResponse{State: tfsdk.State{
				Schema: schemaResponse.Schema,
				Raw:    tftypes.NewValue(schemaResponse.Schema.Type().TerraformType(ctx), nil),
			}}
			r.ImportState(ctx, resource.ImportStateRequest{ID: testDeploymentID}, &response)
			if source == "internal_docker" || source == "internal_source" {
				if !response.Diagnostics.HasError() || !strings.Contains(response.Diagnostics[0].Detail(), "CLI") {
					t.Fatalf("expected CLI-directed rejection, got %v", response.Diagnostics)
				}
				if !response.State.Raw.IsNull() {
					t.Fatal("rejected import populated state")
				}
				return
			}
			var id types.String
			response.Diagnostics.Append(response.State.GetAttribute(ctx, path.Root("id"), &id)...)
			if response.Diagnostics.HasError() || id.ValueString() != testDeploymentID {
				t.Fatalf("imported ID = %s; diagnostics = %v", id, response.Diagnostics)
			}
		})
	}
}
