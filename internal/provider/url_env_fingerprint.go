package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// urlEnvFingerprint computes a non-reversible fingerprint over the webhook URLs
// resolved from the given url_env environment-variable names. The slice index
// scopes each value to its action/webhook slot, so moving a URL between slots
// also changes the fingerprint.
//
// Webhook URLs are delivered via url_env and deliberately kept out of Terraform
// state — only the env-var name is persisted (see modelFromAlertRuleResponse and
// modelFromRunRuleAPI). That keeps the secret out of state, but it also means
// rotating the value behind the env var produces no plan diff, so the rotated
// URL is never re-sent to LangSmith. Recording this derived fingerprint in state
// lets ModifyPlan detect a changed value and force an update, without storing the
// secret itself.
//
// Returns:
//   - fp: hex-encoded fingerprint (meaningful only when hasURLEnv && resolved).
//   - hasURLEnv: at least one entry names a non-empty env var.
//   - resolved: every named env var resolved to a non-empty value. False when a
//     named env var is unset/empty (e.g. a local plan run without the secret), in
//     which case callers should preserve the prior state value rather than force a
//     spurious diff.
func urlEnvFingerprint(envNames []string) (fp string, hasURLEnv bool, resolved bool) {
	resolved = true
	// NUL-delimit index and value so distinct (index, value) pairs cannot
	// collide through concatenation.
	parts := make([]string, 0, len(envNames))
	for i, name := range envNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		hasURLEnv = true
		v := strings.TrimSpace(os.Getenv(name))
		if v == "" {
			resolved = false
			continue
		}
		parts = append(parts, fmt.Sprintf("%d\x00%s", i, v))
	}
	if !hasURLEnv || !resolved {
		return "", hasURLEnv, resolved
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:]), hasURLEnv, resolved
}
