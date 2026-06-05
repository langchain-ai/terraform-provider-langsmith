package provider

import (
	"testing"
	"time"
)

func TestOrgDataSourceModelFromAPI(t *testing.T) {
	createdAt := time.Date(2026, 5, 21, 12, 30, 0, 0, time.UTC)
	model, err := orgDataSourceModelFromAPI(organizationAPI{
		ID:          "org-id",
		DisplayName: "LangChain",
		Handle:      "langchain",
		CreatedAt:   createdAt,
	})
	if err != nil {
		t.Fatalf("orgDataSourceModelFromAPI returned error: %v", err)
	}

	if model.ID.ValueString() != "org-id" {
		t.Fatalf("ID = %q, want org-id", model.ID.ValueString())
	}
	if model.DisplayName.ValueString() != "LangChain" {
		t.Fatalf("DisplayName = %q, want LangChain", model.DisplayName.ValueString())
	}
	if model.Handle.ValueString() != "langchain" {
		t.Fatalf("Handle = %q, want langchain", model.Handle.ValueString())
	}
	if got, want := model.CreatedAt.ValueString(), "2026-05-21T12:30:00Z"; got != want {
		t.Fatalf("CreatedAt = %q, want %q", got, want)
	}
}

func TestOrgDataSourceModelFromAPIRequiresID(t *testing.T) {
	_, err := orgDataSourceModelFromAPI(organizationAPI{})
	if err == nil {
		t.Fatalf("orgDataSourceModelFromAPI returned nil error for missing ID")
	}
}
