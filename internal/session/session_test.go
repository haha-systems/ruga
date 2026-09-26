package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTripAndLatestByDirectory(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	cwd := filepath.Join(t.TempDir(), "repo")
	first := New("openai", cwd)
	first.Provider, first.Model = "https://example.test/v1", "model-a"
	first.Messages = []Message{
		{Role: "user", Content: "remember"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "provider-id", Name: "echo", Arguments: `{"text":"hello"}`}}},
		{Role: "tool", ToolCallID: "provider-id", Name: "echo", Content: "hello"},
	}
	if err := store.Save(first); err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	loaded, err := store.Load(first.ID)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.ID != first.ID || loaded.Backend != first.Backend || loaded.CWD != cwd || len(loaded.Messages) != 3 || loaded.Messages[1].ToolCalls[0].ID != "provider-id" ||
		loaded.Messages[2].ToolCallID != "provider-id" {
		t.Fatalf("loaded session = %+v", loaded)
	}
	info, err := os.Stat(store.path(first.ID))
	if err != nil {
		t.Fatalf("stat session: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("session file permissions = %o, want 600", info.Mode().Perm())
	}

	second := New("openai", cwd)
	second.UpdatedAt = first.UpdatedAt.Add(time.Second)
	if err := store.Save(second); err != nil {
		t.Fatalf("Save(second): %v", err)
	}
	otherDir := New("openai", filepath.Join(t.TempDir(), "other"))
	if err := store.Save(otherDir); err != nil {
		t.Fatalf("Save(otherDir): %v", err)
	}
	latest, err := store.Latest("openai", cwd)
	if err != nil {
		t.Fatalf("Latest(): %v", err)
	}
	if latest.ID != second.ID {
		t.Fatalf("latest ID = %q, want %q", latest.ID, second.ID)
	}
}

func TestLoadRejectsPathLikeIDs(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Load("../outside"); err == nil {
		t.Fatal("Load() accepted a path-like ID")
	}
}
