package ui

import (
	"context"
	"strings"
	"testing"
	"time"

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
