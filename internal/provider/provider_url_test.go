package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
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

// TestSelfHostedEndpointDoesNotDoublePrefix exercises the real SDK client the
// provider builds, so a regression in either normalization or the SDK's
// relative path resolution fails here rather than only against a live install.
func TestResolveControlPlaneURL(t *testing.T) {
	t.Run("configured value wins over environment and trims trailing slash", func(t *testing.T) {
		t.Setenv("LANGSMITH_CONTROL_PLANE_URL", "https://environment.example.com")
		got, err := resolveControlPlaneURL("https://configured.example.com/control///")
		if err != nil {
			t.Fatal(err)
		}
		if want := "https://configured.example.com/control"; got != want {
			t.Fatalf("resolveControlPlaneURL() = %q, want %q", got, want)
		}
	})

	t.Run("environment wins over default", func(t *testing.T) {
		t.Setenv("LANGSMITH_CONTROL_PLANE_URL", "https://environment.example.com/")
		got, err := resolveControlPlaneURL("")
		if err != nil {
			t.Fatal(err)
		}
		if want := "https://environment.example.com"; got != want {
			t.Fatalf("resolveControlPlaneURL() = %q, want %q", got, want)
		}
	})

	t.Run("uses default", func(t *testing.T) {
		t.Setenv("LANGSMITH_CONTROL_PLANE_URL", "")
		got, err := resolveControlPlaneURL("")
		if err != nil {
			t.Fatal(err)
		}
		if got != defaultControlPlaneURL {
			t.Fatalf("resolveControlPlaneURL() = %q, want %q", got, defaultControlPlaneURL)
		}
	})
}

func TestResolveControlPlaneURLValidation(t *testing.T) {
	valid := []string{
		"https://example.com",
		"https://example.com/path",
		"http://localhost:8080",
		"http://127.0.0.1",
		"http://127.255.255.255",
		"http://[::1]:8080",
	}
	for _, value := range valid {
		t.Run("valid "+value, func(t *testing.T) {
			if _, err := resolveControlPlaneURL(value); err != nil {
				t.Fatalf("resolveControlPlaneURL(%q) error = %v", value, err)
			}
		})
	}

	invalid := []string{
		"example.com",
		"ftp://example.com",
		"http://example.com",
		"http://192.168.1.1",
		"https://user@example.com",
		"https://example.com?query=value",
		"https://example.com?",
		"https://example.com#fragment",
		"https://example.com#",
	}
	for _, value := range invalid {
		t.Run("invalid "+value, func(t *testing.T) {
			if _, err := resolveControlPlaneURL(value); err == nil {
				t.Fatalf("resolveControlPlaneURL(%q) returned no error", value)
			}
		})
	}
}

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
