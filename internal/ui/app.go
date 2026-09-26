package ui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/haha-systems/ruga/internal/event"
)

type batchMsg []event.Event

type submitResultMsg struct{ err error }
type approvalResultMsg struct {
	requestID string
	decision  event.ApprovalDecision
	err       error
}
type interruptResultMsg struct{ err error }

type Config struct {
	Project  string
	Branch   string
	Backend  string
	Model    string
	ReadOnly bool
}

type Actions struct {
	ResolveApproval func(context.Context, string, event.ApprovalDecision) error
	Interrupt       func(context.Context) error
}

type focusTarget uint8

const (
	focusComposer focusTarget = iota
	focusTimeline
	focusApproval
)

type model struct {
	viewport     viewport.Model
	input        textinput.Model
	stream       <-chan []event.Event
	submit       func(context.Context, string) error
	actions      Actions
	ctx          context.Context
	events       []event.Event
	approvals    []event.ApprovalRequest
	submitting   map[string]bool
	status       string
	notice       string
	project      string
	branch       string
	backend      string
	modelName    string
	usage        string
	search       textinput.Model
	searchActive bool
	focus        focusTarget
	turnActive   bool
	interrupting bool
	readOnly     bool
	width        int
	height       int
	ready        bool
}

const maxCommandOutputBytes = 4096

var (
	titleStyle         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	mutedStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	approvalStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	resolvedStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	rejectedStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	approvalPanelStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("3")).Padding(0, 1)
	userStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	assistantStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	errorStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	commandStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
)

func Run(ctx context.Context, events <-chan event.Event, submit func(context.Context, string) error, actions Actions, configs ...Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	input := textinput.New()
	input.Prompt = "› "
	input.Placeholder = "Message Codex and press Enter"
	input.Focus()
	search := textinput.New()
	search.Prompt = "/ "
	search.Placeholder = "filter timeline"
	config := Config{}
	if len(configs) > 0 {
		config = configs[0]
	}
	if config.ReadOnly {
		input.Placeholder = "Replay is read-only"
		input.Blur()
	}
	focus := focusComposer
	if config.ReadOnly {
		focus = focusTimeline
	}
	program := tea.NewProgram(model{
		stream: batchEvents(ctx, events), input: input, submit: submit, actions: actions,
		ctx: ctx, status: "idle", focus: focus, submitting: make(map[string]bool),
		project: config.Project, branch: config.Branch, backend: config.Backend,
		modelName: config.Model, search: search, readOnly: config.ReadOnly,
	}, tea.WithContext(ctx), tea.WithAltScreen())
	_, err := program.Run()
	return err
}

func (m model) Init() tea.Cmd { return tea.Batch(waitBatch(m.stream), textinput.Blink) }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		follow := !m.ready || m.viewport.AtBottom()
		m.resize(msg.Width, msg.Height)
		m.refresh(follow)
	case batchMsg:
		follow := !m.ready || m.viewport.AtBottom()
		for _, ev := range msg {
			m.add(ev)
			switch ev.Kind {
			case "turn.started":
				m.status = "working"
				m.turnActive = true
			case "turn.completed":
				m.status = "idle"
				m.turnActive = false
				m.interrupting = false
				if status, _ := ev.Data["status"].(string); status == "failed" {
					m.status = "error"
				} else if status == "interrupted" || status == "cancelled" || status == "canceled" {
					m.status = "interrupted"
					m.finishInterruptedItems(ev.TurnID)
				}
			case "error":
				m.status = "error"
				m.turnActive = false
				m.interrupting = false
			}
		}
		if m.ready {
			m.resize(m.width, m.height)
		}
		m.refresh(follow)
		return m, waitBatch(m.stream)
	case submitResultMsg:
		if msg.err != nil {
			m.status = "error"
			m.turnActive = false
		}
		return m, nil
	case approvalResultMsg:
		if msg.err != nil {
			delete(m.submitting, msg.requestID)
			m.status = "error"
		}
		return m, nil
	case interruptResultMsg:
		m.interrupting = false
		if msg.err != nil {
			m.status = "error"
		} else if m.turnActive {
			m.status = "interrupting"
		} else {
			m.status = "idle"
		}
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m model) View() string {
	if !m.ready {
		return "Starting Codex App Server…"
	}
	header := m.headerLine()
	footer := mutedStyle.Render(fitLine(m.footerKeys(), max(1, m.width)))
	if m.notice != "" {
		footer = mutedStyle.Render(fitLine(m.notice+"  ·  "+m.footerKeys(), max(1, m.width)))
	}
	parts := []string{header}
	if m.searchActive || m.search.Value() != "" {
		parts = append(parts, m.search.View())
	}
	if panel := m.approvalPanel(); panel != "" {
		parts = append(parts, panel)
	}
	parts = append(parts, m.viewport.View(), m.input.View(), footer)
	return strings.Join(parts, "\n")
}

func (m *model) resize(width, height int) {
	m.width, m.height = max(1, width), max(1, height)
	m.input.Width = max(1, width-lipgloss.Width(m.input.Prompt)-2)
	m.search.Width = max(1, width-lipgloss.Width(m.search.Prompt)-2)
	panelHeight := 0
	if panel := m.approvalPanel(); panel != "" {
		panelHeight = lipgloss.Height(panel) + 1
	}
	chromeHeight := 3 // header, composer, footer
	if m.searchActive || m.search.Value() != "" {
		chromeHeight++
	}
	viewportHeight := max(1, height-chromeHeight-panelHeight)
	if !m.ready {
		m.viewport = viewport.New(max(1, width), viewportHeight)
		m.ready = true
		return
	}
	m.viewport.Width = max(1, width)
	m.viewport.Height = viewportHeight
}

func (m model) footerKeys() string {
	copyKeys := " · / filter"
	switch m.focus {
	case focusApproval:
		return "y/enter accept · n/esc reject · tab focus · ctrl+x interrupt · ctrl+c quit" + copyKeys
	case focusTimeline:
		return "↑/↓ scroll · pgup/pgdn page · g/G top/bottom · c copy · tab composer · ctrl+x interrupt · ctrl+c quit" + copyKeys
	default:
		if m.readOnly {
			return "replay · tab timeline · ctrl+c quit" + copyKeys
		}
		return "enter send · tab timeline · ctrl+x interrupt · ctrl+c quit" + copyKeys
	}
}

func (m model) headerLine() string {
	parts := []string{}
	if m.project != "" {
		parts = append(parts, m.project)
	}
	if m.branch != "" {
		parts = append(parts, "git:"+m.branch)
	}
	if m.backend != "" {
		parts = append(parts, m.backend)
	}
	if m.modelName != "" {
		parts = append(parts, m.modelName)
	}
	if m.usage != "" {
		parts = append(parts, m.usage)
	}
	if len(parts) == 0 {
		parts = append(parts, "ruga")
	}
	statusStyle := mutedStyle
	if m.status == "working" || m.status == "interrupting" {
		statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	} else if m.status == "error" {
		statusStyle = errorStyle
	}
	state := "● " + m.status
	if m.width <= lipgloss.Width(state)+3 {
		return statusStyle.Render(fitLine(state, m.width))
	}
	metadataWidth := max(1, m.width-lipgloss.Width(state)-3)
	metadata := titleStyle.Render(fitLine(strings.Join(parts, "  ·  "), metadataWidth))
	return metadata + mutedStyle.Render("  ·  ") + statusStyle.Render(state)
}

func (m model) approvalPanel() string {
	if len(m.approvals) == 0 {
		return ""
	}
	request := m.approvals[0]
	width := max(1, m.width-4)
	lineWidth := max(1, width-2)
	header := fmt.Sprintf("⚠ APPROVAL REQUIRED [%d/%d] · %s", 1, len(m.approvals), strings.ToUpper(strings.ReplaceAll(request.Kind, "_", " ")))
	detail := approvalDetail(request)
	reason := request.Reason
	if reason == "" {
		reason = "No reason supplied"
	}
	keys := "y/enter accept · n/esc reject · tab to change focus"
	if m.submitting[request.RequestID] {
		keys = "Sending decision…"
	}
	lines := []string{fitLine(header, lineWidth)}
	lines = append(lines, wrapLine("details: "+detail, lineWidth)...)
	lines = append(lines, wrapLine("reason: "+reason, lineWidth)...)
	lines = append(lines, fitLine(keys, lineWidth))
	return approvalPanelStyle.Width(width).Render(strings.Join(lines, "\n"))
}

func wrapLine(value string, width int) []string {
	value = safeTerminalText(strings.Join(strings.Fields(value), " "))
	width = max(1, width)
	var lines []string
	var line strings.Builder
	used := 0
	for _, r := range value {
		cellWidth := lipgloss.Width(string(r))
		if used+cellWidth > width && line.Len() > 0 {
			lines = append(lines, line.String())
			line.Reset()
			used = 0
			if r == ' ' {
				continue
			}
		}
		line.WriteRune(r)
		used += cellWidth
	}
	if line.Len() > 0 {
		lines = append(lines, line.String())
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func approvalDetail(request event.ApprovalRequest) string {
	switch request.Kind {
	case "command":
		detail := "command: " + request.Command
		if request.CWD != "" {
			detail += " · cwd: " + request.CWD
		}
		if request.Details != "" && request.Details != "null" {
			detail += " · actions: " + request.Details
		}
		return detail
	case "file_change":
		detail := "files: " + strings.Join(request.FilePaths, ", ")
		if request.GrantRoot != "" {
			detail += " · root: " + request.GrantRoot
		}
		return detail
	case "mcp_tool":
		return "tool: " + request.Tool + " · input: " + request.Details
	case "permissions":
		detail := "permissions: " + strings.Join(request.Permissions, ", ")
		if request.Scope != "" {
			detail += " · scope: " + request.Scope
		}
		return detail
	default:
		return "Review this action before it continues"
	}
}

func fitLine(value string, width int) string {
	value = safeTerminalText(strings.Join(strings.Fields(value), " "))
	if lipgloss.Width(value) <= width {
		return value
	}
	var result strings.Builder
	used := 0
	for _, r := range value {
		cellWidth := lipgloss.Width(string(r))
		if used+cellWidth > max(0, width-1) {
			break
		}
		result.WriteRune(r)
		used += cellWidth
	}
	if width > 0 {
		result.WriteRune('…')
	}
	return result.String()
}

func (m *model) setFocus(target focusTarget) {
	if m.readOnly && target == focusComposer {
		target = focusTimeline
	}
	if target == focusApproval && len(m.approvals) == 0 {
		target = focusComposer
	}
	m.focus = target
	if target == focusComposer {
		m.input.Focus()
	} else {
		m.input.Blur()
	}
}

func (m *model) nextFocus() {
	switch m.focus {
	case focusComposer:
		m.setFocus(focusTimeline)
	case focusTimeline:
		if len(m.approvals) > 0 {
			m.setFocus(focusApproval)
		} else {
			m.setFocus(focusComposer)
		}
	default:
		m.setFocus(focusComposer)
	}
}

func (m model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyCtrlX:
		if m.readOnly || !m.turnActive || m.interrupting {
			return m, nil
		}
		m.interrupting = true
		m.status = "interrupting"
		return m, interruptTurn(m.ctx, m.actions.Interrupt)
	case tea.KeyTab:
		if m.searchActive {
			m.searchActive = false
			m.search.Blur()
			m.resizeIfReady()
			return m, nil
		}
		m.nextFocus()
		return m, nil
	case tea.KeyEsc:
		if m.searchActive {
			m.search.SetValue("")
			m.searchActive = false
			m.search.Blur()
			m.resizeIfReady()
			return m, nil
		}
		if m.search.Value() != "" && m.focus == focusTimeline {
			m.search.SetValue("")
			m.resizeIfReady()
			return m, nil
		}
		switch m.focus {
		case focusApproval:
			return m.decideActiveApproval(event.ApprovalReject)
		case focusTimeline:
			m.setFocus(focusComposer)
		case focusComposer:
			if m.input.Value() != "" {
				m.input.Reset()
			}
		}
		return m, nil
	}
	if m.searchActive {
		if msg.Type == tea.KeyEnter {
			m.searchActive = false
			m.search.Blur()
			m.resizeIfReady()
			return m, nil
		}
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		m.resizeIfReady()
		return m, cmd
	}

	if m.focus == focusApproval {
		key := strings.ToLower(msg.String())
		if msg.Type == tea.KeyEnter || msg.Type == tea.KeyRunes && key == "y" {
			return m.decideActiveApproval(event.ApprovalAccept)
		}
		if msg.Type == tea.KeyRunes && key == "n" {
			return m.decideActiveApproval(event.ApprovalReject)
		}
		return m, nil
	}
	if m.focus == focusTimeline {
		if msg.Type == tea.KeyRunes {
			switch msg.String() {
			case "/":
				m.searchActive = true
				m.search.Focus()
				m.resizeIfReady()
				return m, textinput.Blink
			case "c":
				if err := clipboard.WriteAll(m.copyTimeline()); err != nil {
					m.notice = "copy unavailable"
				} else {
					m.notice = "timeline copied"
				}
				return m, nil
			}
		}
		switch msg.Type {
		case tea.KeyUp:
			m.viewport.LineUp(1)
		case tea.KeyDown:
			m.viewport.LineDown(1)
		case tea.KeyPgUp:
			m.viewport.PageUp()
		case tea.KeyPgDown:
			m.viewport.PageDown()
		case tea.KeyHome:
			m.viewport.GotoTop()
		case tea.KeyEnd:
			m.viewport.GotoBottom()
		case tea.KeyEnter:
			m.setFocus(focusComposer)
		case tea.KeyRunes:
			if msg.String() == "g" {
				m.viewport.GotoTop()
			} else if msg.String() == "G" {
				m.viewport.GotoBottom()
			}
		}
		return m, nil
	}
	if msg.Type == tea.KeyEnter {
		if m.readOnly {
			return m, nil
		}
		prompt := strings.TrimSpace(m.input.Value())
		if prompt == "" || m.turnActive || m.interrupting {
			return m, nil
		}
		m.input.Reset()
		m.status = "working"
		m.turnActive = true
		return m, submitPrompt(m.ctx, m.submit, prompt)
	}
	if m.readOnly {
		return m, nil
	}
	var inputCmd tea.Cmd
	m.input, inputCmd = m.input.Update(msg)
	return m, inputCmd
}

func (m model) decideActiveApproval(decision event.ApprovalDecision) (tea.Model, tea.Cmd) {
	if len(m.approvals) == 0 {
		return m, nil
	}
	requestID := m.approvals[0].RequestID
	if m.submitting[requestID] {
		return m, nil
	}
	if m.submitting == nil {
		m.submitting = make(map[string]bool)
	}
	m.submitting[requestID] = true
	m.resizeIfReady()
	return m, resolveApproval(m.ctx, m.actions.ResolveApproval, requestID, decision)
}

func (m *model) resizeIfReady() {
	if m.ready {
		m.resize(m.width, m.height)
		m.refresh(false)
	}
}

func (m *model) add(ev event.Event) {
	if ev.Backend != "" && ev.Backend != "app" {
		if m.backend == "" {
			m.backend = strings.ToUpper(ev.Backend[:1]) + ev.Backend[1:]
		}
	}
	if name := eventModel(ev); name != "" {
		m.modelName = name
	}
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
				if m.events[index].Summary != "" && ev.Summary != "" {
					m.events[index].Summary += " · "
				}
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
		} else {
			m.usage = compactUsage(ev.Summary)
			for index := len(m.events) - 1; index >= 0; index-- {
				if m.events[index].Kind == "usage.updated" {
					m.events[index] = ev
					return
				}
			}
		}
		m.events = append(m.events, ev)
	case "approval.requested":
		ev.Raw, ev.Data = nil, nil
		if !m.readOnly && ev.Approval != nil && !m.hasApproval(ev.Approval.RequestID) {
			m.approvals = append(m.approvals, *ev.Approval)
		}
		m.events = append(m.events, ev)
		if !m.readOnly && ev.Approval != nil {
			m.setFocus(focusApproval)
		}
	case "approval.resolved":
		ev.Raw, ev.Data = nil, nil
		if ev.Approval != nil {
			m.removeApproval(ev.Approval.RequestID)
			delete(m.submitting, ev.Approval.RequestID)
		}
		m.events = append(m.events, ev)
		if len(m.approvals) == 0 {
			m.setFocus(focusComposer)
		} else if m.focus == focusApproval {
			m.setFocus(focusApproval)
		}
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

func (m model) hasApproval(requestID string) bool {
	for _, approval := range m.approvals {
		if approval.RequestID == requestID {
			return true
		}
	}
	return false
}

func (m *model) removeApproval(requestID string) {
	for i := range m.approvals {
		if m.approvals[i].RequestID == requestID {
			m.approvals = append(m.approvals[:i], m.approvals[i+1:]...)
			return
		}
	}
}

func (m *model) finishInterruptedItems(turnID string) {
	if turnID == "" {
		return
	}
	for i := range m.events {
		entry := &m.events[i]
		if entry.TurnID != turnID {
			continue
		}
		switch entry.Kind {
		case "command.started":
			entry.Kind = "command.completed"
			entry.Summary += " · interrupted"
		case "tool.started":
			entry.Kind = "tool.completed"
			entry.Summary += " · interrupted"
			entry.Data = nil
		}
	}
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
		if !matchesFilter(ev, m.search.Value()) {
			continue
		}
		timestamp := ev.Timestamp.Local().Format("15:04:05")
		detail := strings.TrimSpace(ev.Summary)
		if detail == "" {
			detail = ev.Source
		}
		if ev.Kind == "backend.unknown" && len(ev.Raw) > 0 {
			detail = fmt.Sprintf("%s  %s", detail, compact(string(ev.Raw), 240))
		}
		line := fmt.Sprintf("%s  %-18s %s", timestamp, ev.Kind, detail)
		style := eventStyle(ev.Kind)
		line = style.Render(line)
		switch ev.Kind {
		case "approval.requested":
			approval := "action details unavailable"
			if ev.Approval != nil {
				approval = approvalDetail(*ev.Approval)
			}
			line = approvalStyle.Render(fmt.Sprintf("%s  ⚠ APPROVAL REQUIRED · %s", timestamp, fitLine(approval, max(1, m.viewport.Width-30))))
		case "approval.resolved":
			decision := string(ev.Decision)
			style := resolvedStyle
			if ev.Decision == event.ApprovalReject {
				style = rejectedStyle
			}
			line = style.Render(fmt.Sprintf("%s  APPROVAL %s", timestamp, strings.ToUpper(decision)))
		}
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

func (m model) copyTimeline() string {
	var rows []string
	for _, ev := range m.events {
		if !matchesFilter(ev, m.search.Value()) {
			continue
		}
		line := strings.TrimSpace(ev.Kind + "  " + ev.Summary)
		if ev.Kind == "approval.requested" && ev.Approval != nil {
			line += "\n" + approvalDetail(*ev.Approval)
		}
		if streams, ok := ev.Data["output"].(map[string]string); ok {
			for _, name := range []string{"stdout", "stderr"} {
				if value := strings.TrimSpace(streams[name]); value != "" {
					line += "\n" + name + ":\n" + value
				}
			}
		}
		rows = append(rows, line)
	}
	return strings.Join(rows, "\n\n")
}

func matchesFilter(ev event.Event, query string) bool {
	query = strings.TrimSpace(query)
	if query == "" {
		return true
	}
	parts := []string{ev.Kind, ev.Summary, ev.Source}
	if ev.Approval != nil {
		parts = append(parts, approvalDetail(*ev.Approval), ev.Approval.Reason)
	}
	if streams, ok := ev.Data["output"].(map[string]string); ok {
		parts = append(parts, streams["stdout"], streams["stderr"])
	}
	return strings.Contains(strings.ToLower(strings.Join(parts, " ")), strings.ToLower(query))
}

func eventStyle(kind string) lipgloss.Style {
	switch kind {
	case "user.message":
		return userStyle
	case "message.delta", "message.completed":
		return assistantStyle
	case "command.started", "command.completed":
		return commandStyle
	case "error", "warning", "backend.unknown":
		return errorStyle
	default:
		return lipgloss.NewStyle()
	}
}

func eventModel(ev event.Event) string {
	for _, key := range []string{"model", "modelName", "model_name", "toModel"} {
		if value, ok := ev.Data[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func compactUsage(value string) string {
	value = strings.ReplaceAll(value, " total tokens", " tokens")
	value = strings.ReplaceAll(value, "context ", "ctx ")
	return strings.TrimSpace(value)
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

func resolveApproval(ctx context.Context, resolve func(context.Context, string, event.ApprovalDecision) error, requestID string, decision event.ApprovalDecision) tea.Cmd {
	return func() tea.Msg {
		if resolve == nil {
			return approvalResultMsg{requestID: requestID, decision: decision, err: fmt.Errorf("approval handling is unavailable")}
		}
		return approvalResultMsg{requestID: requestID, decision: decision, err: resolve(ctx, requestID, decision)}
	}
}

func interruptTurn(ctx context.Context, interrupt func(context.Context) error) tea.Cmd {
	return func() tea.Msg {
		if interrupt == nil {
			return interruptResultMsg{err: fmt.Errorf("interrupt is unavailable")}
		}
		return interruptResultMsg{err: interrupt(ctx)}
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
