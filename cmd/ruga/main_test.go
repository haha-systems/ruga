package main

import (
	"path/filepath"
	"testing"

	"github.com/haha-systems/ruga/internal/backend/codex"
	openaibackend "github.com/haha-systems/ruga/internal/backend/openai"
	"github.com/haha-systems/ruga/internal/session"
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

func TestResolveSessionSelectsAndLoadsSessions(t *testing.T) {
	store := session.NewStore(t.TempDir())
	cwd := filepath.Join(t.TempDir(), "repo")
	created, resumed, err := resolveSession(store, backendOptions{}, cwd)
	if err != nil || resumed || created.Backend != "codex" || created.CWD != cwd || created.ID == "" {
		t.Fatalf("new session = %+v, resumed=%v, err=%v", created, resumed, err)
	}

	openAI := session.New("openai", cwd)
	if err := store.Save(openAI); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	latest, resumed, err := resolveSession(store, backendOptions{name: "openai", resumeLatest: true}, cwd)
	if err != nil || !resumed || latest.ID != openAI.ID {
		t.Fatalf("latest session = %+v, resumed=%v, err=%v", latest, resumed, err)
	}

	explicit, resumed, err := resolveSession(store, backendOptions{resumeID: openAI.ID}, cwd)
	if err != nil || !resumed || explicit.ID != openAI.ID || explicit.Backend != "openai" {
		t.Fatalf("explicit session = %+v, resumed=%v, err=%v", explicit, resumed, err)
	}

	if _, _, err := resolveSession(store, backendOptions{name: "codex", resumeID: openAI.ID}, cwd); err == nil {
		t.Fatal("resolveSession() accepted a backend mismatch")
	}
}

func TestApplySessionBackendOptionsReusesSavedOpenAISettings(t *testing.T) {
	state := session.Session{
		Backend: "openai", Provider: "https://provider.example/v1", Model: "saved-model", CredentialEnv: "PROVIDER_KEY",
	}
	options := backendOptions{
		openAIBaseURL: "https://api.openai.com/v1", openAIModel: "default-model", openAIKeyEnv: "OPENAI_API_KEY",
	}
	applySessionBackendOptions(&options, state, true)
	if options.openAIBaseURL != state.Provider || options.openAIModel != state.Model || options.openAIKeyEnv != state.CredentialEnv {
		t.Fatalf("resumed OpenAI settings = %+v", options)
	}

	options = backendOptions{openAIBaseURLSet: true, openAIBaseURL: "https://override.example/v1", openAIModel: "default-model", openAIKeyEnv: "OPENAI_API_KEY"}
	applySessionBackendOptions(&options, state, true)
	if options.openAIBaseURL != "https://override.example/v1" || options.openAIModel != state.Model || options.openAIKeyEnv != state.CredentialEnv {
		t.Fatalf("explicit resume override = %+v", options)
	}
}
