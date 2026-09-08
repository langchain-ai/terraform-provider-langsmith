package provider

import (
	"fmt"

	"github.com/langchain-ai/langsmith-go"
)

type providerData struct {
	LangSmithClient    *langsmith.Client
	ControlPlaneClient *langsmith.Client
}

type configureDiagnostics interface {
	AddError(summary string, detail string)
}

func configureLangSmithClient(data any, diagnostics configureDiagnostics, target string) (*langsmith.Client, bool) {
	if data == nil {
		return nil, false
	}

	switch value := data.(type) {
	case *providerData:
		return value.LangSmithClient, true
	case *langsmith.Client:
		return value, true
	default:
		diagnostics.AddError("Unexpected "+target+" Configure Type", fmt.Sprintf("Expected *providerData or *langsmith.Client, got %T", data))
		return nil, false
	}
}

func configureControlPlaneClient(data any, diagnostics configureDiagnostics, target string) (*langsmith.Client, bool) {
	if data == nil {
		return nil, false
	}

	value, ok := data.(*providerData)
	if !ok {
		diagnostics.AddError("Unexpected "+target+" Configure Type", fmt.Sprintf("Expected *providerData, got %T", data))
		return nil, false
	}
	return value.ControlPlaneClient, true
}
