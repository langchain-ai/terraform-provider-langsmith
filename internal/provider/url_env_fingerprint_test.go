package provider

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestResolveURLEnvFingerprintStableAndSecretFree(t *testing.T) {
	values := map[string]string{
		"PRIMARY_WEBHOOK_URL": "  https://example.com/first  ",
		"BACKUP_WEBHOOK_URL":  "https://example.com/second",
	}
	lookup := func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
	refs := []types.String{
		types.StringValue("PRIMARY_WEBHOOK_URL"),
		types.StringValue("BACKUP_WEBHOOK_URL"),
	}

	first, hasReferences, deferred, err := resolveURLEnvFingerprint(refs, lookup)
	if err != nil {
		t.Fatalf("resolveURLEnvFingerprint returned error: %v", err)
	}
	values["PRIMARY_WEBHOOK_URL"] = "https://example.com/first"
	second, _, _, err := resolveURLEnvFingerprint(refs, lookup)
	if err != nil {
		t.Fatalf("resolveURLEnvFingerprint returned error: %v", err)
	}
	if !hasReferences || deferred {
		t.Fatalf("hasReferences=%v deferred=%v, want true/false", hasReferences, deferred)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("fingerprints differ for unchanged inputs: %q != %q", first, second)
	}
	if bytes.Contains(first, []byte("https://")) {
		t.Fatalf("private fingerprint contains a resolved URL: %s", first)
	}
	var digest string
	if err := json.Unmarshal(first, &digest); err != nil {
		t.Fatalf("private fingerprint is not a JSON-encoded digest: %v", err)
	}
	if len(digest) != 64 {
		t.Fatalf("digest length = %d, want 64 hexadecimal characters", len(digest))
	}
}

func TestResolveURLEnvFingerprintDetectsRotation(t *testing.T) {
	value := "https://example.com/original"
	lookup := func(string) (string, bool) { return value, true }
	refs := []types.String{types.StringValue("WEBHOOK_URL")}

	previous, _, _, err := resolveURLEnvFingerprint(refs, lookup)
	if err != nil {
		t.Fatal(err)
	}
	value = "https://example.com/rotated"
	current, hasReferences, deferred, err := resolveURLEnvFingerprint(refs, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(previous, current) {
		t.Fatal("fingerprint did not change after URL rotation")
	}
	updateRequired, rotationDetected, next := planURLEnvFingerprint(true, previous, current, hasReferences, deferred)
	if !updateRequired || !rotationDetected || !bytes.Equal(next, current) {
		t.Fatalf("updateRequired=%v rotationDetected=%v next=%q, want true/true and current fingerprint", updateRequired, rotationDetected, next)
	}
}

func TestResolveURLEnvFingerprintAvoidsConcatenationCollisions(t *testing.T) {
	lookup := func(name string) (string, bool) {
		return map[string]string{"a": "bc", "ab": "c"}[name], true
	}
	first, _, _, err := resolveURLEnvFingerprint([]types.String{
		types.StringValue("a"),
		types.StringValue("ab"),
	}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	second, _, _, err := resolveURLEnvFingerprint([]types.String{
		types.StringValue("ab"),
		types.StringValue("a"),
	}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("ordered environment name/value pairs produced a concatenation collision")
	}
}

func TestResolveURLEnvFingerprintRejectsMissingAndEmptyValues(t *testing.T) {
	tests := map[string]func(string) (string, bool){
		"missing": func(string) (string, bool) { return "", false },
		"empty":   func(string) (string, bool) { return " \t\n ", true },
	}
	for name, lookup := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := resolveURLEnvFingerprint(
				[]types.String{types.StringValue("WEBHOOK_URL")}, lookup,
			)
			if err == nil || !strings.Contains(err.Error(), "WEBHOOK_URL") {
				t.Fatalf("error = %v, want an error naming WEBHOOK_URL", err)
			}
		})
	}
}

func TestResolveURLEnvFingerprintHandlesNullEmptyAndUnknownReferences(t *testing.T) {
	lookupCalled := false
	lookup := func(string) (string, bool) {
		lookupCalled = true
		return "https://example.com", true
	}

	fingerprint, hasReferences, deferred, err := resolveURLEnvFingerprint([]types.String{
		types.StringNull(),
		types.StringValue(""),
	}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint != nil || hasReferences || deferred || lookupCalled {
		t.Fatalf("fingerprint=%q hasReferences=%v deferred=%v lookupCalled=%v", fingerprint, hasReferences, deferred, lookupCalled)
	}

	_, hasReferences, deferred, err = resolveURLEnvFingerprint(
		[]types.String{types.StringUnknown()}, lookup,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !hasReferences || !deferred || lookupCalled {
		t.Fatalf("hasReferences=%v deferred=%v lookupCalled=%v, want true/true/false", hasReferences, deferred, lookupCalled)
	}
}

func TestPlanURLEnvFingerprintLifecycle(t *testing.T) {
	current := []byte(`"current"`)
	tests := []struct {
		name            string
		stateExists     bool
		previous        []byte
		hasReferences   bool
		deferred        bool
		wantUpdate      bool
		wantRotation    bool
		wantFingerprint []byte
	}{
		{name: "initial create", stateExists: false, hasReferences: true, wantFingerprint: current},
		{name: "provider upgrade initialization", stateExists: true, hasReferences: true, wantUpdate: true, wantFingerprint: current},
		{name: "unchanged existing state", stateExists: true, previous: current, hasReferences: true, wantFingerprint: current},
		{name: "remove all references", stateExists: true, previous: current},
		{name: "unknown reference defers", stateExists: true, previous: current, hasReferences: true, deferred: true, wantFingerprint: current},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			update, rotation, next := planURLEnvFingerprint(test.stateExists, test.previous, current, test.hasReferences, test.deferred)
			if update != test.wantUpdate || rotation != test.wantRotation || !bytes.Equal(next, test.wantFingerprint) {
				t.Fatalf("update=%v rotation=%v next=%q, want %v/%v/%q", update, rotation, next, test.wantUpdate, test.wantRotation, test.wantFingerprint)
			}
		})
	}
}
