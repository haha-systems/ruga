package ui

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/harmonica"
	"github.com/charmbracelet/lipgloss"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/haha-systems/ruga/internal/event"
	"github.com/haha-systems/ruga/internal/presentation"
)

type (
	batchMsg         []event.Event
	streamClosedMsg  struct{}
	panelFrameMsg    struct{ generation int }
	activityFrameMsg struct{ generation int }
)

type (
	submitResultMsg   struct{ err error }
	approvalResultMsg struct {
		requestID string
		decision  event.ApprovalDecision
		err       error
	}
)
type interruptResultMsg struct{ err error }

type Config struct {
	Project  string
	Branch   string
	Backend  string
	Model    string
	Panel    PanelPlacement
	ReadOnly bool
}

type PanelPlacement string

const (
	PanelRight  PanelPlacement = "right"
	PanelLeft   PanelPlacement = "left"
	PanelBottom PanelPlacement = "bottom"
	PanelTop    PanelPlacement = "top"
)

type Actions struct {
	ResolveApproval func(context.Context, string, event.ApprovalDecision) error
	Interrupt       func(context.Context) error
}

type focusTarget uint8

const (
	focusComposer focusTarget = iota
	focusTimeline
	focusTelemetry
	focusApproval
)

type model struct {
	viewport           viewport.Model
	telemetryViewport  viewport.Model
	input              textinput.Model
	stream             <-chan []event.Event
	submit             func(context.Context, string) error
	actions            Actions
	ctx                context.Context
	presentation       presentation.Model
	approvals          []event.ApprovalRequest
	submitting         map[string]bool
	status             string
	notice             string
	project            string
	branch             string
	backend            string
	modelName          string
	usage              string
	search             textinput.Model
	searchActive       bool
	focus              focusTarget
	turnActive         bool
	interrupting       bool
	readOnly           bool
	width              int
	height             int
	ready              bool
	showTelemetry      bool
	selectedTelemetry  int
	expandedTelemetry  map[int]bool
	telemetryRows      map[int][2]int
	telemetryFollowing bool
	panelPosition      float64
	panelVelocity      float64
	panelWidth         int
	panelHeight        int
	panelPlacement     PanelPlacement
	panelGeneration    int
	panelAnimating     bool
	unseenTelemetry    int
	activityFrame      int
	activityGeneration int
	activityTicking    bool
	theme              Theme
}

var panelSpring = harmonica.NewSpring(harmonica.FPS(30), 18, 1)

func Run(ctx context.Context, events <-chan event.Event, submit func(context.Context, string) error, actions Actions, configs ...Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	input := textinput.New()
	input.Prompt = "› "
	input.Placeholder = "Message Codex and press Enter"
	input.Focus()
	search := textinput.New()
	search.Prompt = "/ "
	search.Placeholder = "filter events"
	config := Config{}
	if len(configs) > 0 {
		config = configs[0]
	}

	if config.ReadOnly {
		input.Placeholder = "Replay is read-only"
		input.Blur()
	}

	if !validPanelPlacement(config.Panel) {
		config.Panel = PanelRight
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
		panelPlacement:    config.Panel,
		selectedTelemetry: -1, telemetryFollowing: true,
		theme: defaultTheme,
	}, tea.WithContext(ctx), tea.WithAltScreen())
	_, err := program.Run()
	return err
}

func (m model) Init() tea.Cmd { return tea.Batch(waitBatch(m.stream, m.readOnly), textinput.Blink) }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		follow := !m.ready || m.viewport.AtBottom()
		followTelemetry := m.telemetryFollowing && (!m.ready || m.telemetryViewport.AtBottom())
		if msg.Width < 70 {
			m.panelPosition, m.panelVelocity, m.panelWidth, m.panelHeight = 0, 0, 0, 0
			m.panelAnimating = false
		} else if m.showTelemetry && !m.panelAnimating {
			m.setPanelSize(m.desiredPanelSize())
			m.panelPosition = float64(m.desiredPanelSize())
		}

		m.resize(msg.Width, msg.Height)
		m.refreshViews(follow, followTelemetry)

	case panelFrameMsg:
		if msg.generation != m.panelGeneration || !m.panelAnimating {
			return m, nil
		}

		follow := !m.ready || m.viewport.AtBottom()
		followTelemetry := m.telemetryFollowing && (!m.ready || m.telemetryViewport.AtBottom())
		target := 0.0
		if m.showTelemetry {
			target = float64(m.desiredPanelSize())
		}

		m.panelPosition, m.panelVelocity = panelSpring.Update(m.panelPosition, m.panelVelocity, target)
		m.panelPosition = math.Max(0, math.Min(float64(m.desiredPanelSize()), m.panelPosition))
		if math.Abs(m.panelPosition-target) < 0.5 && math.Abs(m.panelVelocity) < 1 {
			m.panelPosition, m.panelVelocity = target, 0
			m.panelAnimating = false
		}

		m.setPanelSize(int(math.Round(m.panelPosition)))
		m.resize(m.width, m.height)
		m.refreshViews(follow, followTelemetry)
		if m.panelAnimating {
			return m, nextPanelFrame(m.panelGeneration)
		}

		if m.showTelemetry && m.telemetryFollowing && m.telemetryViewport.AtBottom() {
			m.unseenTelemetry = 0
		}

		return m, nil

	case activityFrameMsg:
		if msg.generation != m.activityGeneration || !m.activityTicking || m.presentation.Activity.ActiveTools == 0 {
			return m, nil
		}

		m.activityFrame = (m.activityFrame + 1) % 4
		m.refreshViews(false, false)
		return m, nextActivityFrame(m.activityGeneration)

	case batchMsg:
		follow := !m.ready || m.viewport.AtBottom()
		followTelemetry := m.telemetryFollowing && (!m.ready || m.telemetryViewport.AtBottom())
		previousActivity := m.presentation.Activity.TurnStatus
		previousTelemetry := m.presentation.Activity.TelemetryEvents
		for _, ev := range msg {
			m.add(ev)
		}

		if added := m.presentation.Activity.TelemetryEvents - previousTelemetry; added > 0 && (!m.panelVisible() || !m.telemetryFollowing) {
			m.unseenTelemetry += added
		}

		if activity := m.presentation.Activity.TurnStatus; activity != "" && activity != previousActivity {
			m.turnActive = activity == "working"
			if !m.turnActive {
				m.interrupting = false
			}

			if !m.interrupting {
				m.status = activity
			}
		}

		if m.ready {
			m.resize(m.width, m.height)
		}

		m.refreshViews(follow, followTelemetry)
		wait := waitBatch(m.stream, m.readOnly)
		if m.presentation.Activity.ActiveTools > 0 && !m.activityTicking {
			m.activityTicking = true
			m.activityGeneration++
			return m, tea.Batch(wait, nextActivityFrame(m.activityGeneration))
		}

		if m.presentation.Activity.ActiveTools == 0 && m.activityTicking {
			m.activityTicking = false
			m.activityGeneration++
		}

		return m, wait

	case streamClosedMsg:
		m.notice = "Replay complete"
		return m, nil

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
	footer := m.style(presentation.RoleMuted).Render(fitLine(m.footerKeys(), max(1, m.width)))
	if m.notice != "" {
		footer = m.style(presentation.RoleMuted).Render(fitLine(m.notice+"  ·  "+m.footerKeys(), max(1, m.width)))
	}

	parts := []string{header}

	if panel := m.approvalPanel(); panel != "" {
		parts = append(parts, panel)
	}

	parts = append(parts,
		m.viewPanels(),
		renderPanel(m.activeTheme(), m.input.View(), m.width, m.focus == focusComposer),
		footer,
	)
	view := m.activeTheme().Canvas.Width(m.width).Height(m.height).Render(strings.Join(parts, "\n"))
	if lipgloss.Height(view) > m.height {
		return m.compactView()
	}

	return view
}

func (m model) compactView() string {
	header := m.headerLine()
	footer := m.style(presentation.RoleMuted).Render(fitLine(m.footerKeys(), m.width))
	input := m.input.View()
	if m.notice != "" {
		footer = m.style(presentation.RoleMuted).Render(fitLine(m.notice+"  ·  "+m.footerKeys(), m.width))
	}

	body := m.viewport.View()
	switch m.focus {
	case focusApproval:
		if len(m.approvals) > 0 {
			request := m.approvals[0]
			body = fitLine("⚠ APPROVAL REQUIRED · "+approvalDetail(request), m.width)
		}

	case focusTelemetry:
		body = m.compactInspector()
	default:
		if m.width < 70 && m.showTelemetry {
			body = m.compactInspector()
		}
	}

	bodyRows := max(0, m.height-3)
	lines := strings.Split(body, "\n")
	if len(lines) > bodyRows {
		lines = lines[:bodyRows]
	}

	parts := []string{header}
	parts = append(parts, lines...)
	parts = append(parts, input, footer)
	return m.activeTheme().Canvas.Width(m.width).Height(m.height).Render(strings.Join(parts, "\n"))
}

func (m model) compactInspector() string {
	parts := []string{m.style(presentation.RoleMuted).Render(fitLine(m.inspectorHeading(), m.width))}
	if m.searchActive || m.search.Value() != "" {
		parts = append(parts, m.search.View())
	}

	parts = append(parts, m.telemetryViewport.View())
	return strings.Join(parts, "\n")
}

func (m model) viewPanels() string {
	if !m.panelVisible() {
		return m.conversationRegion(m.width)
	}

	if m.width < 70 {
		return m.inspectorRegion(m.width)
	}

	conversationWidth, inspectorWidth := m.width, m.width
	if m.panelWidth > 0 {
		conversationWidth, inspectorWidth = m.viewWidths()
	}

	conversation := m.conversationRegion(conversationWidth)
	inspector := m.inspectorRegion(inspectorWidth)
	if m.panelPlacement.vertical() {
		if m.panelPlacement == PanelTop {
			conversation, inspector = inspector, conversation
		}

		return lipgloss.JoinVertical(lipgloss.Left, conversation, "", inspector)
	}

	gapHeight := max(lipgloss.Height(conversation), lipgloss.Height(inspector))
	gap := strings.TrimSuffix(strings.Repeat(" \n", gapHeight), "\n")
	if m.panelPlacement == PanelLeft {
		conversation, inspector = inspector, conversation
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, conversation, gap, inspector)
}

func (m model) conversationRegion(width int) string {
	focused := m.focus == focusTimeline
	horizontal, _ := panelInsets(focused)
	heading := m.style(presentation.RoleMuted).Render(fitLine("CONVERSATION", max(1, width-horizontal)))
	content := lipgloss.JoinVertical(lipgloss.Left, heading, m.viewport.View())
	return renderPanel(m.activeTheme(), content, width, focused)
}

func (m model) inspectorRegion(width int) string {
	focused := m.focus == focusTelemetry
	horizontal, _ := panelInsets(focused)
	innerWidth := max(1, width-horizontal)
	parts := []string{m.style(presentation.RoleMuted).Render(fitLine(m.inspectorHeading(), innerWidth))}
	if m.searchActive || m.search.Value() != "" {
		parts = append(parts, m.search.View())
	}

	parts = append(parts, m.telemetryViewport.View())
	return renderPanel(m.activeTheme(), strings.Join(parts, "\n"), width, focused)
}

func (m model) viewHeading() string {
	if !m.panelVisible() {
		return m.style(presentation.RoleMuted).Render(fitLine("CONVERSATION", m.width))
	}

	if m.width < 70 {
		return m.style(presentation.RoleMuted).Render(fitLine(m.inspectorHeading(), m.width))
	}

	if m.panelPlacement.vertical() {
		conversation, inspector := "CONVERSATION", m.inspectorHeading()
		if m.panelPlacement == PanelTop {
			conversation, inspector = inspector, conversation
		}

		return m.style(presentation.RoleMuted).Render(fitLine(conversation+"  ·  "+inspector, m.width))
	}

	left, right := m.viewWidths()
	conversationHeading := m.style(presentation.RoleMuted).Width(left).Render("CONVERSATION")
	inspectorHeading := m.style(presentation.RoleMuted).Width(right).Render(fitLine(m.inspectorHeading(), right))
	if m.panelPlacement == PanelLeft {
		return lipgloss.JoinHorizontal(lipgloss.Top,
			inspectorHeading,
			m.activeTheme().Divider.Render("│"),
			conversationHeading,
		)
	}

	return lipgloss.JoinHorizontal(lipgloss.Top,
		conversationHeading,
		m.activeTheme().Divider.Render("│"),
		inspectorHeading,
	)
}

func (m model) inspectorHeading() string {
	heading := "EVENT STREAM"
	if m.unseenTelemetry > 0 {
		heading += fmt.Sprintf(" +%d", m.unseenTelemetry)
	}

	return heading
}

func (m model) viewBody() string {
	if !m.panelVisible() {
		return m.viewport.View()
	}

	if m.width < 70 {
		return m.telemetryViewport.View()
	}

	if m.panelPlacement.vertical() {
		conversation, inspector := m.viewport.View(), m.telemetryViewport.View()
		divider := m.activeTheme().Divider.Render(strings.Repeat("─", m.width))
		if m.panelPlacement == PanelTop {
			conversation, inspector = inspector, conversation
		}

		return lipgloss.JoinVertical(lipgloss.Left, conversation, divider, inspector)
	}

	divider := m.activeTheme().Divider.Render(strings.TrimSuffix(strings.Repeat("│\n", m.viewport.Height), "\n"))
	conversation, inspector := m.viewport.View(), m.telemetryViewport.View()
	if m.panelPlacement == PanelLeft {
		conversation, inspector = inspector, conversation
		return lipgloss.JoinHorizontal(lipgloss.Top, conversation, divider, inspector)
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, conversation, divider, inspector)
}

func (m model) viewWidths() (int, int) {
	return max(1, m.width-m.panelWidth-1), max(1, m.panelWidth)
}

func (m model) desiredPanelSize() int {
	if m.panelPlacement.vertical() {
		return max(3, (m.height-5)*2/5)
	}

	return desiredPanelWidth(m.width)
}

func (m *model) setPanelSize(size int) {
	if m.panelPlacement.vertical() {
		m.panelHeight = size
		m.panelWidth = 0
		return
	}

	m.panelWidth = size
	m.panelHeight = 0
}

func (p PanelPlacement) vertical() bool {
	return p == PanelTop || p == PanelBottom
}

func validPanelPlacement(p PanelPlacement) bool {
	return p == PanelRight || p == PanelLeft || p == PanelBottom || p == PanelTop
}

func (m model) panelVisible() bool {
	return m.panelWidth > 0 || m.panelHeight > 0 || m.width < 70 && m.showTelemetry
}

func desiredPanelWidth(width int) int {
	return min(60, max(22, (width-1)*2/5))
}

func (m *model) resize(width, height int) {
	m.width, m.height = max(1, width), max(1, height)
	if m.width >= 70 && m.showTelemetry && m.panelWidth == 0 && m.panelHeight == 0 && !m.panelAnimating {
		m.setPanelSize(m.desiredPanelSize())
		m.panelPosition = float64(m.desiredPanelSize())
	}

	inputInsets, _ := panelInsets(m.focus == focusComposer)
	m.input.Width = max(1, m.width-inputInsets-lipgloss.Width(m.input.Prompt))
	inspectorOuterWidth := m.width
	if m.width >= 70 && m.panelWidth > 0 {
		_, inspectorOuterWidth = m.viewWidths()
	}

	searchInsets, _ := panelInsets(m.focus == focusTelemetry)
	m.search.Width = max(1, inspectorOuterWidth-searchInsets-lipgloss.Width(m.search.Prompt))

	mainHeight := m.mainRegionHeight()
	conversationWidth, telemetryWidth := m.width, m.width
	if m.panelWidth > 0 && m.width >= 70 {
		conversationWidth, telemetryWidth = m.viewWidths()
	}

	conversationInsetsH, conversationInsetsV := panelInsets(m.focus == focusTimeline)
	telemetryInsetsH, telemetryInsetsV := panelInsets(m.focus == focusTelemetry)
	conversationWidth = max(1, conversationWidth-conversationInsetsH)
	telemetryWidth = max(1, telemetryWidth-telemetryInsetsH)
	searchRows := 0
	if m.searchActive || m.search.Value() != "" {
		searchRows = 1
	}

	conversationHeight := max(1, mainHeight-1-conversationInsetsV)
	telemetryHeight := max(1, mainHeight-1-searchRows-telemetryInsetsV)
	if m.panelHeight > 0 && m.width >= 70 {
		inspectorMinHeight := 1 + telemetryInsetsV + searchRows + 1
		conversationMinHeight := 1 + conversationInsetsV + 1
		maxPanelHeight := max(0, mainHeight-1-conversationMinHeight)
		if maxPanelHeight < inspectorMinHeight {
			m.panelHeight = 0
		} else {
			m.panelHeight = min(max(m.panelHeight, inspectorMinHeight), maxPanelHeight)
			if !m.panelAnimating {
				m.panelPosition = float64(m.panelHeight)
			}
		}

		if m.panelHeight > 0 {
			telemetryHeight = max(1, m.panelHeight-1-telemetryInsetsV-searchRows)
			conversationHeight = max(1, mainHeight-m.panelHeight-1-1-conversationInsetsV)
		}
	}

	if !m.ready {
		m.viewport = viewport.New(conversationWidth, conversationHeight)
		m.telemetryViewport = viewport.New(telemetryWidth, telemetryHeight)
		m.ready = true
		return
	}

	m.viewport.Width = conversationWidth
	m.viewport.Height = conversationHeight
	m.telemetryViewport.Width = telemetryWidth
	m.telemetryViewport.Height = telemetryHeight
}

func (m model) mainRegionHeight() int {
	_, composerInsetsV := panelInsets(m.focus == focusComposer)
	composerHeight := lipgloss.Height(m.input.View()) + composerInsetsV
	headerHeight, footerHeight := 1, 1
	gapCount := 3
	approvalHeight := 0
	if panel := m.approvalPanel(); panel != "" {
		approvalHeight = lipgloss.Height(panel)
		gapCount++
	}

	return max(1, m.height-headerHeight-footerHeight-composerHeight-approvalHeight-gapCount)
}

func (m model) footerKeys() string {
	inspectorKey := " · ctrl+e events"
	switch m.focus {
	case focusApproval:
		return "y/enter accept · n/esc reject · tab focus · ctrl+x interrupt · ctrl+c quit" + inspectorKey
	case focusTimeline:
		return "↑/↓ scroll · pgup/pgdn page · g/G top/bottom · tab focus · ctrl+x interrupt · ctrl+c quit" + inspectorKey
	case focusTelemetry:
		return "↑/↓ select · enter expand · pgup/pgdn scroll · G follow · / filter · c copy · tab focus · ctrl+c quit" + inspectorKey
	default:
		if m.readOnly {
			return "replay · tab focus · ctrl+c quit" + inspectorKey
		}

		return "enter send · tab focus · ctrl+x interrupt · ctrl+c quit" + inspectorKey
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

	if !m.panelVisible() && len(m.presentation.Telemetry) > 0 {
		indicator := fmt.Sprintf("LOG %d", len(m.presentation.Telemetry))
		if m.unseenTelemetry > 0 {
			indicator += fmt.Sprintf(" +%d", m.unseenTelemetry)
		}

		if m.presentation.Activity.ActiveTools > 0 && m.presentation.Activity.Latest != "" {
			indicator += " · " + m.presentation.Activity.Latest
		}

		parts = append(parts, indicator)
	}

	if len(parts) == 0 {
		parts = append(parts, "ruga")
	}

	statusStyle := m.style(presentation.RoleMuted)
	if m.status == "working" || m.status == "interrupting" {
		statusStyle = m.style(presentation.RoleActive)
	} else if m.status == "error" {
		statusStyle = m.style(presentation.RoleFailure)
	}

	glyph := "●"
	if m.presentation.Activity.ActiveTools > 0 {
		glyph = activityGlyph(m.activityFrame)
	}

	state := glyph + " " + m.status
	if m.width <= lipgloss.Width(state)+3 {
		return statusStyle.Render(fitLine(state, m.width))
	}

	metadataWidth := max(1, m.width-lipgloss.Width(state)-3)
	metadata := m.activeTheme().Title.Render(fitLine(strings.Join(parts, "  ·  "), metadataWidth))
	return metadata + m.style(presentation.RoleMuted).Render("  ·  ") + statusStyle.Render(state)
}

func (m model) approvalPanel() string {
	if len(m.approvals) == 0 {
		return ""
	}

	focused := m.focus == focusApproval
	horizontal, _ := panelInsets(focused)
	width := max(1, m.width-horizontal)
	lineWidth := width
	request := m.approvals[0]
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
	return renderPanel(m.activeTheme(), strings.Join(lines, "\n"), m.width, focused)
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

	followConversation := !m.ready || m.viewport.AtBottom()
	followTelemetry := m.telemetryFollowing && (!m.ready || m.telemetryViewport.AtBottom())
	conversationOffset, telemetryOffset := m.viewport.YOffset, m.telemetryViewport.YOffset

	m.focus = target
	if target == focusTelemetry {
		m.ensureTelemetrySelection()
	}

	if target == focusComposer {
		m.input.Focus()
	} else {
		m.input.Blur()
	}

	if m.ready {
		m.resize(m.width, m.height)
		m.refreshViews(followConversation, followTelemetry)
		if !followConversation {
			m.viewport.SetYOffset(conversationOffset)
		}

		if !followTelemetry {
			m.telemetryViewport.SetYOffset(telemetryOffset)
		}
	}
}

func (m *model) nextFocus() {
	switch m.focus {
	case focusComposer:
		m.setFocus(focusTimeline)
	case focusTimeline:
		if m.showTelemetry {
			m.setFocus(focusTelemetry)
		} else if len(m.approvals) > 0 {
			m.setFocus(focusApproval)
		} else {
			m.setFocus(focusComposer)
		}

	case focusTelemetry:
		if len(m.approvals) > 0 {
			m.setFocus(focusApproval)
		} else {
			m.setFocus(focusComposer)
		}

	default:
		m.setFocus(focusComposer)
	}
}

func (m model) visibleTelemetry() []int {
	indices := make([]int, 0, len(m.presentation.Telemetry))
	for index, item := range m.presentation.Telemetry {
		if item.Matches(m.search.Value()) {
			indices = append(indices, index)
		}
	}

	return indices
}

func (m *model) ensureTelemetrySelection() {
	visible := m.visibleTelemetry()
	if len(visible) == 0 {
		m.selectedTelemetry = -1
		return
	}

	for _, index := range visible {
		if index == m.selectedTelemetry {
			return
		}
	}

	m.selectedTelemetry = visible[len(visible)-1]
}

func (m *model) moveTelemetrySelection(delta int) {
	visible := m.visibleTelemetry()
	if len(visible) == 0 {
		return
	}

	m.ensureTelemetrySelection()
	position := 0
	for index, item := range visible {
		if item == m.selectedTelemetry {
			position = index
			break
		}
	}

	position = max(0, min(len(visible)-1, position+delta))
	m.selectedTelemetry = visible[position]
	m.telemetryFollowing = false
	m.refreshViews(false, false)
	m.ensureSelectedVisible()
}

func (m *model) selectTelemetryBoundary(last bool) {
	visible := m.visibleTelemetry()
	if len(visible) == 0 {
		return
	}

	index := 0
	if last {
		index = len(visible) - 1
	}

	m.selectedTelemetry = visible[index]
	m.telemetryFollowing = last
	if last {
		m.unseenTelemetry = 0
	}

	m.refreshViews(false, last)
	if last {
		m.telemetryViewport.GotoBottom()
	} else {
		m.telemetryViewport.GotoTop()
	}
}

func (m *model) toggleTelemetryExpansion() {
	m.ensureTelemetrySelection()
	if m.selectedTelemetry < 0 {
		return
	}

	if m.expandedTelemetry == nil {
		m.expandedTelemetry = make(map[int]bool)
	}

	m.expandedTelemetry[m.selectedTelemetry] = !m.expandedTelemetry[m.selectedTelemetry]
	m.telemetryFollowing = false
	m.refreshViews(false, false)
	m.ensureSelectedVisible()
}

func (m *model) ensureSelectedVisible() {
	rows, ok := m.telemetryRows[m.selectedTelemetry]
	if !ok || m.telemetryViewport.Height <= 0 {
		return
	}

	if m.expandedTelemetry[m.selectedTelemetry] || rows[0] < m.telemetryViewport.YOffset || rows[0] >= m.telemetryViewport.YOffset+m.telemetryViewport.Height {
		m.telemetryViewport.SetYOffset(rows[0])
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

	case tea.KeyCtrlE:
		m.showTelemetry = !m.showTelemetry
		if !m.showTelemetry && m.focus == focusTelemetry {
			m.setFocus(focusTimeline)
		} else if m.showTelemetry && m.width < 70 && m.focus == focusTimeline {
			m.setFocus(focusTelemetry)
		}

		if m.showTelemetry {
			m.ensureTelemetrySelection()
		}

		if m.width >= 70 {
			m.panelGeneration++
			m.panelAnimating = true
			m.resizeIfReady()
			return m, nextPanelFrame(m.panelGeneration)
		}

		m.panelWidth, m.panelHeight, m.panelPosition, m.panelVelocity = 0, 0, 0, 0
		m.resizeIfReady()
		if m.showTelemetry && m.telemetryFollowing && m.telemetryViewport.AtBottom() {
			m.unseenTelemetry = 0
		}

		return m, nil

	case tea.KeyTab:
		if m.searchActive {
			m.searchActive = false
			m.search.Blur()
			m.resizeIfReady()
			return m, nil
		}

		m.nextFocus()
		m.refreshViews(false, false)
		if m.focus == focusTelemetry {
			m.ensureSelectedVisible()
		}

		return m, nil

	case tea.KeyEsc:
		if m.searchActive {
			m.search.SetValue("")
			m.searchActive = false
			m.search.Blur()
			m.resizeIfReady()
			return m, nil
		}

		if m.search.Value() != "" && m.focus == focusTelemetry {
			m.search.SetValue("")
			m.resizeIfReady()
			return m, nil
		}

		switch m.focus {
		case focusApproval:
			return m.decideActiveApproval(event.ApprovalReject)
		case focusTimeline, focusTelemetry:
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

	if m.focus == focusTimeline || m.focus == focusTelemetry {
		targetViewport := &m.viewport
		if m.focus == focusTelemetry {
			targetViewport = &m.telemetryViewport
		}

		if msg.Type == tea.KeyRunes {
			switch msg.String() {
			case "/":
				if m.focus != focusTelemetry {
					break
				}

				m.searchActive = true
				m.search.Focus()
				m.resizeIfReady()
				return m, textinput.Blink

			case "c":
				copyText := m.copyConversation()
				if m.focus == focusTelemetry {
					copyText = m.copyTelemetry()
				}

				if err := clipboard.WriteAll(copyText); err != nil {
					m.notice = "copy unavailable"
				} else {
					m.notice = "view copied"
				}

				return m, nil
			}
		}

		if m.focus == focusTelemetry {
			switch msg.Type {
			case tea.KeyUp:
				m.moveTelemetrySelection(-1)
				return m, nil

			case tea.KeyDown:
				m.moveTelemetrySelection(1)
				return m, nil

			case tea.KeyEnter:
				m.toggleTelemetryExpansion()
				return m, nil

			case tea.KeyPgUp, tea.KeyPgDown:
				m.telemetryFollowing = false
			case tea.KeyHome:
				m.selectTelemetryBoundary(false)
				return m, nil

			case tea.KeyEnd:
				m.selectTelemetryBoundary(true)
				return m, nil

			case tea.KeyRunes:
				if msg.String() == "g" || msg.String() == "G" {
					m.selectTelemetryBoundary(msg.String() == "G")
					return m, nil
				}
			}
		}

		switch msg.Type {
		case tea.KeyUp:
			targetViewport.LineUp(1)
		case tea.KeyDown:
			targetViewport.LineDown(1)
		case tea.KeyPgUp:
			targetViewport.PageUp()
		case tea.KeyPgDown:
			targetViewport.PageDown()
		case tea.KeyHome:
			targetViewport.GotoTop()
		case tea.KeyEnd:
			targetViewport.GotoBottom()
		case tea.KeyEnter:
			if m.focus == focusTimeline {
				m.setFocus(focusComposer)
			}

		case tea.KeyRunes:
			if msg.String() == "g" {
				targetViewport.GotoTop()
			} else if msg.String() == "G" {
				targetViewport.GotoBottom()
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
	m.presentation.Apply(ev)
	if ev.Backend != "" && ev.Backend != "app" {
		if m.backend == "" {
			m.backend = strings.ToUpper(ev.Backend[:1]) + ev.Backend[1:]
		}
	}

	if name := m.presentation.Activity.Model; name != "" {
		m.modelName = name
	}

	if usage := m.presentation.Activity.Usage; usage != "" {
		m.usage = compactUsage(usage)
	}

	switch ev.Kind {
	case "approval.requested":
		if !m.readOnly && ev.Approval != nil && !m.hasApproval(ev.Approval.RequestID) {
			m.approvals = append(m.approvals, *ev.Approval)
		}

		if !m.readOnly && ev.Approval != nil {
			m.setFocus(focusApproval)
		}

	case "approval.resolved":
		if ev.Approval != nil {
			m.removeApproval(ev.Approval.RequestID)
			delete(m.submitting, ev.Approval.RequestID)
		}

		if len(m.approvals) == 0 {
			m.setFocus(focusComposer)
		} else if m.focus == focusApproval {
			m.setFocus(focusApproval)
		}
	}
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
	m.refreshViews(follow, !m.ready || m.telemetryViewport.AtBottom())
}

func (m *model) refreshViews(followConversation, followTelemetry bool) {
	if !m.ready {
		return
	}

	conversation := make([]string, 0, len(m.presentation.Conversation))
	for _, item := range m.presentation.Conversation {
		if item.Kind == presentation.ConversationReasoning {
			conversation = append(conversation, m.style(item.Role).Render(fitLine("◈ "+item.Text, m.viewport.Width)))
			continue
		}

		label := "YOU"
		if item.Kind == presentation.ConversationAssistant {
			label = "ASSISTANT"
		}

		body := wrapPreservingLines(safeTerminalText(item.Text), m.viewport.Width)
		bodyStyle := m.activeTheme().Text
		if item.Kind == presentation.ConversationAssistant && item.Completed {
			var rendered bool
			body, rendered = renderAssistantMarkdown(item.Text, m.viewport.Width, m.activeTheme(), terminalSupportsMarkdownColor())
			if rendered {
				bodyStyle = lipgloss.NewStyle()
			}
		}

		conversation = append(conversation, m.style(item.Role).Render(label)+"\n"+bodyStyle.Render(body))
	}

	m.viewport.SetContent(strings.Join(conversation, "\n\n"))
	if followConversation {
		m.viewport.GotoBottom()
	}

	m.ensureTelemetrySelection()
	telemetry := make([]string, 0, len(m.presentation.Telemetry))
	m.telemetryRows = make(map[int][2]int, len(m.presentation.Telemetry))
	for index, item := range m.presentation.Telemetry {
		if !item.Matches(m.search.Value()) {
			continue
		}

		start := len(telemetry)
		line := telemetryLine(item, m.telemetryViewport.Width, m.activityFrame)
		role := item.Role
		if item.State == presentation.RoleFailure || item.State == presentation.RoleWarning {
			role = item.State
		}

		if m.showTelemetry && index == m.selectedTelemetry {
			role = presentation.RoleSelected
		}

		if role == presentation.RoleSelected {
			telemetry = append(telemetry, m.style(role).Render(line))
		} else {
			prefix, rest := splitCells(line, 11)
			telemetry = append(telemetry, m.style(role).Render(prefix)+m.activeTheme().Text.Render(rest))
		}

		if m.expandedTelemetry[index] {
			telemetry = append(telemetry, m.expandedTelemetryLines(item, m.telemetryViewport.Width)...)
		}

		m.telemetryRows[index] = [2]int{start, len(telemetry) - 1}
	}

	m.telemetryViewport.SetContent(strings.Join(telemetry, "\n"))
	if followTelemetry {
		m.telemetryViewport.GotoBottom()
	}
}

func (m model) expandedTelemetryLines(item presentation.TelemetryItem, width int) []string {
	const displayLimit = 8192
	var lines []string
	used, omitted := 0, 0
	for _, detail := range item.ExpandedDetails() {
		value := strings.ReplaceAll(safeTerminalText(detail.Value), "\t", "    ")
		remaining := max(0, displayLimit-used)
		if remaining == 0 {
			omitted += len(value)
			continue
		}

		shown := utf8Prefix(value, remaining)
		used += len(shown)
		omitted += len(value) - len(shown)
		label := fitLine(detail.Label, 10)
		prefix := "  ├ " + lipgloss.NewStyle().Width(10).Render(label) + " "
		wrapped := strings.Split(wrapPreservingLines(shown, max(1, width-lipgloss.Width(prefix))), "\n")
		for index, row := range wrapped {
			if index == 0 {
				lines = append(lines, m.style(presentation.RoleMuted).Render(truncateCells(prefix+row, width)))
			} else {
				lines = append(lines, truncateCells(strings.Repeat(" ", lipgloss.Width(prefix))+row, width))
			}
		}
	}

	if omitted > 0 {
		lines = append(lines, truncateCells(fmt.Sprintf("  … %d bytes omitted; copy view for full text", omitted), width))
	}

	return lines
}

func telemetryLine(item presentation.TelemetryItem, width, frame int) string {
	width = max(1, width)
	glyph := item.Glyph
	if item.State == presentation.RoleActive && (item.Kind == presentation.TelemetryTool || item.Kind == presentation.TelemetryCommand) {
		glyph = activityGlyph(frame)
	}

	prefix := glyph + " " + fitLine(item.Type, 8)
	if width <= lipgloss.Width(prefix)+3 {
		return fitLine(prefix, width)
	}

	prefix = lipgloss.NewStyle().Width(11).Render(prefix)
	primary := item.Primary
	if primary == "" {
		primary = item.Summary
	}

	detail, status := item.Detail, item.DisplayStatus
	if width < 55 {
		detail = ""
	}

	if width < 25 {
		status = ""
	}

	detailWidth := min(20, lipgloss.Width(detail))
	statusWidth := lipgloss.Width(status)
	separators := 0
	if detail != "" {
		separators += 2
	}

	if status != "" {
		separators += 2
	}

	primaryWidth := max(1, width-lipgloss.Width(prefix)-detailWidth-statusWidth-separators)
	line := prefix + lipgloss.NewStyle().Width(primaryWidth).Render(fitLine(primary, primaryWidth))
	if detail != "" {
		line += "  " + fitLine(detail, detailWidth)
	}

	if status != "" {
		line += "  " + status
	}

	return truncateCells(line, width)
}

func activityGlyph(frame int) string {
	return []string{"◐", "◓", "◑", "◒"}[frame%4]
}

func truncateCells(value string, width int) string {
	value = safeTerminalText(strings.ReplaceAll(value, "\n", " "))
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

func splitCells(value string, width int) (string, string) {
	used := 0
	for index, r := range value {
		cellWidth := lipgloss.Width(string(r))
		if used+cellWidth > width {
			return value[:index], value[index:]
		}

		used += cellWidth
	}

	return value, ""
}

func wrapPreservingLines(value string, width int) string {
	width = max(1, width)
	var lines []string
	for _, source := range strings.Split(value, "\n") {
		var line strings.Builder
		used := 0
		for _, r := range source {
			cellWidth := lipgloss.Width(string(r))
			if used+cellWidth > width && line.Len() > 0 {
				lines = append(lines, line.String())
				line.Reset()
				used = 0
			}

			line.WriteRune(r)
			used += cellWidth
		}

		lines = append(lines, line.String())
	}

	return strings.Join(lines, "\n")
}

func (m model) copyTimeline() string {
	return m.copyRows(nil)
}

func (m model) copyConversation() string {
	surface := presentation.SurfaceConversation
	return m.copyRows(&surface)
}

func (m model) copyTelemetry() string {
	surface := presentation.SurfaceTelemetry
	return m.copyRows(&surface)
}

func (m model) copyRows(surface *presentation.Surface) string {
	var rows []string
	for _, entry := range m.presentation.Order {
		if surface != nil && entry.Surface != *surface {
			continue
		}

		if entry.Surface == presentation.SurfaceConversation {
			item := m.presentation.Conversation[entry.Index]
			rows = append(rows, string(item.Kind)+"  "+item.Text)
			continue
		}

		item := m.presentation.Telemetry[entry.Index]
		if !item.Matches(m.search.Value()) {
			continue
		}

		line := strings.TrimSpace(item.Label + "  " + item.Summary)
		for _, detail := range item.Details {
			line += "\n" + detail.Label + ":\n" + detail.Value
		}

		rows = append(rows, line)
	}

	return strings.Join(rows, "\n\n")
}

func compactUsage(value string) string {
	value = strings.ReplaceAll(value, " total tokens", " tokens")
	value = strings.ReplaceAll(value, "context ", "ctx ")
	return strings.TrimSpace(value)
}

func waitBatch(stream <-chan []event.Event, stayOpen bool) tea.Cmd {
	return func() tea.Msg {
		batch, ok := <-stream
		if !ok {
			if stayOpen {
				return streamClosedMsg{}
			}

			return tea.QuitMsg{}
		}

		return batchMsg(batch)
	}
}

func nextPanelFrame(generation int) tea.Cmd {
	return tea.Tick(time.Second/30, func(time.Time) tea.Msg {
		return panelFrameMsg{generation: generation}
	})
}

func nextActivityFrame(generation int) tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg {
		return activityFrameMsg{generation: generation}
	})
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
