package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestNormalizeAPIURL(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"self hosted with api v1":    {"https://langsmith.example.com/api/v1", "https://langsmith.example.com"},
		"self hosted trailing slash": {"https://langsmith.example.com/api/v1/", "https://langsmith.example.com"},
		"self hosted bare origin":    {"https://langsmith.example.com", "https://langsmith.example.com"},
		"saas default":               {"https://api.smith.langchain.com", "https://api.smith.langchain.com"},
		"saas with api v1":           {"https://api.smith.langchain.com/api/v1", "https://api.smith.langchain.com"},
		"surrounding whitespace":     {"  https://langsmith.example.com/api/v1  ", "https://langsmith.example.com"},
		"subpath install":            {"https://example.com/langsmith/api/v1", "https://example.com/langsmith"},
		"empty":                      {"", ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := normalizeAPIURL(tc.in); got != tc.want {
				t.Fatalf("normalizeAPIURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolveAPIURL(t *testing.T) {
	t.Run("configured value wins over environment", func(t *testing.T) {
		t.Setenv("LANGSMITH_ENDPOINT", "https://from-env.example.com/api/v1")
		want := "https://configured.example.com"
		if got := resolveAPIURL("https://configured.example.com/api/v1"); got != want {
			t.Fatalf("resolveAPIURL() = %q, want %q", got, want)
		}
	})

	t.Run("falls back to normalized environment", func(t *testing.T) {
		t.Setenv("LANGSMITH_ENDPOINT", "https://from-env.example.com/api/v1")
		want := "https://from-env.example.com"
		if got := resolveAPIURL(""); got != want {
			t.Fatalf("resolveAPIURL() = %q, want %q", got, want)
		}
	})

	t.Run("empty when neither is set defers to SDK resolution", func(t *testing.T) {
		t.Setenv("LANGSMITH_ENDPOINT", "")
		if got := resolveAPIURL(""); got != "" {
			t.Fatalf("resolveAPIURL() = %q, want empty", got)
		}
	})
}

func TestResolveControlPlaneURL(t *testing.T) {
	t.Run("argument wins over environment and trims trailing slashes", func(t *testing.T) {
		t.Setenv("LANGSMITH_CONTROL_PLANE_URL", "https://environment.example.com")
		got, err := resolveControlPlaneURL("https://configured.example.com/control///", "")
		if err != nil {
			t.Fatal(err)
		}
		if want := "https://configured.example.com/control"; got != want {
			t.Fatalf("resolveControlPlaneURL() = %q, want %q", got, want)
		}
	})

	t.Run("environment wins over the api url", func(t *testing.T) {
		t.Setenv("LANGSMITH_CONTROL_PLANE_URL", "https://environment.example.com/")
		got, err := resolveControlPlaneURL("", "https://langsmith.example.com")
		if err != nil {
			t.Fatal(err)
		}
		if want := "https://environment.example.com"; got != want {
			t.Fatalf("resolveControlPlaneURL() = %q, want %q", got, want)
		}
	})

	t.Run("uses the default when nothing is configured", func(t *testing.T) {
		t.Setenv("LANGSMITH_CONTROL_PLANE_URL", "")
		got, err := resolveControlPlaneURL("", "")
		if err != nil {
			t.Fatal(err)
		}
		if got != defaultControlPlaneURL {
			t.Fatalf("resolveControlPlaneURL() = %q, want %q", got, defaultControlPlaneURL)
		}
	})
}

// A self-hosted install that configures only api_url must not have its API key
// sent to the SaaS control plane, so the control-plane URL is derived from the
// API URL that is actually in effect.
func TestResolveControlPlaneURLDerivesFromAPIURL(t *testing.T) {
	cases := map[string]struct{ apiURL, want string }{
		"saas":                {"https://api.smith.langchain.com", "https://api.host.langchain.com"},
		"saas eu":             {"https://eu.api.smith.langchain.com", "https://eu.api.host.langchain.com"},
		"saas mixed case":     {"https://API.smith.langchain.com", "https://api.host.langchain.com"},
		"self hosted":         {"https://langsmith.example.com", "https://langsmith.example.com/api-host"},
		"self hosted subpath": {"https://example.com/langsmith", "https://example.com/langsmith/api-host"},
		"self hosted http":    {"http://langsmith.internal", "http://langsmith.internal/api-host"},
		"self hosted port":    {"https://langsmith.internal:8443", "https://langsmith.internal:8443/api-host"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("LANGSMITH_CONTROL_PLANE_URL", "")
			got, err := resolveControlPlaneURL("", tc.apiURL)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("resolveControlPlaneURL(\"\", %q) = %q, want %q", tc.apiURL, got, tc.want)
			}
		})
	}
}

func TestResolveControlPlaneURLValidation(t *testing.T) {
	// Plain HTTP is accepted for any host: self-hosted control planes are
	// documented as http(s)://<host>/api-host, and rejecting one here fails
	// provider configuration, which takes every other resource down with it.
	valid := []string{
		"https://example.com",
		"https://example.com/path",
		"http://localhost:8080",
		"http://127.0.0.1",
		"http://[::1]:8080",
		"http://langsmith.internal/api-host",
		"http://192.168.1.1",
	}
	for _, value := range valid {
		t.Run("valid "+value, func(t *testing.T) {
			if _, err := resolveControlPlaneURL(value, ""); err != nil {
				t.Fatalf("resolveControlPlaneURL(%q) error = %v", value, err)
			}
		})
	}

	invalid := []string{
		"example.com",
		"ftp://example.com",
		"https://user@example.com",
		"https://example.com?query=value",
		"https://example.com?",
		"https://example.com#fragment",
		"https://example.com#",
	}
	for _, value := range invalid {
		t.Run("invalid "+value, func(t *testing.T) {
			if _, err := resolveControlPlaneURL(value, ""); err == nil {
				t.Fatalf("resolveControlPlaneURL(%q) returned no error", value)
			}
		})
	}
}

// The diagnostic is attached to control_plane_url whatever the source, so the
// message has to say which value was rejected and where it came from.
func TestResolveControlPlaneURLErrorNamesValueAndSource(t *testing.T) {
	t.Setenv("LANGSMITH_CONTROL_PLANE_URL", "ftp://environment.example.com")
	_, err := resolveControlPlaneURL("", "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "ftp://environment.example.com") || !strings.Contains(err.Error(), "LANGSMITH_CONTROL_PLANE_URL") {
		t.Fatalf("error = %v", err)
	}
}

// TestSelfHostedEndpointDoesNotDoublePrefix exercises the real SDK client the
// provider builds, so a regression in either normalization or the SDK's
// relative path resolution fails here rather than only against a live install.
func TestSelfHostedEndpointDoesNotDoublePrefix(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	// The endpoint shape self-hosted customers are documented to use.
	client := langsmith.NewClient(
		option.WithBaseURL(normalizeAPIURL(server.URL+"/api/v1")),
		option.WithAPIKey("test-key"),
	)

	var out map[string]any
	if err := client.Get(context.Background(), "api/v1/info", nil, &out); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if gotPath != "/api/v1/info" {
		t.Fatalf("request path = %q, want %q", gotPath, "/api/v1/info")
	}
}
