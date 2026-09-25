package ui

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/haha-systems/ruga/internal/event"
)

func TestTimelineFollowsOnlyWhenAlreadyAtBottom(t *testing.T) {
	m := model{viewport: viewport.New(50, 3), ready: true}
	for i := range 10 {
		m.add(event.Event{Kind: "user.message", Summary: strings.Repeat("line ", 5) + string(rune('a'+i))})
	}
	m.refresh(true)
	if !m.viewport.AtBottom() {
		t.Fatal("timeline should start at the bottom")
	}
	m.viewport.LineUp(2)
	if m.viewport.AtBottom() {
		t.Fatal("expected manual scroll to move away from the bottom")
	}
	m.add(event.Event{Kind: "user.message", Summary: "new event"})
	m.refresh(m.viewport.AtBottom())
	if m.viewport.AtBottom() {
		t.Fatal("new event forced the manually scrolled timeline to the bottom")
	}
}

func TestEnterSubmitsComposerValue(t *testing.T) {
	ctx := context.Background()
	input := textinput.New()
	input.Focus()
	input.SetValue("  explain this repository  ")
	var submitted string
	m := model{
		input:  input,
		ctx:    ctx,
		status: "idle",
		submit: func(_ context.Context, prompt string) error {
			submitted = prompt
			return nil
		},
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter did not schedule a submission")
	}
	msg := cmd()
	if _, ok := msg.(submitResultMsg); !ok {
		t.Fatalf("submission command returned %T", msg)
	}
	if submitted != "explain this repository" {
		t.Fatalf("submitted prompt = %q", submitted)
	}
	if updated.(model).status != "working" {
		t.Fatalf("status after submission = %q", updated.(model).status)
	}
}

func TestDeltaEventsCoalesceInTimeline(t *testing.T) {
	m := model{}
	for _, part := range []string{"Cod", "ex", " works"} {
		m.add(event.Event{Kind: "message.delta", ItemID: "item-1", Summary: part, Timestamp: time.Now()})
	}
	if len(m.events) != 1 || m.events[0].Summary != "Codex works" {
		t.Fatalf("coalesced events = %+v", m.events)
	}
}

func TestCommandOutputIsBoundedAndLifecycleUpdatesInPlace(t *testing.T) {
	m := model{}
	m.add(event.Event{Kind: "command.started", ItemID: "cmd-1", Summary: "go test ./...", Timestamp: time.Now()})
	m.add(event.Event{Kind: "command.output", ItemID: "cmd-1", Summary: strings.Repeat("x", maxCommandOutputBytes+17), Data: map[string]any{"stream": "stderr"}, Timestamp: time.Now()})
	m.add(event.Event{Kind: "command.completed", ItemID: "cmd-1", Summary: "go test ./... · exit 1", Timestamp: time.Now()})
	if len(m.events) != 1 {
		t.Fatalf("command lifecycle created %d timeline entries, want 1", len(m.events))
	}
	entry := m.events[0]
	if entry.Kind != "command.completed" || entry.Summary != "go test ./... · exit 1" {
		t.Fatalf("completed command = %+v", entry)
	}
	if outputBytes(entry) != maxCommandOutputBytes || omittedOutputBytes(entry.Data) != 17 {
		t.Fatalf("output limits = retained %d, omitted %d", outputBytes(entry), omittedOutputBytes(entry.Data))
	}
	if got := len(entry.Data["output"].(map[string]string)["stderr"]); got != maxCommandOutputBytes {
		t.Fatalf("retained output bytes = %d, want %d", got, maxCommandOutputBytes)
	}
	m.viewport = viewport.New(100, 20)
	m.ready = true
	m.refresh(true)
	if view := m.viewport.View(); !strings.Contains(view, "stderr:") || !strings.Contains(view, "17 output bytes omitted") {
		t.Fatalf("command output rendering is missing details: %q", view)
	}
}

func TestCommandOutputPreservesUTF8Boundary(t *testing.T) {
	m := model{}
	m.add(event.Event{Kind: "command.started", ItemID: "cmd-utf8"})
	m.add(event.Event{Kind: "command.output", ItemID: "cmd-utf8", Summary: strings.Repeat("a", maxCommandOutputBytes-1) + "🙂"})
	output := m.events[0].Data["output"].(map[string]string)["stdout"]
	if !utf8.ValidString(output) || len(output) > maxCommandOutputBytes {
		t.Fatalf("stored output is invalid or too large: bytes=%d", len(output))
	}
	if omittedOutputBytes(m.events[0].Data) != len("🙂") {
		t.Fatalf("omitted bytes = %d, want %d", omittedOutputBytes(m.events[0].Data), len("🙂"))
	}
}

func TestToolProgressAndCompletionUpdateSingleEntry(t *testing.T) {
	m := model{}
	m.add(event.Event{Kind: "tool.started", ItemID: "tool-1", Summary: "search"})
	m.add(event.Event{Kind: "tool.progress", ItemID: "tool-1", Summary: "Found 3 results"})
	m.add(event.Event{Kind: "tool.completed", ItemID: "tool-1", Summary: "search · succeeded"})
	if len(m.events) != 1 || m.events[0].Kind != "tool.completed" || m.events[0].Summary != "search · succeeded" {
		t.Fatalf("tool lifecycle entries = %+v", m.events)
	}
}

func TestFirstClassEventsRemainReadable(t *testing.T) {
	m := model{viewport: viewport.New(120, 20), ready: true}
	for _, ev := range []event.Event{
		{Kind: "file.changed", ItemID: "file-1", Summary: "fileChange internal/ui/app.go"},
		{Kind: "file.changed", ItemID: "file-1", Summary: "update internal/ui/app.go"},
		{Kind: "tool.started", ItemID: "tool-1", Summary: "docs/search"},
		{Kind: "tool.progress", ItemID: "tool-1", Summary: "Found 3 results"},
		{Kind: "tool.completed", ItemID: "tool-1", Summary: "docs/search · succeeded"},
		{Kind: "status.update", Summary: "Checking the build"},
		{Kind: "usage.updated", Summary: "40 in · 10 out · 50 total tokens · context 128000"},
	} {
		m.add(ev)
	}
	if len(m.events) != 4 {
		t.Fatalf("file and tool lifecycles should coalesce, got %d rows", len(m.events))
	}
	m.refresh(true)
	view := m.viewport.View()
	for _, want := range []string{"file.changed", "internal/ui/app.go", "tool.completed", "docs/search · succeeded", "status.update", "Checking the build", "usage.updated", "50 total tokens"} {
		if !strings.Contains(view, want) {
			t.Errorf("timeline rendering %q does not contain %q", view, want)
		}
	}
}

func TestTurnEventsUpdateStatus(t *testing.T) {
	m := model{viewport: viewport.New(50, 3), ready: true, status: "idle"}
	for _, tt := range []struct {
		ev   event.Event
		want string
	}{
		{ev: event.Event{Kind: "turn.started"}, want: "working"},
		{ev: event.Event{Kind: "turn.completed", Data: map[string]any{"status": "completed"}}, want: "idle"},
		{ev: event.Event{Kind: "turn.completed", Data: map[string]any{"status": "failed"}}, want: "error"},
	} {
		updated, _ := m.Update(batchMsg{tt.ev})
		m = updated.(model)
		if m.status != tt.want {
			t.Fatalf("status for %s = %q, want %q", tt.ev.Kind, m.status, tt.want)
		}
	}
}
