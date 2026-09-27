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
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

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

	if len(m.presentation.Conversation) != 2 || m.presentation.Conversation[1].Text != "Ruga uses a normalized event bus." || len(m.presentation.Telemetry) != 1 {
		t.Fatalf("fixture presentation = %+v", m.presentation)
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

func TestCompletedReplayStaysOpenForInspection(t *testing.T) {
	stream := make(chan []event.Event)
	close(stream)
	if _, ok := waitBatch(stream, true)().(streamClosedMsg); !ok {
		t.Fatal("completed replay should stay open for inspection")
	}

	if _, ok := waitBatch(stream, false)().(tea.QuitMsg); !ok {
		t.Fatal("closed live stream should quit")
	}

	m := interactiveTestModel()
	m.readOnly = true
	updated, cmd := m.Update(streamClosedMsg{})
	if cmd != nil || updated.(model).notice != "Replay complete" {
		t.Fatal("replay completion did not leave an inspection notice")
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

	if len(m.presentation.Conversation) != 1 || m.presentation.Conversation[0].Text != "Codex works" || len(m.presentation.Conversation[0].Events) != 3 {
		t.Fatalf("coalesced conversation = %+v", m.presentation.Conversation)
	}
}

func TestCompletedAssistantMessageRendersMarkdown(t *testing.T) {
	theme := NewDefaultTheme()
	body, rendered := renderAssistantMarkdown("# A heading\n\nA **bold** answer.", 60, theme, true)
	if !rendered {
		t.Fatal("expected wide color terminal to render markdown")
	}

	body = strings.TrimSpace(ansi.Strip(body))
	if strings.Contains(body, "# A heading") || strings.Contains(body, "**bold**") || !strings.Contains(body, "A heading") || !strings.Contains(body, "bold answer") {
		t.Fatalf("markdown output = %q", body)
	}
}

func TestMarkdownRenderingFallsBackForNarrowOrColorlessTerminal(t *testing.T) {
	input := "# A heading\n\nA **bold** answer."
	for _, test := range []struct {
		name  string
		width int
		color bool
	}{
		{name: "narrow", width: 18, color: true},
		{name: "colorless", width: 60, color: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, rendered := renderAssistantMarkdown(input, test.width, NewDefaultTheme(), test.color)
			if rendered || !strings.Contains(body, "# A heading") || !strings.Contains(body, "**bold**") {
				t.Fatalf("fallback output = %q, rendered=%v", body, rendered)
			}
		})
	}
}

func TestCommandOutputIsBoundedAndLifecycleUpdatesInPlace(t *testing.T) {
	m := model{}
	const outputSize = 4096 + 17
	m.add(event.Event{Kind: "command.started", ItemID: "cmd-1", Summary: "go test ./...", Timestamp: time.Now()})
	m.add(event.Event{Kind: "command.output", ItemID: "cmd-1", Summary: strings.Repeat("x", outputSize), Data: map[string]any{"stream": "stderr"}, Timestamp: time.Now()})
	m.add(event.Event{Kind: "command.completed", ItemID: "cmd-1", Summary: "go test ./... · exit 1", Timestamp: time.Now()})
	if len(m.presentation.Telemetry) != 1 {
		t.Fatalf("command lifecycle created %d telemetry entries, want 1", len(m.presentation.Telemetry))
	}

	entry := m.presentation.Telemetry[0]
	if entry.Summary != "go test ./... · exit 1" || len(entry.Events) != 3 {
		t.Fatalf("completed command = %+v", entry)
	}

	if len(entry.Details) != 1 || len(entry.Details[0].Value) != outputSize {
		t.Fatal("presentation discarded command output")
	}

	m.viewport = viewport.New(100, 20)
	m.telemetryViewport = viewport.New(100, 20)
	m.ready = true
	m.refresh(true)
	if view := m.telemetryViewport.View(); strings.Contains(view, "stderr:") {
		t.Fatalf("normal telemetry should stay compact: %q", view)
	}

	if copied := m.copyTelemetry(); !strings.Contains(copied, strings.Repeat("x", outputSize)) {
		t.Fatalf("telemetry copy lost the complete command output")
	}
}

func TestCommandOutputPreservesUTF8Boundary(t *testing.T) {
	m := model{}
	m.add(event.Event{Kind: "command.started", ItemID: "cmd-utf8"})
	full := strings.Repeat("a", 8191) + "🙂"
	m.add(event.Event{Kind: "command.output", ItemID: "cmd-utf8", Summary: full})
	if m.presentation.Telemetry[0].Details[0].Value != full {
		t.Fatal("presentation lost UTF-8 command output")
	}

	display := strings.Join(m.expandedTelemetryLines(m.presentation.Telemetry[0], 60), "\n")
	if !utf8.ValidString(display) || !strings.Contains(display, "bytes omitted") {
		t.Fatalf("expanded UTF-8 output was malformed or unbounded: %q", display[len(display)-min(200, len(display)):])
	}
}

func TestToolProgressAndCompletionUpdateSingleEntry(t *testing.T) {
	m := model{}
	m.add(event.Event{Kind: "tool.started", ItemID: "tool-1", Summary: "search"})
	m.add(event.Event{Kind: "tool.progress", ItemID: "tool-1", Summary: "Found 3 results"})
	m.add(event.Event{Kind: "tool.completed", ItemID: "tool-1", Summary: "search · succeeded"})
	if len(m.presentation.Telemetry) != 1 || m.presentation.Telemetry[0].Summary != "search · succeeded" || len(m.presentation.Telemetry[0].Events) != 3 {
		t.Fatalf("tool lifecycle entries = %+v", m.presentation.Telemetry)
	}
}

func TestFirstClassEventsRemainReadable(t *testing.T) {
	m := model{viewport: viewport.New(120, 20), telemetryViewport: viewport.New(120, 20), ready: true}
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

	if len(m.presentation.Telemetry) != 3 || len(m.presentation.Conversation) != 1 {
		t.Fatalf("file, tool, usage, and status reduction = %+v", m.presentation)
	}

	m.refresh(true)
	view := m.telemetryViewport.View()
	for _, want := range []string{"Δ PATCH", "internal/ui/app.go", "⌕ SEARCH", "docs/search · succeeded", "USAGE", "50 total tokens"} {
		if !strings.Contains(view, want) {
			t.Errorf("telemetry rendering %q does not contain %q", view, want)
		}
	}

	if conversation := m.viewport.View(); !strings.Contains(conversation, "Checking the build") || strings.Contains(conversation, "internal/ui/app.go") ||
		strings.Contains(conversation, "docs/search") {

		t.Fatalf("conversation contains operational telemetry: %q", conversation)
	}
}

func TestSemanticTelemetryTruncatesDisplayButCopyKeepsFullText(t *testing.T) {
	m := model{viewport: viewport.New(40, 5), telemetryViewport: viewport.New(40, 5), ready: true, width: 40, height: 8}
	full := "search " + strings.Repeat("very-long-query/", 8)
	m.add(event.Event{Kind: "tool.started", ItemID: "tool-1", Summary: full})
	m.refresh(true)
	if view := m.telemetryViewport.View(); !strings.Contains(view, "…") || strings.Contains(view, full) {
		t.Fatalf("telemetry display did not truncate: %q", view)
	}

	if copied := m.copyTimeline(); !strings.Contains(copied, full) {
		t.Fatalf("copy lost full telemetry summary: %q", copied)
	}

	if m.presentation.Telemetry[0].Summary != full {
		t.Fatal("presentation model truncated the source text")
	}
}

func TestInspectorTogglePreservesIndependentViewportPositions(t *testing.T) {
	m := interactiveTestModel()
	m.resize(100, 12)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = settlePanel(t, updated.(model))
	if !m.showTelemetry || !strings.Contains(m.View(), "EVENT STREAM") {
		t.Fatal("Ctrl+E did not open the event inspector")
	}

	for i := range 18 {
		m.add(event.Event{Kind: "user.message", Summary: fmt.Sprintf("chat row %d", i)})
		m.add(event.Event{Kind: "tool.started", ItemID: fmt.Sprintf("tool-%d", i), Summary: fmt.Sprintf("search row %d", i)})
	}

	m.refreshViews(true, true)
	if !m.viewport.AtBottom() || !m.telemetryViewport.AtBottom() {
		t.Fatal("new conversation and telemetry should initially follow")
	}

	m.telemetryViewport.LineUp(3)
	if m.telemetryViewport.AtBottom() {
		t.Fatal("manual telemetry scroll did not move away from the bottom")
	}

	m.add(event.Event{Kind: "user.message", Summary: "latest chat"})
	m.add(event.Event{Kind: "tool.started", ItemID: "new-tool", Summary: "new search"})
	m.refreshViews(m.viewport.AtBottom(), m.telemetryViewport.AtBottom())
	if !m.viewport.AtBottom() || m.telemetryViewport.AtBottom() {
		t.Fatal("new activity forced the scrolled telemetry viewport to follow")
	}

	offset := m.telemetryViewport.YOffset
	m.setFocus(focusTelemetry)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = settlePanel(t, updated.(model))
	if m.showTelemetry || m.focus == focusTelemetry || strings.Contains(m.View(), "EVENT STREAM") {
		t.Fatal("Ctrl+E did not hide the inspector and restore focus")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = settlePanel(t, updated.(model))
	if !m.showTelemetry || m.telemetryViewport.YOffset != offset {
		t.Fatalf("inspector position was lost across toggle: %d to %d", offset, m.telemetryViewport.YOffset)
	}
}

func TestNarrowInspectorUsesFullWidthAndKeepsConversation(t *testing.T) {
	m := interactiveTestModel()
	m.resize(50, 10)
	m.add(event.Event{Kind: "user.message", Summary: "keep this message"})
	m.add(event.Event{Kind: "tool.started", ItemID: "tool-1", Summary: "search"})
	m.refresh(true)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = updated.(model)
	if !strings.Contains(m.View(), "EVENT STREAM") || strings.Contains(m.View(), "keep this message") {
		t.Fatalf("narrow inspector layout = %q", m.View())
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = updated.(model)
	if !strings.Contains(m.View(), "keep this message") {
		t.Fatal("conversation was lost after closing narrow inspector")
	}
}

func TestPanelPlacementControlsPaneGeometry(t *testing.T) {
	for _, placement := range []PanelPlacement{PanelRight, PanelLeft, PanelBottom, PanelTop} {
		t.Run(string(placement), func(t *testing.T) {
			m := interactiveTestModel()
			m.panelPlacement = placement
			m.showTelemetry = true
			m.resize(100, 20)
			m.viewport.SetContent("conversation sentinel")
			m.telemetryViewport.SetContent("event stream sentinel")
			conversationInsetsH, conversationInsetsV := panelInsets(m.focus == focusTimeline)
			inspectorInsetsH, inspectorInsetsV := panelInsets(m.focus == focusTelemetry)

			if placement.vertical() {
				if m.viewport.Width != 100-conversationInsetsH || m.telemetryViewport.Width != 100-inspectorInsetsH {
					t.Fatalf("vertical pane widths = %d, %d", m.viewport.Width, m.telemetryViewport.Width)
				}

				telemetryOuter := m.telemetryViewport.Height + 1 + inspectorInsetsV
				conversationOuter := m.viewport.Height + 1 + conversationInsetsV
				if telemetryOuter != m.panelHeight || conversationOuter+telemetryOuter+1 != 10 {
					t.Fatalf("vertical pane outer heights = %d, %d with panel height %d", conversationOuter, telemetryOuter, m.panelHeight)
				}
			} else {
				conversationOuter, inspectorOuter := m.viewWidths()
				if m.viewport.Width != conversationOuter-conversationInsetsH || m.telemetryViewport.Width != inspectorOuter-inspectorInsetsH ||
					m.viewport.Height != 10-1-conversationInsetsV || m.telemetryViewport.Height != 10-1-inspectorInsetsV {

					t.Fatalf("horizontal pane geometry = %dx%d and %dx%d", m.viewport.Width, m.viewport.Height, m.telemetryViewport.Width, m.telemetryViewport.Height)
				}
			}

			heading := m.viewHeading()
			conversationAt, inspectorAt := strings.Index(heading, "CONVERSATION"), strings.Index(heading, "EVENT STREAM")
			if conversationAt < 0 || inspectorAt < 0 {
				t.Fatalf("pane heading = %q", heading)
			}

			if (placement == PanelLeft || placement == PanelTop) && inspectorAt > conversationAt {
				t.Fatalf("inspector should precede conversation for %s placement: %q", placement, heading)
			}

			if (placement == PanelRight || placement == PanelBottom) && conversationAt > inspectorAt {
				t.Fatalf("conversation should precede inspector for %s placement: %q", placement, heading)
			}

			body := m.viewBody()
			conversationAt, inspectorAt = strings.Index(body, "conversation sentinel"), strings.Index(body, "event stream sentinel")
			if conversationAt < 0 || inspectorAt < 0 {
				t.Fatalf("pane body = %q", body)
			}

			if (placement == PanelLeft || placement == PanelTop) && inspectorAt > conversationAt {
				t.Fatalf("inspector body should precede conversation for %s placement: %q", placement, body)
			}

			if (placement == PanelRight || placement == PanelBottom) && conversationAt > inspectorAt {
				t.Fatalf("conversation body should precede inspector for %s placement: %q", placement, body)
			}
		})
	}
}

func TestResizeAccountsForPanelInsets(t *testing.T) {
	widths, heights := []int{18, 50, 70, 100}, []int{4, 10, 20}
	placements := []PanelPlacement{PanelRight, PanelLeft, PanelBottom, PanelTop}
	focuses := []focusTarget{focusTimeline, focusTelemetry, focusComposer, focusApproval}

	for _, width := range widths {
		for _, height := range heights {
			for _, placement := range placements {
				for _, focus := range focuses {
					for _, searchActive := range []bool{false, true} {
						name := fmt.Sprintf("%dx%d/%s/focus-%d/search-%t", width, height, placement, focus, searchActive)
						t.Run(name, func(t *testing.T) {
							m := interactiveTestModel()
							m.panelPlacement = placement
							m.showTelemetry = true
							m.focus = focus
							m.approvals = []event.ApprovalRequest{{RequestID: "approval-1", Kind: "command", Command: "echo ok"}}
							m.search = textinput.New()
							m.search.Prompt = "/ "
							m.searchActive = searchActive
							m.input.SetValue("composer-visible")
							m.resize(width, height)

							conversationOuter, inspectorOuter := width, width
							if width >= 70 && !placement.vertical() {
								conversationOuter, inspectorOuter = m.viewWidths()
							}

							conversationInsets, _ := panelInsets(focus == focusTimeline)
							inspectorInsets, _ := panelInsets(focus == focusTelemetry)
							if want := max(1, conversationOuter-conversationInsets); m.viewport.Width != want {
								t.Fatalf("conversation width = %d, want %d", m.viewport.Width, want)
							}

							if want := max(1, inspectorOuter-inspectorInsets); m.telemetryViewport.Width != want {
								t.Fatalf("inspector width = %d, want %d", m.telemetryViewport.Width, want)
							}

							if m.viewport.Height < 1 || m.telemetryViewport.Height < 1 || m.input.Width < 1 || m.search.Width < 1 {
								t.Fatalf(
									"invalid content dimensions: conversation=%dx%d inspector=%dx%d input=%d search=%d",
									m.viewport.Width,
									m.viewport.Height,
									m.telemetryViewport.Width,
									m.telemetryViewport.Height,
									m.input.Width,
									m.search.Width,
								)
							}

							for _, line := range strings.Split(m.View(), "\n") {
								if got := lipgloss.Width(line); got > width {
									t.Fatalf("rendered line width = %d, terminal width = %d: %q", got, width, line)
								}
							}

							if width == 100 && height == 20 && focus == focusComposer && !searchActive {
								view := m.View()
								if got := lipgloss.Height(view); got > height {
									t.Fatalf("rendered height = %d, terminal height = %d", got, height)
								}

								if !strings.Contains(view, "composer-visible") || !strings.Contains(view, "ctrl+c quit") {
									t.Fatal("composer or help line is missing from the rendered view")
								}
							}
						})
					}
				}
			}
		}
	}
}

func TestFocusChangePreservesViewportPosition(t *testing.T) {
	m := interactiveTestModel()
	m.panelPlacement = PanelRight
	m.showTelemetry = true
	m.setPanelSize(m.desiredPanelSize())
	m.resize(100, 20)
	for index := range 100 {
		m.add(event.Event{Kind: "user.message", Summary: fmt.Sprintf("conversation row %d", index)})
		m.add(event.Event{Kind: "tool.started", ItemID: fmt.Sprintf("tool-%d", index), Summary: fmt.Sprintf("event row %d", index)})
	}

	m.refreshViews(true, true)
	m.viewport.SetYOffset(3)
	m.telemetryViewport.SetYOffset(4)

	m.setFocus(focusTimeline)
	conversationOuter, _ := m.viewWidths()
	conversationInsets, _ := panelInsets(true)
	if want := max(1, conversationOuter-conversationInsets); m.viewport.Width != want {
		t.Fatalf("conversation width after focus = %d, want %d", m.viewport.Width, want)
	}

	if m.viewport.YOffset != 3 || m.telemetryViewport.YOffset != 4 {
		t.Fatalf("manual offsets changed after focus: %d, %d", m.viewport.YOffset, m.telemetryViewport.YOffset)
	}

	m.viewport.GotoBottom()
	m.telemetryViewport.GotoBottom()
	m.telemetryFollowing = true
	m.setFocus(focusTelemetry)
	if !m.viewport.AtBottom() || !m.telemetryViewport.AtBottom() {
		t.Fatal("viewports left the bottom after focus changed their dimensions")
	}
}

func TestPanelAnimationFitsContainer(t *testing.T) {
	for _, placement := range []PanelPlacement{PanelRight, PanelLeft, PanelBottom, PanelTop} {
		t.Run(string(placement), func(t *testing.T) {
			m := interactiveTestModel()
			m.panelPlacement = placement
			m.resize(100, 20)
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
			m = updated.(model)

			for frame := 0; frame < 40 && m.panelAnimating; frame++ {
				updated, _ = m.Update(panelFrameMsg{generation: m.panelGeneration})
				m = updated.(model)
				assertViewFitsTerminal(t, m)
			}

			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
			m = updated.(model)
			for frame := 0; frame < 40 && m.panelAnimating; frame++ {
				updated, _ = m.Update(panelFrameMsg{generation: m.panelGeneration})
				m = updated.(model)
				assertViewFitsTerminal(t, m)
			}
		})
	}
}

func TestLongContentStaysInsidePanel(t *testing.T) {
	m := interactiveTestModel()
	m.panelPlacement = PanelRight
	m.showTelemetry = true
	m.setPanelSize(m.desiredPanelSize())
	m.resize(100, 20)
	m.add(event.Event{Kind: "assistant.message", Summary: strings.Repeat("assistant text ", 40)})
	m.add(event.Event{Kind: "command.started", ItemID: "command-1", Summary: "go test ./..."})
	m.add(event.Event{Kind: "command.output", ItemID: "command-1", Summary: strings.Repeat("output ", 1300)})
	m.expandedTelemetry = map[int]bool{1: true}
	m.setFocus(focusTelemetry)
	m.refreshViews(false, false)

	view := m.View()
	for _, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > m.width {
			t.Fatalf("rendered line width %d exceeds terminal width %d", width, m.width)
		}
	}

	expanded := strings.Join(m.expandedTelemetryLines(m.presentation.Telemetry[1], m.telemetryViewport.Width), "\n")
	if !strings.Contains(expanded, "bytes omitted") {
		t.Fatal("expanded command output lost the existing truncation marker")
	}

	for _, line := range strings.Split(expanded, "\n") {
		if width := lipgloss.Width(line); width > m.telemetryViewport.Width {
			t.Fatalf("expanded line width %d exceeds inspector width %d", width, m.telemetryViewport.Width)
		}
	}
}

func assertViewFitsTerminal(t *testing.T, m model) {
	t.Helper()
	view := m.View()
	if height := lipgloss.Height(view); height > m.height {
		t.Fatalf("view height %d exceeds terminal height %d", height, m.height)
	}

	for _, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > m.width {
			t.Fatalf("view line width %d exceeds terminal width %d: %q", width, m.width, line)
		}
	}
}

func TestViewFocusedRegionBorder(t *testing.T) {
	tests := []struct {
		name   string
		focus  focusTarget
		marker string
	}{
		{name: "conversation", focus: focusTimeline, marker: "CONVERSATION"},
		{name: "inspector", focus: focusTelemetry, marker: "EVENT STREAM"},
		{name: "composer", focus: focusComposer, marker: "Message Codex"},
		{name: "approval", focus: focusApproval, marker: "APPROVAL REQUIRED"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := interactiveTestModel()
			if test.focus == focusTelemetry {
				m.showTelemetry = true
				m.setPanelSize(m.desiredPanelSize())
			}

			if test.focus == focusApproval {
				m.approvals = []event.ApprovalRequest{{RequestID: "approval-1", Kind: "command", Command: "echo ok"}}
			}

			if test.focus == focusComposer {
				m.input.SetValue("composer sentinel")
				test.marker = "composer sentinel"
			}

			m.setFocus(test.focus)
			assertOnlyFocusedPanelContains(t, m.View(), test.marker)
		})
	}
}

func TestViewApprovalFocusBorderFallsBackToComposer(t *testing.T) {
	m := interactiveTestModel()
	m.input.SetValue("composer sentinel")
	m.setFocus(focusApproval)
	view := m.View()
	if strings.Contains(view, "APPROVAL REQUIRED") {
		t.Fatal("view shows an approval region without a pending approval")
	}

	assertOnlyFocusedPanelContains(t, view, "composer sentinel")
}

func assertOnlyFocusedPanelContains(t *testing.T, view, marker string) {
	t.Helper()
	top := strings.Index(view, "╭")
	bottom := strings.Index(view, "╰")
	if strings.Count(view, "╭") != 1 || strings.Count(view, "╰") != 1 || top < 0 || bottom < top {
		t.Fatalf("view does not contain exactly one focused panel border: %q", view)
	}

	content := strings.Index(view, marker)
	if content < top || content > bottom {
		t.Fatalf("focused panel does not contain %q: %q", marker, view)
	}
}

func TestSmallTerminalKeepsEveryRenderedLineWithinWidth(t *testing.T) {
	m := interactiveTestModel()
	m.resize(25, 8)
	m.add(event.Event{Kind: "user.message", Summary: strings.Repeat("conversation text ", 5)})
	m.add(event.Event{Kind: "tool.started", ItemID: "tool-1", Summary: "search", Data: map[string]any{"tool_name": "search", "arguments": `{"query":"long-query-for-small-terminals"}`}})
	m.refreshViews(true, true)
	for _, showInspector := range []bool{false, true} {
		if showInspector {
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
			m = updated.(model)
			m.setFocus(focusTelemetry)
			m.toggleTelemetryExpansion()
		}

		for _, line := range strings.Split(m.View(), "\n") {
			if width := lipgloss.Width(line); width > 25 {
				t.Fatalf("line width %d exceeds terminal width: %q", width, line)
			}
		}
	}
}

func TestPanelAnimationCanReverseWhileEventsKeepArriving(t *testing.T) {
	m := interactiveTestModel()
	m.resize(100, 14)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = updated.(model)
	if cmd == nil || !m.panelAnimating || m.panelWidth != 0 {
		t.Fatal("opening the inspector did not schedule a transition")
	}

	openingGeneration := m.panelGeneration
	updated, _ = m.Update(panelFrameMsg{generation: openingGeneration})
	m = updated.(model)
	if m.panelWidth <= 0 || m.panelWidth >= desiredPanelWidth(m.width) {
		t.Fatalf("first transition width = %d", m.panelWidth)
	}

	updated, _ = m.Update(batchMsg{{Kind: "user.message", Summary: "arrived during motion"}})
	m = updated.(model)
	if len(m.presentation.Conversation) != 1 {
		t.Fatal("animation blocked event reduction")
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = updated.(model)
	if cmd == nil || m.showTelemetry || m.panelGeneration == openingGeneration {
		t.Fatal("closing during an opening transition was not scheduled")
	}

	staleWidth := m.panelWidth
	updated, _ = m.Update(panelFrameMsg{generation: openingGeneration})
	m = updated.(model)
	if m.panelWidth != staleWidth {
		t.Fatal("stale animation frame changed panel width")
	}

	m = settlePanel(t, m)
	if m.panelWidth != 0 || !strings.Contains(m.View(), "arrived during motion") {
		t.Fatal("reversed transition lost conversation or left the panel visible")
	}
}

func TestHiddenActivityIndicatorTicksOnlyForActiveTools(t *testing.T) {
	m := interactiveTestModel()
	m.resize(100, 14)
	updated, cmd := m.Update(batchMsg{
		{Kind: "turn.started", TurnID: "turn-1"},
		{Kind: "tool.started", TurnID: "turn-1", ItemID: "tool-1", Summary: "search", Data: map[string]any{"tool_name": "search"}},
	})
	m = updated.(model)
	if cmd == nil || !m.activityTicking || m.presentation.Activity.ActiveTools != 1 || m.unseenTelemetry != 2 {
		t.Fatalf("active hidden activity = %+v, unseen=%d", m.presentation.Activity, m.unseenTelemetry)
	}

	if header := m.headerLine(); !strings.Contains(header, "LOG 2 +2") || !strings.Contains(header, "SEARCH") || !strings.Contains(header, "◐") {
		t.Fatalf("hidden activity indicator = %q", header)
	}

	activeGeneration := m.activityGeneration
	updated, cmd = m.Update(activityFrameMsg{generation: activeGeneration})
	m = updated.(model)
	if cmd == nil || m.activityFrame != 1 || !strings.Contains(m.headerLine(), "◓") {
		t.Fatal("active tool did not advance its restrained indicator")
	}

	updated, _ = m.Update(batchMsg{
		{Kind: "tool.completed", TurnID: "turn-1", ItemID: "tool-1", Summary: "search · succeeded", Data: map[string]any{"tool_name": "search"}},
		{Kind: "turn.completed", TurnID: "turn-1", Data: map[string]any{"status": "completed"}},
	})
	m = updated.(model)
	if m.activityTicking || m.presentation.Activity.ActiveTools != 0 {
		t.Fatal("activity animation continued after tools completed")
	}

	updated, cmd = m.Update(activityFrameMsg{generation: activeGeneration})
	if cmd != nil || updated.(model).activityFrame != m.activityFrame {
		t.Fatal("stale activity frame changed an idle view")
	}
}

func TestUnseenTelemetryWaitsForExplicitFollow(t *testing.T) {
	m := interactiveTestModel()
	m.showTelemetry = true
	m.resize(100, 10)
	for i := range 20 {
		m.add(event.Event{Kind: "tool.started", ItemID: fmt.Sprintf("tool-%d", i), Summary: "search"})
	}

	m.refreshViews(true, true)
	m.setFocus(focusTelemetry)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = updated.(model)
	if m.telemetryFollowing || m.telemetryViewport.AtBottom() {
		t.Fatal("PageUp did not suspend telemetry follow")
	}

	updated, _ = m.Update(batchMsg{{Kind: "tool.started", ItemID: "new-tool", Summary: "read"}})
	m = updated.(model)
	if m.unseenTelemetry != 1 || m.telemetryViewport.AtBottom() || !strings.Contains(m.viewHeading(), "+1") {
		t.Fatal("new telemetry did not remain unseen while scrolled away")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = updated.(model)
	if m.unseenTelemetry != 0 || !m.telemetryFollowing || !m.telemetryViewport.AtBottom() {
		t.Fatal("End did not restore follow and clear unseen activity")
	}
}

func TestTelemetrySelectionExpandsInlineAndRevealsTruncatedText(t *testing.T) {
	m := interactiveTestModel()
	m.showTelemetry = true
	m.resize(65, 24)
	query := strings.Repeat("ResumeThread", 6)
	m.add(event.Event{Kind: "tool.started", ItemID: "search-1", Summary: "search", Data: map[string]any{"tool_name": "search", "arguments": fmt.Sprintf(`{"query":%q}`, query)}})
	m.add(event.Event{Kind: "tool.completed", ItemID: "search-1", Summary: "search · succeeded", Data: map[string]any{"tool_name": "search", "result": "6 hits"}})
	m.add(event.Event{Kind: "tool.started", ItemID: "read-2", Summary: "read", Data: map[string]any{"tool_name": "read", "arguments": `{"path":"internal/session/session.go"}`}})
	m.refreshViews(true, true)
	m.setFocus(focusTelemetry)
	if m.selectedTelemetry != 1 {
		t.Fatalf("initial selection = %d, want latest item", m.selectedTelemetry)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	if m.selectedTelemetry != 0 || m.telemetryFollowing {
		t.Fatalf("selection/follow after Up = %d/%v", m.selectedTelemetry, m.telemetryFollowing)
	}

	compact := m.telemetryViewport.View()
	if !strings.Contains(compact, "…") || strings.Contains(compact, query) {
		t.Fatalf("compact line did not truncate the long query: %q", compact)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if !m.expandedTelemetry[0] {
		t.Fatal("Enter did not expand selected item")
	}

	if expanded := m.telemetryViewport.View(); !strings.Contains(expanded, "query") || !strings.Contains(expanded, query[:30]) || !strings.Contains(expanded, "6 hits") {
		t.Fatalf("expanded telemetry omitted details: %q", expanded)
	}

	if m.presentation.Telemetry[0].Primary != fmt.Sprintf("%q", query) {
		t.Fatal("compact truncation changed the stored primary value")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if m.expandedTelemetry[0] {
		t.Fatal("Enter did not collapse selected item")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = updated.(model)
	if m.selectedTelemetry != 1 || !m.telemetryFollowing || !m.telemetryViewport.AtBottom() {
		t.Fatal("End did not restore follow at the latest item")
	}
}

func TestExpandedCommandOutputIsBoundedOnlyForDisplay(t *testing.T) {
	m := interactiveTestModel()
	m.showTelemetry = true
	m.resize(70, 20)
	full := strings.Repeat("long output line\n", 700)
	m.add(event.Event{Kind: "command.started", ItemID: "cmd-1", Summary: "go test ./...", Data: map[string]any{"command": "go test ./..."}})
	m.add(event.Event{Kind: "command.output", ItemID: "cmd-1", Summary: full, Data: map[string]any{"stream": "stderr"}})
	m.refreshViews(true, true)
	m.setFocus(focusTelemetry)
	m.toggleTelemetryExpansion()
	if content := m.telemetryViewport.View(); !strings.Contains(content, "stderr") {
		t.Fatalf("expanded command output is missing: %q", content)
	}

	if !strings.Contains(m.copyTelemetry(), full) || m.presentation.Telemetry[0].Details[0].Value != full {
		t.Fatal("expanded display limit discarded command output")
	}

	if len(m.telemetryViewport.View()) > 2000 {
		t.Fatal("viewport rendered unbounded command output")
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

	if m.presentation.Telemetry[0].Status != "stopped" || m.presentation.Telemetry[0].DisplayStatus != "! STOPPED" {
		t.Fatalf("command after interrupt = %+v", m.presentation.Telemetry[0])
	}

	if m.presentation.Telemetry[1].Status != "stopped" || m.presentation.Telemetry[1].DisplayStatus != "! STOPPED" {
		t.Fatalf("tool after interrupt = %+v", m.presentation.Telemetry[1])
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
		submitting: make(map[string]bool), viewport: viewport.New(90, 12), telemetryViewport: viewport.New(90, 12), ready: true,
		width: 90, height: 20, selectedTelemetry: -1, telemetryFollowing: true,
	}
}

func settlePanel(t *testing.T, m model) model {
	t.Helper()
	for frame := 0; frame < 40 && m.panelAnimating; frame++ {
		updated, _ := m.Update(panelFrameMsg{generation: m.panelGeneration})
		m = updated.(model)
	}

	if m.panelAnimating {
		t.Fatalf("panel animation did not settle: width=%d position=%f", m.panelWidth, m.panelPosition)
	}

	return m
}
