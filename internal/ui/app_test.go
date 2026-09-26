package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/haha-systems/ruga/internal/bus"
	"github.com/haha-systems/ruga/internal/event"
	"github.com/haha-systems/ruga/internal/recording"
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

func TestReplayFixtureDrivesTimelineDeterministically(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	eventBus := bus.New()
	events, err := eventBus.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fixture := "../recording/testdata/coding-session.jsonl"
	if err := recording.Replay(ctx, eventBus, fixture); err != nil {
		t.Fatal(err)
	}
	if err := eventBus.Close(); err != nil {
		t.Fatal(err)
	}

	m := model{viewport: viewport.New(120, 20), ready: true, status: "idle", width: 120, height: 20}
	for range 5 {
		select {
		case ev := <-events:
			updated, _ := m.Update(batchMsg{ev})
			m = updated.(model)
		case <-ctx.Done():
			t.Fatal("timed out waiting for replay fixture")
		}
	}
	if len(m.events) != 4 || m.events[2].Kind != "message.delta" || m.events[2].Summary != "Ruga uses a normalized event bus." {
		t.Fatalf("fixture timeline = %+v", m.events)
	}
	if m.status != "idle" || m.turnActive {
		t.Fatalf("fixture turn status = %q active=%v", m.status, m.turnActive)
	}
	m.refresh(true)
	if view := m.viewport.View(); !strings.Contains(view, "Ruga uses a normalized event bus.") {
		t.Fatalf("replayed assistant response missing from timeline: %q", view)
	}
}

func TestReadOnlyReplayKeepsTimelineFocusAndIgnoresInterrupt(t *testing.T) {
	m := model{readOnly: true, turnActive: true, focus: focusTimeline}
	m.setFocus(focusComposer)
	if m.focus != focusTimeline {
		t.Fatalf("read-only focus = %v, want timeline", m.focus)
	}
	updated, cmd := m.updateKey(tea.KeyMsg{Type: tea.KeyCtrlX})
	got := updated.(model)
	if cmd != nil || !got.turnActive || got.interrupting {
		t.Fatalf("Ctrl+X changed replay state: active=%v interrupting=%v cmd=%v", got.turnActive, got.interrupting, cmd)
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
		{ev: event.Event{Kind: "turn.completed", Data: map[string]any{"status": "interrupted"}}, want: "interrupted"},
	} {
		updated, _ := m.Update(batchMsg{tt.ev})
		m = updated.(model)
		if m.status != tt.want {
			t.Fatalf("status for %s = %q, want %q", tt.ev.Kind, m.status, tt.want)
		}
	}
}

func TestInterruptMarksActiveCommandsAndToolsComplete(t *testing.T) {
	m := interactiveTestModel()
	m.add(event.Event{Kind: "command.started", TurnID: "turn-1", ItemID: "cmd-1", Summary: "sleep 60"})
	m.add(event.Event{Kind: "tool.started", TurnID: "turn-1", ItemID: "tool-1", Summary: "search"})
	updated, _ := m.Update(batchMsg{{Kind: "turn.completed", TurnID: "turn-1", Data: map[string]any{"status": "interrupted"}}})
	m = updated.(model)
	if m.status != "interrupted" || m.turnActive {
		t.Fatalf("interrupted turn state = %q active=%v", m.status, m.turnActive)
	}
	if m.events[0].Kind != "command.completed" || !strings.Contains(m.events[0].Summary, "interrupted") {
		t.Fatalf("command after interrupt = %+v", m.events[0])
	}
	if m.events[1].Kind != "tool.completed" || !strings.Contains(m.events[1].Summary, "interrupted") {
		t.Fatalf("tool after interrupt = %+v", m.events[1])
	}
}

func TestApprovalTakesFocusAndAcceptsWithY(t *testing.T) {
	var gotID string
	var gotDecision event.ApprovalDecision
	m := interactiveTestModel()
	m.actions.ResolveApproval = func(_ context.Context, requestID string, decision event.ApprovalDecision) error {
		gotID, gotDecision = requestID, decision
		return nil
	}
	request := event.ApprovalRequest{RequestID: "approval-1", Kind: "command", Command: "rm -i cache.tmp", Reason: "outside workspace"}
	updated, _ := m.Update(batchMsg{{Kind: "approval.requested", Summary: "Command execution approval requested", Approval: &request}})
	m = updated.(model)
	if m.focus != focusApproval || len(m.approvals) != 1 {
		t.Fatalf("approval focus/state = %v/%+v", m.focus, m.approvals)
	}
	view := m.View()
	for _, want := range []string{"APPROVAL REQUIRED", "rm -i cache.tmp", "outside workspace", "y/enter accept"} {
		if !strings.Contains(view, want) {
			t.Errorf("approval prompt does not contain %q: %q", want, view)
		}
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("y did not resolve the active approval")
	}
	msg := cmd()
	if _, ok := msg.(approvalResultMsg); !ok {
		t.Fatalf("approval command returned %T", msg)
	}
	m = updated.(model)
	if gotID != "approval-1" || gotDecision != event.ApprovalAccept || !m.submitting[gotID] {
		t.Fatalf("approval action = %q %q, submitting=%v", gotID, gotDecision, m.submitting)
	}
}

func TestApprovalCanRejectWithNOrEscape(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'n'}},
		{Type: tea.KeyEsc},
	} {
		var got event.ApprovalDecision
		m := interactiveTestModel()
		m.approvals = []event.ApprovalRequest{{RequestID: "approval-2", Kind: "file_change", FilePaths: []string{"main.go"}}}
		m.focus = focusApproval
		m.actions.ResolveApproval = func(_ context.Context, _ string, decision event.ApprovalDecision) error {
			got = decision
			return nil
		}
		_, cmd := m.Update(key)
		if cmd == nil {
			t.Fatalf("key %s did not schedule rejection", key.String())
		}
		cmd()
		if got != event.ApprovalReject {
			t.Fatalf("key %s decision = %q, want reject", key.String(), got)
		}
	}
}

func TestApprovalResolutionClearsPromptAndRestoresComposerFocus(t *testing.T) {
	m := interactiveTestModel()
	request := event.ApprovalRequest{RequestID: "approval-done", Kind: "file_change", FilePaths: []string{"main.go"}}
	m.add(event.Event{Kind: "approval.requested", Approval: &request})
	if m.focus != focusApproval || len(m.approvals) != 1 {
		t.Fatal("approval request did not activate its prompt")
	}
	m.add(event.Event{
		Kind: "approval.resolved", Approval: &event.ApprovalRequest{RequestID: request.RequestID},
		Decision: event.ApprovalAccept,
	})
	if len(m.approvals) != 0 || m.focus != focusComposer || !m.input.Focused() {
		t.Fatalf("resolved approval left stale focus/prompt: focus=%v approvals=%v", m.focus, m.approvals)
	}
}

func TestKeyboardFocusSeparatesComposerFromTimelineAndApproval(t *testing.T) {
	m := interactiveTestModel()
	for i := range 12 {
		m.add(event.Event{Kind: "user.message", Summary: strings.Repeat("row ", 8) + string(rune('a'+i))})
	}
	m.refresh(true)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(model)
	if m.focus != focusTimeline || m.input.Focused() {
		t.Fatalf("Tab did not move focus to timeline: focus=%v input=%v", m.focus, m.input.Focused())
	}
	m.viewport.GotoBottom()
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	if m.viewport.AtBottom() {
		t.Fatal("Up did not scroll the focused timeline")
	}
	request := event.ApprovalRequest{RequestID: "approval-focus", Kind: "permissions", Permissions: []string{"network"}}
	updated, _ = m.Update(batchMsg{{Kind: "approval.requested", Approval: &request}})
	m = updated.(model)
	if m.focus != focusApproval || m.input.Focused() {
		t.Fatal("approval request did not take keyboard focus")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(model)
	if m.input.Value() != "" {
		t.Fatalf("approval shortcut leaked into composer: %q", m.input.Value())
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(model)
	if m.focus != focusComposer || !m.input.Focused() {
		t.Fatal("Tab from approval focus did not return to composer")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(model)
	if m.input.Value() != "y" {
		t.Fatalf("composer did not receive ordinary text after focus changed: %q", m.input.Value())
	}
}

func TestTimelineNavigationBindings(t *testing.T) {
	m := interactiveTestModel()
	m.viewport.Height = 3
	m.focus = focusTimeline
	for i := range 20 {
		m.add(event.Event{Kind: "user.message", Summary: fmt.Sprintf("row %d", i)})
	}
	m.refresh(true)
	for _, tt := range []struct {
		name string
		key  tea.KeyMsg
		top  bool
	}{
		{name: "g goes to top", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}, top: true},
		{name: "end goes to bottom", key: tea.KeyMsg{Type: tea.KeyEnd}},
		{name: "G goes to bottom", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}},
		{name: "home goes to top", key: tea.KeyMsg{Type: tea.KeyHome}, top: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			updated, _ := m.Update(tt.key)
			current := updated.(model)
			if tt.top && current.viewport.YOffset != 0 {
				t.Fatalf("YOffset = %d, want top", current.viewport.YOffset)
			}
			if !tt.top && !current.viewport.AtBottom() {
				t.Fatalf("key %s did not move timeline to bottom", tt.key.String())
			}
		})
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if updated.(model).viewport.AtBottom() {
		t.Fatal("PageUp did not scroll up")
	}
	updated, _ = updated.(model).Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if !updated.(model).viewport.AtBottom() {
		t.Fatal("PageDown did not return to the bottom")
	}
}

func TestCtrlXInterruptsAndCtrlCQuits(t *testing.T) {
	interrupted := false
	m := interactiveTestModel()
	m.turnActive = true
	m.status = "working"
	m.actions.Interrupt = func(context.Context) error {
		interrupted = true
		return nil
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	if cmd == nil || updated.(model).status != "interrupting" {
		t.Fatal("Ctrl+X did not enter interrupting state")
	}
	cmd()
	if !interrupted {
		t.Fatal("Ctrl+X did not call the backend interrupt action")
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("Ctrl+C did not quit")
	}
	if updated.(model).turnActive != m.turnActive {
		t.Fatal("Ctrl+C unexpectedly changed turn state")
	}
}

func TestEscapeClearsComposerWithoutQuitting(t *testing.T) {
	m := interactiveTestModel()
	m.input.SetValue("draft")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil || updated.(model).input.Value() != "" {
		t.Fatal("Escape should clear the composer draft without quitting")
	}
}

func interactiveTestModel() model {
	input := textinput.New()
	input.Focus()
	return model{
		input: input, ctx: context.Background(), status: "idle", focus: focusComposer,
		submitting: make(map[string]bool), viewport: viewport.New(90, 12), ready: true,
		width: 90, height: 20,
	}
}
