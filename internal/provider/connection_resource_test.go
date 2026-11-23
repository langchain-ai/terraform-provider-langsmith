package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func newConnectionResourceWithServer(t *testing.T, kind string, handler http.Handler) *ConnectionResource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &ConnectionResource{
		client: langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("test-key")),
		kind:   kind,
	}
}

func TestConnectionResourceMetadata(t *testing.T) {
	for want, res := range map[string]resource.Resource{"langsmith_tunnel": NewTunnelResource(), "langsmith_relay": NewRelayResource()} {
		var resp resource.MetadataResponse
		res.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "langsmith"}, &resp)
		if resp.TypeName != want {
			t.Fatalf("TypeName = %q, want %s", resp.TypeName, want)
		}
	}
}

func TestConnectionResourceReadRejectsOtherKind(t *testing.T) {
	res := newConnectionResourceWithServer(t, connectionKindRelay, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, connectionAPI{ID: "conn-id", Name: "vpc", Kind: "tunnel", Enabled: true})
	}))
	if _, err := res.readConnection(context.Background(), "conn-id"); err == nil || isLangSmithNotFound(err) {
		t.Fatalf("readConnection err = %v, want kind mismatch error", err)
	}
}

func TestConnectionResourceCreateRelay(t *testing.T) {
	requests := []string{}
	res := newConnectionResourceWithServer(t, connectionKindRelay, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/connections" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		want := map[string]any{"name": "ci-tailnet", "kind": "relay"}
		if !reflect.DeepEqual(payload, want) {
			t.Fatalf("payload = %#v, want %#v", payload, want)
		}
		writeJSON(t, w, connectionAPI{ID: "conn-id", Name: "ci-tailnet", Kind: "relay", Status: "pending", Enabled: true, CreatedAt: "2026-11-23T00:00:00Z"})
	}))

	model, err := res.createConnection(context.Background(), connectionResourceModel{
		Name:        types.StringValue("ci-tailnet"),
		Description: types.StringNull(),
		Enabled:     types.BoolValue(true),
	})
	if err != nil {
		t.Fatalf("createConnection: %v", err)
	}
	if model.ID.ValueString() != "conn-id" || model.Status.ValueString() != "pending" {
		t.Fatalf("model = %#v", model)
	}
	if !model.Description.IsNull() {
		t.Fatalf("Description = %v, want null", model.Description)
	}
	if !reflect.DeepEqual(requests, []string{"POST /v1/connections"}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestConnectionResourceCreateDisabledPatchesEnabled(t *testing.T) {
	requests := []string{}
	res := newConnectionResourceWithServer(t, connectionKindTunnel, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/connections":
			writeJSON(t, w, connectionAPI{ID: "conn-id", Name: "n", Kind: "tunnel", Enabled: true})
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/connections/conn-id":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !reflect.DeepEqual(payload, map[string]any{"enabled": false}) {
				t.Fatalf("patch payload = %#v", payload)
			}
			writeJSON(t, w, connectionAPI{ID: "conn-id", Name: "n", Kind: "tunnel", Enabled: false})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))

	model, err := res.createConnection(context.Background(), connectionResourceModel{
		Name:    types.StringValue("n"),
		Enabled: types.BoolValue(false),
	})
	if err != nil {
		t.Fatalf("createConnection: %v", err)
	}
	if model.Enabled.ValueBool() {
		t.Fatalf("Enabled = true, want false")
	}
	if !reflect.DeepEqual(requests, []string{"POST /v1/connections", "PATCH /v1/connections/conn-id"}) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestConnectionResourceUpdateSendsMutableFields(t *testing.T) {
	res := newConnectionResourceWithServer(t, connectionKindRelay, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v1/connections/conn-id" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		want := map[string]any{"name": "renamed", "description": "", "enabled": true}
		if !reflect.DeepEqual(payload, want) {
			t.Fatalf("payload = %#v, want %#v", payload, want)
		}
		empty := ""
		writeJSON(t, w, connectionAPI{ID: "conn-id", Name: "renamed", Description: &empty, Kind: "relay", Enabled: true})
	}))

	model, err := res.updateConnection(context.Background(), "conn-id", connectionResourceModel{
		Name:        types.StringValue("renamed"),
		Description: types.StringNull(),
		Enabled:     types.BoolValue(true),
	})
	if err != nil {
		t.Fatalf("updateConnection: %v", err)
	}
	if model.Name.ValueString() != "renamed" || !model.Description.IsNull() {
		t.Fatalf("model = %#v", model)
	}
}

func TestConnectionResourceReadNotFoundAndDeleteIgnoresMissing(t *testing.T) {
	res := newConnectionResourceWithServer(t, connectionKindTunnel, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	if _, err := res.readConnection(context.Background(), "missing"); !isLangSmithNotFound(err) {
		t.Fatalf("read err = %v, want LangSmith 404", err)
	}
	if err := res.deleteConnection(context.Background(), "missing"); err != nil {
		t.Fatalf("delete err = %v, want nil", err)
	}
}

func TestConnectionResourceCreateKeepsIDWhenDisableFails(t *testing.T) {
	res := newConnectionResourceWithServer(t, connectionKindRelay, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			writeJSON(t, w, connectionAPI{ID: "conn-id", Name: "n", Kind: "relay", Enabled: true})
		case http.MethodPatch:
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))

	model, err := res.createConnection(context.Background(), connectionResourceModel{
		Name:    types.StringValue("n"),
		Enabled: types.BoolValue(false),
	})
	if err == nil {
		t.Fatalf("createConnection err = nil, want PATCH failure")
	}
	if model.ID.ValueString() != "conn-id" || !model.Enabled.ValueBool() {
		t.Fatalf("model = %#v, want created connection state", model)
	}
}

func TestConnectionResourceReadRejectsPersonalConnection(t *testing.T) {
	res := newConnectionResourceWithServer(t, connectionKindTunnel, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, connectionAPI{ID: "conn-id", Name: "mine", Kind: "tunnel", Enabled: true, Personal: true})
	}))
	if _, err := res.readConnection(context.Background(), "conn-id"); err == nil || isLangSmithNotFound(err) {
		t.Fatalf("readConnection err = %v, want personal-connection error", err)
	}
}
