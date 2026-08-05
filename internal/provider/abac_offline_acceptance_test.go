package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccABACResourcesOffline(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the offline Terraform acceptance test")
	}

	backend := newABACContractBackend(t)
	server := httptest.NewServer(backend)
	t.Cleanup(server.Close)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"langsmith": providerserver.NewProtocol6WithError(New("test")()),
		},
		Steps: []resource.TestStep{
			{
				Config: abacAcceptanceConfig(server.URL, "created", []string{"role-a"}),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("langsmith_access_policy.test", "id"),
					resource.TestCheckResourceAttr("langsmith_access_policy.test", "role_ids.#", "1"),
					resource.TestCheckResourceAttrSet("langsmith_tag_key.test", "id"),
					resource.TestCheckResourceAttrSet("langsmith_tag_value.test", "id"),
					resource.TestCheckResourceAttrSet("langsmith_tagging.test", "id"),
					resource.TestCheckResourceAttrSet("langsmith_tag.convenience", "id"),
				),
			},
			{
				Config: abacAcceptanceConfig(server.URL, "updated", []string{"role-b"}),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("langsmith_access_policy.test", "name", "Offline policy updated"),
					resource.TestCheckResourceAttr("langsmith_access_policy.test", "description", "policy updated"),
					resource.TestCheckResourceAttr("langsmith_access_policy.test", "role_ids.0", "role-b"),
					resource.TestCheckResourceAttr("langsmith_tag_key.test", "description", "key updated"),
					resource.TestCheckResourceAttr("langsmith_tag_value.test", "description", "value updated"),
				),
			},
			{
				Config: abacAcceptanceConfig(server.URL, "cleared", []string{}),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("langsmith_access_policy.test", "description"),
					resource.TestCheckResourceAttr("langsmith_access_policy.test", "role_ids.#", "0"),
					resource.TestCheckNoResourceAttr("langsmith_tag_key.test", "description"),
					resource.TestCheckNoResourceAttr("langsmith_tag_value.test", "description"),
					resource.TestCheckNoResourceAttr("langsmith_tag.convenience", "key_description"),
					resource.TestCheckNoResourceAttr("langsmith_tag.convenience", "value_description"),
				),
			},
			{
				Config:             abacAcceptanceConfig(server.URL, "cleared", []string{}),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})

	backend.assertContractCoverage(t)
}

func abacAcceptanceConfig(apiURL, phase string, roleIDs []string) string {
	keyDescription := "\n  description = \"key created\""
	valueDescription := "\n  description = \"value created\""
	convenienceDescriptions := "\n  key_description   = \"convenience key created\"\n  value_description = \"convenience value created\""
	policyName := "Offline policy"
	policyDescription := "\n  description = \"policy created\""
	conditionValue := "production"
	if phase == "updated" {
		keyDescription = "\n  description = \"key updated\""
		valueDescription = "\n  description = \"value updated\""
		convenienceDescriptions = "\n  key_description   = \"convenience key updated\"\n  value_description = \"convenience value updated\""
		policyName = "Offline policy updated"
		policyDescription = "\n  description = \"policy updated\""
		conditionValue = "staging"
	}
	if phase == "cleared" {
		keyDescription, valueDescription, convenienceDescriptions = "", "", ""
		policyName, policyDescription, conditionValue = "Offline policy updated", "", "staging"
	}

	quotedRoles := make([]string, 0, len(roleIDs))
	for _, roleID := range roleIDs {
		quotedRoles = append(quotedRoles, fmt.Sprintf("%q", roleID))
	}

	return fmt.Sprintf(`
provider "langsmith" {
  api_url      = %q
  api_key      = "offline-test-key"
  workspace_id = "offline-workspace"
}

resource "langsmith_tag_key" "test" {
  key = "Environment"%s
}

resource "langsmith_tag_value" "test" {
  tag_key_id = langsmith_tag_key.test.id
  value      = "managed"%s
}

resource "langsmith_tagging" "test" {
  tag_value_id  = langsmith_tag_value.test.id
  resource_type = "project"
  resource_id   = "offline-project"
}

resource "langsmith_tag" "convenience" {
  key   = "Team"
  value = "platform"%s
}

resource "langsmith_access_policy" "test" {
  name   = %q
  effect = "allow"%s
  condition_groups = [{
    permission    = "projects:read"
    resource_type = "project"
    conditions = [{
      attribute_name  = "resource_tag_key"
      attribute_key   = langsmith_tag_key.test.key
      operator        = "equals"
      attribute_value = %q
    }]
  }]
  role_ids = [%s]
}
`, apiURL, keyDescription, valueDescription, convenienceDescriptions, policyName, policyDescription, conditionValue, strings.Join(quotedRoles, ", "))
}

type abacContractBackend struct {
	t             *testing.T
	mu            sync.Mutex
	nextID        int
	keys          map[string]tagKeyAPI
	values        map[string]tagValueAPI
	taggings      map[string]taggingAPI
	policy        accessPolicyAPI
	policyExists  bool
	sawPolicyNull bool
	sawRoleAttach bool
	sawRoleDetach bool
}

func newABACContractBackend(t *testing.T) *abacContractBackend {
	return &abacContractBackend{
		t: t, keys: map[string]tagKeyAPI{}, values: map[string]tagValueAPI{}, taggings: map[string]taggingAPI{},
	}
}

func (b *abacContractBackend) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()

	path := strings.TrimPrefix(req.URL.Path, "/")
	switch {
	case path == tagKeysPath && req.Method == http.MethodPost:
		b.createTagKey(w, req)
	case strings.HasPrefix(path, tagKeysPath+"/"):
		b.handleTagKeyPath(w, req, strings.TrimPrefix(path, tagKeysPath+"/"))
	case path == taggingsPath && req.Method == http.MethodPost:
		b.createTagging(w, req)
	case strings.HasPrefix(path, taggingsPath+"/") && req.Method == http.MethodDelete:
		delete(b.taggings, strings.TrimPrefix(path, taggingsPath+"/"))
		w.WriteHeader(http.StatusNoContent)
	case path == "api/v1/workspaces/current/tags/resource" && req.Method == http.MethodGet:
		b.readTaggings(w, req)
	case path == accessPoliciesPath && req.Method == http.MethodPost:
		b.createPolicy(w, req)
	case strings.HasPrefix(path, accessPoliciesPath+"/"):
		b.handlePolicy(w, req, strings.TrimPrefix(path, accessPoliciesPath+"/"))
	case strings.HasPrefix(path, "api/v1/platform/orgs/current/roles/"):
		b.handlePolicyRole(w, req, path)
	default:
		b.reject(w, req, "unexpected route")
	}
}

func (b *abacContractBackend) createTagKey(w http.ResponseWriter, req *http.Request) {
	var payload tagKeyPayload
	if !b.decode(w, req, &payload) {
		return
	}
	id := b.id("key")
	item := tagKeyAPI{ID: id, Key: payload.Key, Description: payload.Description, CreatedAt: "created", UpdatedAt: "created"}
	b.keys[id] = item
	writeJSON(b.t, w, item)
}

func (b *abacContractBackend) handleTagKeyPath(w http.ResponseWriter, req *http.Request, suffix string) {
	parts := strings.Split(suffix, "/")
	keyID := parts[0]
	if len(parts) >= 2 && parts[1] == "tag-values" {
		b.handleTagValuePath(w, req, keyID, parts[2:])
		return
	}
	item, ok := b.keys[keyID]
	if !ok {
		http.NotFound(w, req)
		return
	}
	switch req.Method {
	case http.MethodGet:
		writeJSON(b.t, w, item)
	case http.MethodPatch:
		var payload tagKeyPayload
		if !b.decode(w, req, &payload) {
			return
		}
		item.Key, item.Description, item.UpdatedAt = payload.Key, payload.Description, "updated"
		b.keys[keyID] = item
		writeJSON(b.t, w, item)
	case http.MethodDelete:
		delete(b.keys, keyID)
		for id, value := range b.values {
			if value.TagKeyID == keyID {
				delete(b.values, id)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		b.reject(w, req, "unexpected tag-key method")
	}
}

func (b *abacContractBackend) handleTagValuePath(w http.ResponseWriter, req *http.Request, keyID string, rest []string) {
	if len(rest) == 0 && req.Method == http.MethodPost {
		var payload tagValuePayload
		if !b.decode(w, req, &payload) {
			return
		}
		id := b.id("value")
		item := tagValueAPI{ID: id, TagKeyID: keyID, Value: payload.Value, Description: payload.Description, CreatedAt: "created", UpdatedAt: "created"}
		b.values[id] = item
		writeJSON(b.t, w, item)
		return
	}
	if len(rest) != 1 {
		b.reject(w, req, "unexpected tag-value route")
		return
	}
	id := rest[0]
	item, ok := b.values[id]
	if !ok || item.TagKeyID != keyID {
		http.NotFound(w, req)
		return
	}
	switch req.Method {
	case http.MethodGet:
		writeJSON(b.t, w, item)
	case http.MethodPatch:
		var payload tagValuePayload
		if !b.decode(w, req, &payload) {
			return
		}
		item.Value, item.Description, item.UpdatedAt = payload.Value, payload.Description, "updated"
		b.values[id] = item
		writeJSON(b.t, w, item)
	case http.MethodDelete:
		delete(b.values, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		b.reject(w, req, "unexpected tag-value method")
	}
}

func (b *abacContractBackend) createTagging(w http.ResponseWriter, req *http.Request) {
	var payload taggingAPI
	if !b.decode(w, req, &payload) {
		return
	}
	payload.ID, payload.CreatedAt = b.id("tagging"), "created"
	b.taggings[payload.ID] = payload
	writeJSON(b.t, w, payload)
}

func (b *abacContractBackend) readTaggings(w http.ResponseWriter, req *http.Request) {
	resourceType, resourceID := req.URL.Query().Get("resource_type"), req.URL.Query().Get("resource_id")
	result := []tagKeyWithTaggingsAPI{}
	for _, value := range b.values {
		matches := []taggingAPI{}
		for _, tagging := range b.taggings {
			if tagging.TagValueID == value.ID && tagging.ResourceType == resourceType && tagging.ResourceID == resourceID {
				matches = append(matches, tagging)
			}
		}
		if len(matches) > 0 {
			result = append(result, tagKeyWithTaggingsAPI{Values: []tagValueWithTaggingsAPI{{tagValueAPI: value, Taggings: matches}}})
		}
	}
	writeJSON(b.t, w, result)
}

func (b *abacContractBackend) createPolicy(w http.ResponseWriter, req *http.Request) {
	var payload accessPolicyPayload
	if !b.decode(w, req, &payload) {
		return
	}
	b.policy = accessPolicyAPI{
		ID: "policy-1", Name: payload.Name, Description: payload.Description, Effect: payload.Effect,
		ConditionGroups: payload.ConditionGroups, RoleIDs: slices.Clone(payload.RoleIDs), CreatedAt: "created", UpdatedAt: "created",
	}
	b.policyExists = true
	writeJSON(b.t, w, accessPolicyCreateResponse{ID: b.policy.ID})
}

func (b *abacContractBackend) handlePolicy(w http.ResponseWriter, req *http.Request, id string) {
	if !b.policyExists || id != b.policy.ID {
		http.NotFound(w, req)
		return
	}
	switch req.Method {
	case http.MethodGet:
		writeJSON(b.t, w, b.policy)
	case http.MethodPatch:
		var fields map[string]json.RawMessage
		if !b.decode(w, req, &fields) {
			return
		}
		if _, exists := fields["role_ids"]; exists {
			b.reject(w, req, "PATCH must not contain role_ids")
			return
		}
		_ = json.Unmarshal(fields["name"], &b.policy.Name)
		_ = json.Unmarshal(fields["effect"], &b.policy.Effect)
		_ = json.Unmarshal(fields["condition_groups"], &b.policy.ConditionGroups)
		if raw, exists := fields["description"]; exists {
			if string(raw) == "null" {
				b.sawPolicyNull = true
				b.policy.Description = ""
			} else {
				_ = json.Unmarshal(raw, &b.policy.Description)
			}
		}
		b.policy.UpdatedAt = "updated"
		writeJSON(b.t, w, b.policy)
	case http.MethodDelete:
		b.policyExists = false
		w.WriteHeader(http.StatusNoContent)
	default:
		b.reject(w, req, "unexpected access-policy method")
	}
}

func (b *abacContractBackend) handlePolicyRole(w http.ResponseWriter, req *http.Request, path string) {
	prefix := "api/v1/platform/orgs/current/roles/"
	roleID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/access-policies")
	if roleID == "" || !strings.HasSuffix(path, "/access-policies") {
		b.reject(w, req, "invalid canonical role route")
		return
	}
	switch req.Method {
	case http.MethodPost:
		var payload accessPolicyAttachmentPayload
		if !b.decode(w, req, &payload) {
			return
		}
		if !slices.Equal(payload.AccessPolicyIDs, []string{b.policy.ID}) {
			b.reject(w, req, "invalid attach payload")
			return
		}
		if !slices.Contains(b.policy.RoleIDs, roleID) {
			b.policy.RoleIDs = append(b.policy.RoleIDs, roleID)
		}
		b.sawRoleAttach = true
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if got := req.URL.Query()["access_policy_ids"]; !slices.Equal(got, []string{b.policy.ID}) || req.ContentLength > 0 {
			b.reject(w, req, "detach requires query IDs and no body")
			return
		}
		b.policy.RoleIDs = slices.DeleteFunc(b.policy.RoleIDs, func(value string) bool { return value == roleID })
		b.sawRoleDetach = true
		w.WriteHeader(http.StatusNoContent)
	default:
		b.reject(w, req, "unexpected role-policy method")
	}
}

func (b *abacContractBackend) decode(w http.ResponseWriter, req *http.Request, value any) bool {
	if err := json.NewDecoder(req.Body).Decode(value); err != nil {
		b.reject(w, req, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func (b *abacContractBackend) reject(w http.ResponseWriter, req *http.Request, reason string) {
	b.t.Errorf("%s %s: %s", req.Method, req.URL.RequestURI(), reason)
	http.Error(w, reason, http.StatusBadRequest)
}

func (b *abacContractBackend) id(prefix string) string {
	b.nextID++
	return fmt.Sprintf("%s-%d", prefix, b.nextID)
}

func (b *abacContractBackend) assertContractCoverage(t *testing.T) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.sawPolicyNull || !b.sawRoleAttach || !b.sawRoleDetach {
		t.Fatalf("contract coverage: description null=%t attach=%t detach=%t", b.sawPolicyNull, b.sawRoleAttach, b.sawRoleDetach)
	}
	if b.policyExists || len(b.keys) != 0 || len(b.values) != 0 || len(b.taggings) != 0 {
		t.Fatalf("Terraform destroy left remote objects: policy=%t keys=%d values=%d taggings=%d", b.policyExists, len(b.keys), len(b.values), len(b.taggings))
	}
}
