package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/option"
)

func TestAccessPolicyAttachmentLifecycleRequests(t *testing.T) {
	requests := []string{}
	resource := newAccessPolicyAttachmentResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.Method+" "+req.URL.RequestURI())
		switch req.Method {
		case http.MethodPost:
			assertAccessPolicyAttachmentPayload(t, req, "policy-id")
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			writeJSON(t, w, accessPolicyAPI{ID: "policy-id", RoleIDs: []string{"role-a", "role-b"}})
		case http.MethodDelete:
			assertAccessPolicyDetachQuery(t, req, "policy-id")
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
	}))

	if err := resource.attach(context.Background(), "role-b", "policy-id"); err != nil {
		t.Fatalf("attach() error = %v", err)
	}
	attached, err := resource.isAttached(context.Background(), "role-b", "policy-id")
	if err != nil || !attached {
		t.Fatalf("isAttached() = %t, %v", attached, err)
	}
	if err := resource.detach(context.Background(), "role-b", "policy-id"); err != nil {
		t.Fatalf("detach() error = %v", err)
	}

	want := []string{
		"POST /api/v1/platform/orgs/current/roles/role-b/access-policies",
		"GET /api/v1/platform/orgs/current/access-policies/policy-id",
		"DELETE /api/v1/platform/orgs/current/roles/role-b/access-policies?access_policy_ids=policy-id",
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests = %#v, want %#v", requests, want)
	}
}

func TestAccessPolicyAttachmentReadDetectsDrift(t *testing.T) {
	resource := newAccessPolicyAttachmentResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		writeJSON(t, w, accessPolicyAPI{ID: "policy-id", RoleIDs: []string{"role-a"}})
	}))

	attached, err := resource.isAttached(context.Background(), "role-b", "policy-id")
	if err != nil || attached {
		t.Fatalf("isAttached() = %t, %v", attached, err)
	}
}

func TestAccessPolicyAttachmentCreateDoesNotRetry(t *testing.T) {
	requests := 0
	resource := newAccessPolicyAttachmentResourceWithServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		http.Error(w, "temporary failure", http.StatusInternalServerError)
	}))

	if err := resource.attach(context.Background(), "role-b", "policy-id"); err == nil {
		t.Fatal("attach() error = nil")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func assertAccessPolicyAttachmentPayload(t *testing.T, req *http.Request, policyID string) {
	t.Helper()
	var payload accessPolicyAttachmentPayload
	decodeJSON(t, req, &payload)
	if !reflect.DeepEqual(payload.AccessPolicyIDs, []string{policyID}) {
		t.Fatalf("attachment payload = %#v", payload)
	}
}

func assertAccessPolicyDetachQuery(t *testing.T, req *http.Request, policyID string) {
	t.Helper()
	if got := req.URL.Query()["access_policy_ids"]; !reflect.DeepEqual(got, []string{policyID}) {
		t.Fatalf("access_policy_ids query = %#v", got)
	}
	if req.Body != nil && req.ContentLength > 0 {
		t.Fatal("detach request unexpectedly has a body")
	}
}

func newAccessPolicyAttachmentResourceWithServer(t *testing.T, handler http.Handler) *AccessPolicyAttachmentResource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &AccessPolicyAttachmentResource{client: langsmith.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("test-key"))}
}
