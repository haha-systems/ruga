package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/xiy/ruga/internal/event"
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
	// Keep streamed text updates readable without retaining a redraw-sized row
	// for every protocol delta.
	if ev.Kind == "message.delta" && len(m.events) > 0 {
		last := &m.events[len(m.events)-1]
		if last.Kind == ev.Kind && last.ItemID == ev.ItemID {
			last.Summary += ev.Summary
			last.Timestamp = ev.Timestamp
			return
		}
	}
	m.events = append(m.events, ev)
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

func compact(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
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
