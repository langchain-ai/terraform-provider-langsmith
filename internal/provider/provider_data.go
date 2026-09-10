package provider

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/langchain-ai/langsmith-go"
)

type providerData struct {
	LangSmithClient    *langsmith.Client
	ControlPlaneClient *langsmith.Client
}

func configureProviderData(data any, diagnostics *diag.Diagnostics) *providerData {
	if data == nil {
		return nil
	}
	value, ok := data.(*providerData)
	if !ok {
		diagnostics.AddError("Unexpected Provider Configure Type", fmt.Sprintf("Expected *providerData, got %T", data))
		return nil
	}
	return value
}
