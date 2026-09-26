package recording

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haha-systems/ruga/internal/bus"
	"github.com/haha-systems/ruga/internal/event"
)

func TestRecorderStoresNormalizedEventsAndReplayPreservesOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	liveBus := bus.New()
	recorder, err := NewRecorder(context.Background(), liveBus, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	want := []event.Event{
		{ID: "one", Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Backend: "codex", Kind: "user.message", Summary: "hello"},
		{ID: "two", Timestamp: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC), Backend: "codex", Kind: "message.delta", Summary: "world", Data: map[string]any{"n": float64(2)}},
	}
	for _, ev := range want {
		if err := liveBus.Publish(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}

	if err := liveBus.Close(); err != nil {
		t.Fatal(err)
	}

	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}

	payload, err := os.ReadFile(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(string(payload)), "\n")
	if len(lines) != len(want) {
		t.Fatalf("recorded %d JSONL lines, want %d", len(lines), len(want))
	}

	replayBus := bus.New()
	events, err := replayBus.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := Replay(ctx, replayBus, recorder.Path()); err != nil {
		t.Fatal(err)
	}

	if err := replayBus.Close(); err != nil {
		t.Fatal(err)
	}

	for i, expected := range want {
		select {
		case got := <-events:
			var expectedJSON, gotJSON []byte
			expectedJSON, _ = json.Marshal(expected)
			gotJSON, _ = json.Marshal(got)
			if string(gotJSON) != string(expectedJSON) {
				t.Fatalf("event %d = %s, want %s", i, gotJSON, expectedJSON)
			}

		case <-ctx.Done():
			t.Fatalf("waiting for event %d: %v", i, ctx.Err())
		}
	}
}

func TestReplayReportsMalformedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"user.message"}`+"\nnot-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Replay(context.Background(), inertBus{}, path)
	if err == nil || !strings.Contains(err.Error(), ":2: decode event:") {
		t.Fatalf("Replay error = %v, want path and line 2 decode error", err)
	}
}

type inertBus struct{}

func (inertBus) Publish(context.Context, event.Event) error { return nil }
func (inertBus) Subscribe(context.Context) (<-chan event.Event, error) {
	return nil, nil
}

func TestResolveSessionAcceptsIDAndPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	resolved, err := ResolveSession("session-123")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(resolved, filepath.Join("ruga", "sessions", "session-123.jsonl")) {
		t.Fatalf("resolved session ID = %q", resolved)
	}

	path := filepath.Join(t.TempDir(), "recording.jsonl")
	resolved, err = ResolveSession(path)
	if err != nil || resolved != path {
		t.Fatalf("resolved explicit path = %q, %v", resolved, err)
	}
}
