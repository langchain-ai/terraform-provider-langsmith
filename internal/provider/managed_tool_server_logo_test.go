package provider

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go/option"
)

func writeTestLogo(t *testing.T, size int) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	logo := filepath.Join(t.TempDir(), "logo.png")
	if err := os.WriteFile(logo, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return logo
}

func planLogo(t *testing.T, r *managedToolServerResource, model managedToolServerModel) resource.ModifyPlanResponse {
	t.Helper()
	plan := tfsdk.Plan(managedToolServerTestState(t, r, model))
	response := resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan}, &response)
	return response
}

func logoSHA256(t *testing.T, get func(context.Context, path.Path, any) diag.Diagnostics) types.String {
	t.Helper()
	var sum types.String
	if diags := get(context.Background(), path.Root("logo_sha256"), &sum); diags.HasError() {
		t.Fatal(diags)
	}
	return sum
}

func TestManagedToolServerLogo(t *testing.T) {
	ctx := context.Background()
	logo := writeTestLogo(t, 32)
	data, sum, err := readServerLogo(logo)
	if err != nil {
		t.Fatal(err)
	}
	iconSource := ""
	puts, deletes := 0, 0
	server := managedToolServerAPI{ID: "server-id", Name: "Example-MCP", UpstreamURL: "https://mcp.example.com/mcp", Owner: "workspace", Scope: "workspace", URL: "/v1/managed-tools/servers/server-id/mcp", Enabled: true, Connection: managedToolServerConnectionAPI{Type: "none"}}
	r := &managedToolServerResource{client: newTagTestClientWithOptions(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/v1/managed-tools/servers":
			writeJSON(t, w, server)
		case req.Method == http.MethodPut && req.URL.Path == "/v1/managed-tools/servers/server-id/icon":
			puts++
			file, _, err := req.FormFile("file")
			if err != nil {
				t.Fatalf("logo upload: %v", err)
			}
			got, _ := io.ReadAll(file)
			if !bytes.Equal(got, data) {
				t.Error("uploaded logo differs from the file")
			}
			iconSource = "custom"
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodDelete && req.URL.Path == "/v1/managed-tools/servers/server-id/icon":
			deletes++
			iconSource = ""
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodGet && req.URL.Path == "/v1/managed-tools/servers":
			listed := server
			listed.IconSource = iconSource
			writeJSON(t, w, map[string]any{"items": []managedToolServerAPI{listed}, "next_cursor": nil})
		default:
			t.Errorf("unexpected %s %s", req.Method, req.URL.Path)
		}
	}), option.WithTenantID("workspace"))}

	withLogo := managedToolServerTestModel()
	withLogo.LogoPath = types.StringValue(logo)
	planned := planLogo(t, r, withLogo)
	if planned.Diagnostics.HasError() {
		t.Fatalf("ModifyPlan: %v", planned.Diagnostics)
	}
	if got := logoSHA256(t, planned.Plan.GetAttribute); got.ValueString() != sum {
		t.Fatalf("planned logo_sha256 = %s, want %s", got, sum)
	}

	created := resource.CreateResponse{State: tfsdk.State{Schema: planned.Plan.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: planned.Plan}, &created)
	if created.Diagnostics.HasError() || puts != 1 {
		t.Fatalf("Create: %v, uploads %d", created.Diagnostics, puts)
	}

	// A logo removed outside Terraform clears logo_sha256, so the next plan
	// uploads it again in place.
	iconSource = "server"
	read := resource.ReadResponse{State: created.State}
	r.Read(ctx, resource.ReadRequest{State: created.State}, &read)
	if got := logoSHA256(t, read.State.GetAttribute); !got.IsNull() {
		t.Fatalf("logo_sha256 after removal = %s, want null", got)
	}
	updated := resource.UpdateResponse{State: read.State}
	r.Update(ctx, resource.UpdateRequest{Plan: planned.Plan, State: read.State}, &updated)
	if updated.Diagnostics.HasError() || puts != 2 {
		t.Fatalf("Update: %v, uploads %d", updated.Diagnostics, puts)
	}
	if got := logoSHA256(t, updated.State.GetAttribute); got.ValueString() != sum {
		t.Fatalf("logo_sha256 after upload = %s, want %s", got, sum)
	}

	// Removing logo_path removes the uploaded logo.
	removed := planLogo(t, r, managedToolServerTestModel())
	cleared := resource.UpdateResponse{State: updated.State}
	r.Update(ctx, resource.UpdateRequest{Plan: removed.Plan, State: updated.State}, &cleared)
	if cleared.Diagnostics.HasError() || deletes != 1 || puts != 2 {
		t.Fatalf("Update without logo: %v, deletes %d uploads %d", cleared.Diagnostics, deletes, puts)
	}
}

func TestManagedToolServerLogoValidation(t *testing.T) {
	r := &managedToolServerResource{}
	dir := t.TempDir()
	svg := filepath.Join(dir, "logo.svg")
	if err := os.WriteFile(svg, []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o600); err != nil {
		t.Fatal(err)
	}
	whole, err := os.ReadFile(writeTestLogo(t, 64))
	if err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(dir, "truncated.png")
	if err := os.WriteFile(truncated, whole[:len(whole)-20], 0o600); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(dir, "large.png")
	if err := os.WriteFile(large, make([]byte, maxServerLogoBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, logo := range map[string]string{
		"not an image": svg, "truncated image data": truncated, "too large": large, "missing": filepath.Join(dir, "missing.png"), "too many pixels": writeTestLogo(t, maxServerLogoPixels+1),
	} {
		t.Run(name, func(t *testing.T) {
			model := managedToolServerTestModel()
			model.LogoPath = types.StringValue(logo)
			if planned := planLogo(t, r, model); !planned.Diagnostics.HasError() {
				t.Fatal("ModifyPlan accepted an invalid logo")
			}
		})
	}
}
