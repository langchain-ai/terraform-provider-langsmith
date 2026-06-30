package provider

import "testing"

func TestURLEnvFingerprintStableAndSensitiveToValue(t *testing.T) {
	t.Setenv("TEST_WEBHOOK_A", "https://example.com/old")

	fp1, hasURLEnv, resolved := urlEnvFingerprint([]string{"TEST_WEBHOOK_A"})
	if !hasURLEnv || !resolved {
		t.Fatalf("hasURLEnv=%v resolved=%v, want both true", hasURLEnv, resolved)
	}
	if fp1 == "" {
		t.Fatalf("fingerprint is empty")
	}

	// Same value -> same fingerprint (no spurious diff).
	fp2, _, _ := urlEnvFingerprint([]string{"TEST_WEBHOOK_A"})
	if fp1 != fp2 {
		t.Fatalf("fingerprint not stable: %q != %q", fp1, fp2)
	}

	// Rotated value -> different fingerprint (rotation is detected).
	t.Setenv("TEST_WEBHOOK_A", "https://example.com/new")
	fp3, _, _ := urlEnvFingerprint([]string{"TEST_WEBHOOK_A"})
	if fp3 == fp1 {
		t.Fatalf("fingerprint did not change after rotation: %q", fp3)
	}
}

func TestURLEnvFingerprintNoURLEnv(t *testing.T) {
	fp, hasURLEnv, resolved := urlEnvFingerprint([]string{"", ""})
	if hasURLEnv {
		t.Fatalf("hasURLEnv = true, want false")
	}
	if !resolved {
		t.Fatalf("resolved = false, want true when there is nothing to resolve")
	}
	if fp != "" {
		t.Fatalf("fingerprint = %q, want empty", fp)
	}
}

func TestURLEnvFingerprintUnresolvedWhenEnvMissing(t *testing.T) {
	// Env var named but not set -> resolved=false so callers keep prior state.
	fp, hasURLEnv, resolved := urlEnvFingerprint([]string{"TEST_WEBHOOK_DEFINITELY_UNSET"})
	if !hasURLEnv {
		t.Fatalf("hasURLEnv = false, want true")
	}
	if resolved {
		t.Fatalf("resolved = true, want false for an unset env var")
	}
	if fp != "" {
		t.Fatalf("fingerprint = %q, want empty when unresolved", fp)
	}
}

func TestURLEnvFingerprintPositionScoped(t *testing.T) {
	t.Setenv("TEST_WEBHOOK_A", "https://example.com/a")
	t.Setenv("TEST_WEBHOOK_B", "https://example.com/b")

	// Same set of resolved URLs in different slots must produce different
	// fingerprints, so reordering actions is treated as a change.
	fpAB, _, _ := urlEnvFingerprint([]string{"TEST_WEBHOOK_A", "TEST_WEBHOOK_B"})
	fpBA, _, _ := urlEnvFingerprint([]string{"TEST_WEBHOOK_B", "TEST_WEBHOOK_A"})
	if fpAB == fpBA {
		t.Fatalf("fingerprint not position-scoped: %q == %q", fpAB, fpBA)
	}

	// A gap (empty slot) before a url_env still resolves and is stable.
	fpGap, hasURLEnv, resolved := urlEnvFingerprint([]string{"", "TEST_WEBHOOK_A"})
	if !hasURLEnv || !resolved || fpGap == "" {
		t.Fatalf("gap case: hasURLEnv=%v resolved=%v fp=%q", hasURLEnv, resolved, fpGap)
	}
}
