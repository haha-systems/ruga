package main

import (
	"testing"

	"github.com/haha-systems/ruga/internal/backend/codex"
	openaibackend "github.com/haha-systems/ruga/internal/backend/openai"
)

func TestNewBackendSelection(t *testing.T) {
	t.Run("defaults to Codex", func(t *testing.T) {
		client, err := newBackend(backendOptions{}, ".", func(string) string { return "" })
		if err != nil {
			t.Fatalf("newBackend(): %v", err)
		}
		if _, ok := client.(*codex.Backend); !ok {
			t.Fatalf("backend type = %T, want *codex.Backend", client)
		}
	})

	t.Run("selects OpenAI-compatible backend", func(t *testing.T) {
		client, err := newBackend(backendOptions{
			name: "openai", openAIBaseURL: "http://localhost:8080/v1", openAIModel: "local-model", openAIKeyEnv: "CUSTOM_KEY",
		}, ".", func(name string) string {
			if name != "CUSTOM_KEY" {
				t.Errorf("environment lookup name = %q, want CUSTOM_KEY", name)
			}
			return "configured-key"
		})
		if err != nil {
			t.Fatalf("newBackend(): %v", err)
		}
		if client.(*openaibackend.Backend).ModelName() != "local-model" {
			t.Fatalf("model = %q, want local-model", client.(*openaibackend.Backend).ModelName())
		}
	})

	t.Run("rejects unknown backend", func(t *testing.T) {
		if _, err := newBackend(backendOptions{name: "unknown"}, ".", func(string) string { return "" }); err == nil {
			t.Fatal("newBackend() error = nil, want unknown backend error")
		}
	})
}
