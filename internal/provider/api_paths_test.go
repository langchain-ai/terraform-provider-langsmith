package provider

import (
	"strings"
	"testing"
)

// TestAPIPathsUsePublicPrefix pins every request path the provider builds to
// the shape declared in the public OpenAPI spec, which prefixes all routes
// with api/. smith-go registers the platform and sandbox routes without that
// prefix, and the self-hosted gateway only exposes the prefixed form — so a
// path that drops api/ reaches SaaS but falls through to the SPA on a
// self-hosted install, returning HTML instead of JSON.
func TestAPIPathsUsePublicPrefix(t *testing.T) {
	paths := []string{
		alertRuleCollectionPath("session-id"),
		alertRuleResourcePath("session-id", "alert-id"),
		evaluatorCollectionPath(),
		evaluatorResourcePath("evaluator-id"),
		sandboxRegistriesPath,
		sandboxRegistryResourcePath("registry-name"),
		gatewayPoliciesPath,
		gatewayPolicyResourcePath("policy-id"),
	}

	for _, p := range paths {
		if !strings.HasPrefix(p, "api/") {
			t.Errorf("path %q must start with api/ to match the public OpenAPI spec", p)
		}
		if strings.HasPrefix(p, "/") {
			t.Errorf("path %q must be relative so it resolves against the client base URL", p)
		}
	}
}
