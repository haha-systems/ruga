package ui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/haha-systems/ruga/internal/event"
)

type batchMsg []event.Event

type submitResultMsg struct{ err error }

type model struct {
	viewport viewport.Model
	input    textinput.Model
	stream   <-chan []event.Event
	submit   func(context.Context, string) error
	ctx      context.Context
	events   []event.Event
	status   string
	ready    bool
}

const maxCommandOutputBytes = 4096

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	mutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

func Run(ctx context.Context, events <-chan event.Event, submit func(context.Context, string) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	input := textinput.New()
	input.Prompt = "› "
	input.Placeholder = "Message Codex and press Enter"
	input.Focus()
	program := tea.NewProgram(model{
		stream: batchEvents(ctx, events), input: input, submit: submit, ctx: ctx, status: "idle",
	}, tea.WithContext(ctx), tea.WithAltScreen())
	_, err := program.Run()
	return err
}

func (m model) Init() tea.Cmd { return tea.Batch(waitBatch(m.stream), textinput.Blink) }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		follow := !m.ready || m.viewport.AtBottom()
		if !m.ready {
			m.viewport = viewport.New(msg.Width, max(1, msg.Height-3))
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = max(1, msg.Height-3)
		}
		m.input.Width = max(1, msg.Width-4)
		m.refresh(follow)
	case batchMsg:
		follow := !m.ready || m.viewport.AtBottom()
		for _, ev := range msg {
			m.add(ev)
			switch ev.Kind {
			case "turn.started":
				m.status = "working"
			case "turn.completed":
				m.status = "idle"
				if status, _ := ev.Data["status"].(string); status == "failed" {
					m.status = "error"
				}
			case "error":
				m.status = "error"
			}
		}
		m.refresh(follow)
		return m, waitBatch(m.stream)
	case submitResultMsg:
		if msg.err != nil {
			m.status = "error"
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			prompt := strings.TrimSpace(m.input.Value())
			if prompt == "" || m.status == "working" {
				return m, nil
			}
			m.input.Reset()
			m.status = "working"
			return m, submitPrompt(m.ctx, m.submit, prompt)
		case "up":
			m.viewport.LineUp(1)
			return m, nil
		case "down":
			m.viewport.LineDown(1)
			return m, nil
		case "pgup":
			m.viewport.PageUp()
			return m, nil
		case "pgdown":
			m.viewport.PageDown()
			return m, nil
		}
		var inputCmd tea.Cmd
		m.input, inputCmd = m.input.Update(msg)
		return m, inputCmd
	}
	return m, nil
}

func (m model) View() string {
	if !m.ready {
		return "Starting Codex App Server…"
	}
	statusStyle := mutedStyle
	if m.status == "working" {
		statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	} else if m.status == "error" {
		statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	}
	footer := statusStyle.Render(m.status) + mutedStyle.Render("  ·  ↑/↓ scroll  pgup/pgdn page  ctrl+c quit")
	return titleStyle.Render("ruga  ·  Codex App Server") + "\n" + m.viewport.View() + "\n" + m.input.View() + "\n" + footer
}

func (m *model) add(ev event.Event) {
	switch ev.Kind {
	case "command.started":
		ev.Data = map[string]any{"output": map[string]string{}, "outputBytes": 0}
		ev.Raw = nil
		m.events = append(m.events, ev)
	case "command.output":
		if index := m.findEvent(ev.ItemID, "command.started", "command.completed"); index >= 0 {
			m.addCommandOutput(&m.events[index], ev)
		}
	case "command.completed":
		if index := m.findEvent(ev.ItemID, "command.started", "command.completed"); index >= 0 {
			entry := &m.events[index]
			if outputBytes(*entry) == 0 {
				if item := nestedItem(ev); item != nil {
					if output, ok := item["aggregatedOutput"].(string); ok {
						m.appendOutput(entry, "stdout", output)
					}
				}
			}
			entry.Kind, entry.Summary = ev.Kind, ev.Summary
			entry.Raw = nil
		} else {
			ev.Data = map[string]any{"output": map[string]string{}, "outputBytes": 0}
			ev.Raw = nil
			m.events = append(m.events, ev)
		}
	case "tool.started":
		ev.Raw = nil
		ev.Data = nil
		m.events = append(m.events, ev)
	case "tool.progress":
		if index := m.findEvent(ev.ItemID, "tool.started", "tool.completed"); index >= 0 && ev.Summary != "" {
			m.events[index].Data = map[string]any{"progress": compact(ev.Summary, 240)}
		}
	case "tool.completed":
		if index := m.findEvent(ev.ItemID, "tool.started", "tool.completed"); index >= 0 {
			entry := &m.events[index]
			entry.Kind, entry.Summary = ev.Kind, ev.Summary
			entry.Raw, entry.Data = nil, nil
		} else {
			ev.Raw, ev.Data = nil, nil
			m.events = append(m.events, ev)
		}
	case "message.delta":
		if index := m.findEvent(ev.ItemID, "message.delta"); index >= 0 {
			m.events[index].Summary += ev.Summary
			m.events[index].Timestamp = ev.Timestamp
		} else {
			ev.Raw, ev.Data = nil, nil
			m.events = append(m.events, ev)
		}
	case "status.update":
		if ev.ItemID != "" {
			if index := m.findEvent(ev.ItemID, "status.update"); index >= 0 {
				m.events[index].Summary += ev.Summary
				m.events[index].Timestamp = ev.Timestamp
				return
			}
		}
		ev.Data = nil
		ev.Raw = nil
		m.events = append(m.events, ev)
	case "file.changed":
		ev.Raw, ev.Data = nil, nil
		if index := m.findEvent(ev.ItemID, "file.changed"); index >= 0 {
			m.events[index].Summary = ev.Summary
		} else {
			m.events = append(m.events, ev)
		}
	case "file.output", "usage.updated":
		ev.Raw = nil
		if ev.Kind == "file.output" {
			ev.Data = nil
		}
		m.events = append(m.events, ev)
	default:
		if ev.Kind == "backend.unknown" {
			if len(ev.Raw) > 240 {
				ev.Raw = append(ev.Raw[:240:240], []byte("…")...)
			}
			ev.Data = nil
		}
		m.events = append(m.events, ev)
	}
}

func (m *model) findEvent(itemID string, kinds ...string) int {
	if itemID == "" {
		return -1
	}
	for i := len(m.events) - 1; i >= 0; i-- {
		if m.events[i].ItemID != itemID {
			continue
		}
		for _, kind := range kinds {
			if m.events[i].Kind == kind {
				return i
			}
		}
	}
	return -1
}

func (m *model) addCommandOutput(entry *event.Event, output event.Event) {
	stream := "stdout"
	if value, ok := output.Data["stream"].(string); ok && value != "" {
		stream = value
	}
	m.appendOutput(entry, stream, output.Summary)
}

func (m *model) appendOutput(entry *event.Event, stream, value string) {
	value = safeTerminalText(value)
	data := entry.Data
	if data == nil {
		data = map[string]any{}
	}
	streams, _ := data["output"].(map[string]string)
	if streams == nil {
		streams = map[string]string{}
	}
	used, _ := data["outputBytes"].(int)
	remaining := maxCommandOutputBytes - used
	accepted := 0
	if remaining > 0 {
		prefix := utf8Prefix(value, remaining)
		streams[stream] += prefix
		accepted = len(prefix)
		used += accepted
	}
	data["output"], data["outputBytes"] = streams, used
	if accepted < len(value) {
		data["omittedBytes"] = omittedOutputBytes(data) + len(value) - accepted
	}
	entry.Data = data
}

func outputBytes(entry event.Event) int {
	value, _ := entry.Data["outputBytes"].(int)
	return value
}

func omittedOutputBytes(data map[string]any) int {
	value, _ := data["omittedBytes"].(int)
	return value
}

func nestedItem(ev event.Event) map[string]any {
	item, _ := ev.Data["item"].(map[string]any)
	return item
}

func safeTerminalText(value string) string {
	value = strings.ToValidUTF8(value, "�")
	var safe strings.Builder
	for _, r := range value {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			safe.WriteRune(r)
		} else {
			safe.WriteRune('�')
		}
	}
	return safe.String()
}

func utf8Prefix(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func (m *model) refresh(follow bool) {
	if !m.ready {
		return
	}
	rows := make([]string, 0, len(m.events))
	for _, ev := range m.events {
		timestamp := ev.Timestamp.Local().Format("15:04:05")
		detail := strings.TrimSpace(ev.Summary)
		if detail == "" {
			detail = ev.Source
		}
		if ev.Kind == "backend.unknown" && len(ev.Raw) > 0 {
			detail = fmt.Sprintf("%s  %s", detail, compact(string(ev.Raw), 240))
		}
		line := fmt.Sprintf("%s  %-18s %s", timestamp, ev.Kind, detail)
		if ev.Kind == "command.started" || ev.Kind == "command.completed" {
			if streams, ok := ev.Data["output"].(map[string]string); ok {
				for _, name := range []string{"stdout", "stderr"} {
					if output := strings.TrimRight(streams[name], "\n"); output != "" {
						line += "\n          " + mutedStyle.Render(name+":") + "\n" + indent(output, "            ")
					}
				}
			}
			if omitted := omittedOutputBytes(ev.Data); omitted > 0 {
				line += fmt.Sprintf("\n          %s", mutedStyle.Render(fmt.Sprintf("… %d output bytes omitted (limit %d)", omitted, maxCommandOutputBytes)))
			}
		}
		if ev.Kind == "tool.started" {
			if progress, ok := ev.Data["progress"].(string); ok {
				line += "\n          " + mutedStyle.Render(progress)
			}
		}
		if ev.ThreadID != "" && ev.Kind == "thread.started" {
			line += "\n          " + mutedStyle.Render("thread "+ev.ThreadID)
		}
		rows = append(rows, line)
	}
	m.viewport.SetContent(strings.Join(rows, "\n\n"))
	if follow {
		m.viewport.GotoBottom()
	}
}

func indent(value, prefix string) string {
	lines := strings.Split(value, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

func compact(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	return utf8Prefix(value, limit) + "…"
}

func waitBatch(stream <-chan []event.Event) tea.Cmd {
	return func() tea.Msg {
		batch, ok := <-stream
		if !ok {
			return tea.QuitMsg{}
		}
		return batchMsg(batch)
	}
}

func submitPrompt(ctx context.Context, submit func(context.Context, string) error, prompt string) tea.Cmd {
	return func() tea.Msg {
		if submit == nil {
			return submitResultMsg{err: fmt.Errorf("Codex backend is unavailable")}
		}
		return submitResultMsg{err: submit(ctx, prompt)}
	}
}

func batchEvents(ctx context.Context, input <-chan event.Event) <-chan []event.Event {
	output := make(chan []event.Event, 1)
	go func() {
		defer close(output)
		var pending []event.Event
		var timer *time.Timer
		var timerC <-chan time.Time
		flush := func() bool {
			if len(pending) == 0 {
				return true
			}
			batch := pending
			pending = nil
			select {
			case output <- batch:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-input:
				if !ok {
					flush()
					return
				}
				pending = append(pending, ev)
				if timer == nil {
					timer = time.NewTimer(40 * time.Millisecond)
					timerC = timer.C
				}
			case <-timerC:
				if !flush() {
					return
				}
				timerC = nil
				timer = nil
			}
		}
	}()
	return output
}
