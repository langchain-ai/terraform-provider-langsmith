package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const urlEnvFingerprintPrivateKey = "url_env_fingerprint_v1"

type resolvedURLEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// resolveURLEnvFingerprint hashes the ordered environment-variable references
// and their resolved values. The returned bytes contain only a JSON-encoded
// SHA-256 digest, suitable for opaque Terraform private state.
func resolveURLEnvFingerprint(refs []types.String, lookup func(string) (string, bool)) ([]byte, bool, bool, error) {
	resolved := make([]resolvedURLEnv, 0, len(refs))
	deferred := false
	for _, ref := range refs {
		if ref.IsUnknown() {
			deferred = true
			continue
		}
		if ref.IsNull() || ref.ValueString() == "" {
			continue
		}

		name := ref.ValueString()
		value, ok := lookup(name)
		value = strings.TrimSpace(value)
		if !ok || value == "" {
			return nil, true, false, fmt.Errorf("environment variable %s is not set or is empty", name)
		}
		resolved = append(resolved, resolvedURLEnv{Name: name, Value: value})
	}
	if deferred {
		return nil, true, true, nil
	}

	if len(resolved) == 0 {
		return nil, false, false, nil
	}
	encoded, err := json.Marshal(resolved)
	if err != nil {
		return nil, true, false, fmt.Errorf("encode url_env values for fingerprinting: %w", err)
	}
	digest := sha256.Sum256(encoded)
	privateData, err := json.Marshal(hex.EncodeToString(digest[:]))
	if err != nil {
		return nil, true, false, fmt.Errorf("encode url_env fingerprint: %w", err)
	}
	return privateData, true, false, nil
}

// planURLEnvFingerprint decides whether the resource needs an in-place update
// and what fingerprint should be carried into the planned private state.
func planURLEnvFingerprint(stateExists bool, previous, current []byte, hasReferences, deferred bool) (bool, bool, []byte) {
	if deferred {
		return false, false, previous
	}
	if !hasReferences {
		return false, false, nil
	}
	if !stateExists {
		return false, false, current
	}
	updateRequired := !bytes.Equal(previous, current)
	rotationDetected := updateRequired && len(previous) > 0
	return updateRequired, rotationDetected, current
}

func urlEnvReferencesFromNestedList(value types.List) ([]types.String, error) {
	if value.IsUnknown() {
		return []types.String{types.StringUnknown()}, nil
	}
	if value.IsNull() {
		return nil, nil
	}

	refs := make([]types.String, 0, len(value.Elements()))
	for index, element := range value.Elements() {
		if element.IsUnknown() {
			refs = append(refs, types.StringUnknown())
			continue
		}
		if element.IsNull() {
			continue
		}
		object, ok := element.(types.Object)
		if !ok {
			return nil, fmt.Errorf("nested item %d has type %T, want object", index, element)
		}
		urlEnv, ok := object.Attributes()["url_env"]
		if !ok {
			return nil, fmt.Errorf("nested item %d has no url_env attribute", index)
		}
		ref, ok := urlEnv.(types.String)
		if !ok {
			return nil, fmt.Errorf("nested item %d url_env has type %T, want string", index, urlEnv)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func modifyPlanForURLEnv(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse, refs []types.String) {
	current, hasReferences, deferred, err := resolveURLEnvFingerprint(refs, os.LookupEnv)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Resolve Webhook URL Environment Variable", err.Error())
		return
	}
	if deferred {
		return
	}

	previous, diags := req.Private.GetKey(ctx, urlEnvFingerprintPrivateKey)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	updateRequired, rotationDetected, next := planURLEnvFingerprint(!req.State.Raw.IsNull(), previous, current, hasReferences, false)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, urlEnvFingerprintPrivateKey, next)...)
	if resp.Diagnostics.HasError() || !updateRequired {
		return
	}
	if rotationDetected {
		resp.Diagnostics.AddWarning(
			"Webhook URL Rotation Detected",
			"A resolved `url_env` webhook value changed. Terraform will update this rule in place so LangSmith uses the current value. The webhook URL remains omitted from Terraform state.",
		)
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("updated_at"), types.StringUnknown())...)
}
