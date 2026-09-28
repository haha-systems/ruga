// Package session stores the durable state needed to continue Ruga sessions.
// It is intentionally separate from the raw event recordings in recording.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ThreeDotsLabs/watermill"
)

// Message is the provider-neutral conversational state required by
// OpenAI-compatible backends. Provider-specific wire details remain in their
// backend adapters.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall is provider-neutral continuation data for an assistant tool call.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// TranscriptMessage preserves the user-facing conversation when provider
// context is compacted or reconstructed for resume.
type TranscriptMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Session contains the durable identity and continuation state for a run.
type Session struct {
	ID             string              `json:"id"`
	Backend        string              `json:"backend"`
	BackendSession string              `json:"backend_session,omitempty"`
	Provider       string              `json:"provider,omitempty"`
	Model          string              `json:"model,omitempty"`
	CredentialEnv  string              `json:"credential_env,omitempty"`
	CWD            string              `json:"cwd"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
	Messages       []Message           `json:"messages,omitempty"`
	Transcript     []TranscriptMessage `json:"transcript,omitempty"`
	Summary        string              `json:"summary,omitempty"`
	CompactedAt    time.Time           `json:"compacted_at,omitempty"`
}

// Store persists each session as an atomically replaced JSON file.
type Store struct{ dir string }

func NewStore(dir string) *Store { return &Store{dir: dir} }

// DefaultDir returns the private per-user location for resumable session state.
func DefaultDir() (string, error) {
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "ruga", "sessions"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find user home directory: %w", err)
	}

	return filepath.Join(home, ".local", "state", "ruga", "sessions"), nil
}

func New(backend, cwd string) Session {
	now := time.Now().UTC()
	return Session{ID: watermill.NewUUID(), Backend: backend, CWD: cwd, CreatedAt: now, UpdatedAt: now}
}

// ShortPath renders home-relative paths compactly for the TUI.
func ShortPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}

	rel, err := filepath.Rel(home, path)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		if rel == "." {
			return "~"
		}

		return filepath.Join("~", rel)
	}

	return path
}

func (s *Store) Save(value Session) error {
	if !validID(value.ID) {
		return errors.New("session ID is required")
	}

	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create session state directory %q: %w", s.dir, err)
	}

	if value.UpdatedAt.IsZero() {
		value.UpdatedAt = time.Now().UTC()
	}

	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session %q: %w", value.ID, err)
	}

	temp, err := os.CreateTemp(s.dir, ".session-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary session state: %w", err)
	}

	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("secure temporary session state: %w", err)
	}

	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write session state: %w", err)
	}

	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync session state: %w", err)
	}

	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary session state: %w", err)
	}

	if err := os.Rename(name, s.path(value.ID)); err != nil {
		return fmt.Errorf("save session %q: %w", value.ID, err)
	}

	return nil
}

func (s *Store) Load(id string) (Session, error) {
	if !validID(id) {
		return Session{}, errors.New("valid session ID is required")
	}

	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return Session{}, fmt.Errorf("read session %q: %w", id, err)
	}

	var value Session
	if err := json.Unmarshal(data, &value); err != nil {
		return Session{}, fmt.Errorf("decode session %q: %w", id, err)
	}

	if value.ID != id || value.Backend == "" {
		return Session{}, fmt.Errorf("session %q has invalid identity or backend", id)
	}

	return value, nil
}

// Latest returns the most recently updated session matching backend and cwd.
func (s *Store) Latest(backend, cwd string) (Session, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Session{}, fmt.Errorf("no %s session found for %s", backend, cwd)
		}

		return Session{}, fmt.Errorf("list sessions: %w", err)
	}

	var matches []Session
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		value, err := s.Load(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			continue
		}

		if value.Backend == backend && filepath.Clean(value.CWD) == filepath.Clean(cwd) {
			matches = append(matches, value)
		}
	}

	if len(matches) == 0 {
		return Session{}, fmt.Errorf("no %s session found for %s", backend, cwd)
	}

	sort.Slice(matches, func(i, j int) bool { return matches[i].UpdatedAt.After(matches[j].UpdatedAt) })
	return matches[0], nil
}

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".json") }

func validID(id string) bool {
	return strings.TrimSpace(id) != "" && id != "." && id != ".." && filepath.Base(id) == id && !strings.ContainsAny(id, `/\\`)
}
