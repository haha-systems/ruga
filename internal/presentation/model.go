// Package presentation reduces normalized application events into records the
// terminal can display without knowing which backend produced them.
package presentation

import (
	"strings"

	"github.com/haha-systems/ruga/internal/event"
)

type SemanticRole string

const (
	RoleNavigation SemanticRole = "navigation"
	RoleRead       SemanticRole = "read"
	RoleMutation   SemanticRole = "mutation"
	RoleExecution  SemanticRole = "execution"
	RoleReasoning  SemanticRole = "reasoning"
	RoleSuccess    SemanticRole = "success"
	RoleWarning    SemanticRole = "warning"
	RoleFailure    SemanticRole = "failure"
	RoleMuted      SemanticRole = "muted"
	RoleActive     SemanticRole = "active"
	RoleSelected   SemanticRole = "selected"
)

type ConversationKind string

const (
	ConversationUser      ConversationKind = "user"
	ConversationAssistant ConversationKind = "assistant"
	ConversationReasoning ConversationKind = "reasoning"
)

type ConversationItem struct {
	Kind   ConversationKind
	Text   string
	Role   SemanticRole
	TurnID string
	ItemID string
	Events []event.Event
}

func (item ConversationItem) Matches(query string) bool {
	return matches(query, string(item.Kind), item.Text, item.Events)
}

type TelemetryKind string

const (
	TelemetryTool     TelemetryKind = "tool"
	TelemetryCommand  TelemetryKind = "command"
	TelemetryFile     TelemetryKind = "file"
	TelemetryApproval TelemetryKind = "approval"
	TelemetryWarning  TelemetryKind = "warning"
	TelemetryError    TelemetryKind = "error"
	TelemetrySession  TelemetryKind = "session"
	TelemetryTurn     TelemetryKind = "turn"
	TelemetryUsage    TelemetryKind = "usage"
	TelemetryOther    TelemetryKind = "other"
)

type Detail struct {
	Label string
	Value string
}

type TelemetryItem struct {
	Kind          TelemetryKind
	Label         string
	Glyph         string
	Type          string
	Primary       string
	Detail        string
	DisplayStatus string
	Summary       string
	Status        string
	Role          SemanticRole
	State         SemanticRole
	TurnID        string
	ItemID        string
	Details       []Detail
	Events        []event.Event
}

func (item TelemetryItem) Matches(query string) bool {
	parts := []string{string(item.Kind), item.Label, item.Glyph, item.Type, item.Primary, item.Detail, item.Summary, item.Status, item.DisplayStatus}
	for _, detail := range item.Details {
		parts = append(parts, detail.Label, detail.Value)
	}

	return matches(query, strings.Join(parts, " "), "", item.Events)
}

func matches(query, primary, secondary string, events []event.Event) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}

	if strings.Contains(strings.ToLower(primary+" "+secondary), query) {
		return true
	}

	for _, ev := range events {
		if strings.Contains(strings.ToLower(ev.Kind+" "+ev.Source+" "+ev.Summary), query) {
			return true
		}
	}

	return false
}

type ActivityState struct {
	TurnStatus      string
	ActiveTools     int
	EventCount      int
	TelemetryEvents int
	Latest          string
	Usage           string
	Model           string
}

type Surface uint8

const (
	SurfaceConversation Surface = iota
	SurfaceTelemetry
)

// Entry preserves arrival order for the existing single viewport. The split
// interface can use Conversation and Telemetry independently.
type Entry struct {
	Surface Surface
	Index   int
}

type Model struct {
	Conversation []ConversationItem
	Telemetry    []TelemetryItem
	Activity     ActivityState
	Order        []Entry

	activeMessages  map[string]int
	activeTelemetry map[string]int
}

func (m *Model) Apply(ev event.Event) {
	m.Activity.EventCount++
	if name := modelName(ev); name != "" {
		m.Activity.Model = name
	}

	switch ev.Kind {
	case "user.message":
		m.appendConversation(ConversationItem{Kind: ConversationUser, Text: ev.Summary, Role: RoleNavigation, TurnID: ev.TurnID, ItemID: ev.ItemID, Events: []event.Event{ev}})
	case "message.started":
		delete(m.activeMessages, messageKey(ev))
	case "message.delta":
		m.assistantMessage(ev, false)
	case "message.completed":
		m.assistantMessage(ev, true)
	case "status.update":
		m.reasoning(ev)
	case "turn.started":
		m.Activity.TurnStatus = "working"
		m.operational(ev)

	case "turn.completed":
		m.Activity.TurnStatus = turnStatus(ev)
		m.finishActive(ev.TurnID)
		m.Activity.ActiveTools = 0
		m.operational(ev)
		clear(m.activeTelemetry)
		clear(m.activeMessages)

	case "error":
		m.Activity.TurnStatus = "error"
		m.operational(ev)

	case "usage.updated":
		m.Activity.Usage = ev.Summary
		m.operational(ev)

	default:
		m.operational(ev)
	}
}

func (m *Model) finishActive(turnID string) {
	for index := range m.Telemetry {
		item := &m.Telemetry[index]
		if (item.Kind != TelemetryTool && item.Kind != TelemetryCommand) || item.State != RoleActive || turnID != "" && item.TurnID != turnID {
			continue
		}

		if m.Activity.TurnStatus == "error" {
			item.State, item.Status = RoleFailure, "failed"
		} else {
			item.State, item.Status = RoleWarning, "stopped"
		}

		item.updateLine()
	}
}

func (m *Model) appendConversation(item ConversationItem) int {
	index := len(m.Conversation)
	m.Conversation = append(m.Conversation, item)
	m.Order = append(m.Order, Entry{Surface: SurfaceConversation, Index: index})
	return index
}

func messageKey(ev event.Event) string {
	if ev.ItemID != "" {
		return "item:" + ev.ItemID
	}

	return "turn:" + ev.TurnID
}

func (m *Model) assistantMessage(ev event.Event, completed bool) {
	key := messageKey(ev)
	index, found := m.activeMessages[key]
	if !found {
		if ev.Summary == "" {
			return
		}

		index = m.appendConversation(ConversationItem{Kind: ConversationAssistant, Role: RoleSuccess, TurnID: ev.TurnID, ItemID: ev.ItemID})
		if m.activeMessages == nil {
			m.activeMessages = make(map[string]int)
		}

		m.activeMessages[key] = index
	}

	item := &m.Conversation[index]
	item.Events = append(item.Events, ev)
	if completed {
		if ev.Summary != "" {
			item.Text = ev.Summary
		}

		delete(m.activeMessages, key)
	} else {
		item.Text += ev.Summary
	}
}

func (m *Model) reasoning(ev event.Event) {
	if ev.Summary == "" {
		m.operational(ev)
		return
	}

	key := "reasoning:" + ev.ItemID
	if ev.ItemID != "" {
		if index, found := m.activeMessages[key]; found {
			item := &m.Conversation[index]
			if strings.HasPrefix(ev.Summary, item.Text) {
				item.Text = ev.Summary
			} else {
				item.Text += ev.Summary
			}

			item.Events = append(item.Events, ev)
			return
		}
	}

	index := m.appendConversation(ConversationItem{Kind: ConversationReasoning, Role: RoleReasoning, Text: ev.Summary, TurnID: ev.TurnID, ItemID: ev.ItemID, Events: []event.Event{ev}})
	if ev.ItemID != "" {
		if m.activeMessages == nil {
			m.activeMessages = make(map[string]int)
		}

		m.activeMessages[key] = index
	}
}

func (m *Model) operational(ev event.Event) {
	m.Activity.TelemetryEvents++
	kind, label, role := classify(ev)
	key := telemetryKey(ev, kind)
	index, found := m.activeTelemetry[key]
	if key == "" || !found {
		index = len(m.Telemetry)
		m.Telemetry = append(m.Telemetry, TelemetryItem{Kind: kind, Label: label, Role: role, TurnID: ev.TurnID, ItemID: ev.ItemID})
		m.Order = append(m.Order, Entry{Surface: SurfaceTelemetry, Index: index})
		if key != "" {
			if m.activeTelemetry == nil {
				m.activeTelemetry = make(map[string]int)
			}

			m.activeTelemetry[key] = index
		}
	}

	item := &m.Telemetry[index]
	item.Events = append(item.Events, ev)
	if ev.Kind != "command.output" && ev.Summary != "" {
		item.Summary = ev.Summary
	}

	if ev.Kind == "command.output" {
		for _, detail := range details(ev) {
			appendOutput(item, detail)
		}
	} else if ev.Kind != "command.completed" || !hasOutput(item.Details) {
		item.Details = append(item.Details, details(ev)...)
	}

	if kind == TelemetryOther && ev.Kind == "backend.unknown" {
		item.Summary = "Unrecognized activity"
	}

	item.State, item.Status = state(ev)
	if ev.Kind == "command.output" || ev.Kind == "tool.progress" {
		item.State, item.Status = RoleActive, "running"
	}

	if (ev.Kind == "tool.started" || ev.Kind == "command.started") && !found {
		m.Activity.ActiveTools++
	}

	if ev.Kind == "tool.completed" || ev.Kind == "command.completed" {
		if found && m.Activity.ActiveTools > 0 {
			m.Activity.ActiveTools--
		}

		delete(m.activeTelemetry, key)
	}

	item.updateLine()
	m.Activity.Latest = item.Type
}

func appendOutput(item *TelemetryItem, incoming Detail) {
	for index := range item.Details {
		if item.Details[index].Label == incoming.Label {
			item.Details[index].Value += incoming.Value
			return
		}
	}

	item.Details = append(item.Details, incoming)
}

func hasOutput(details []Detail) bool {
	for _, detail := range details {
		if detail.Label == "stdout" || detail.Label == "stderr" {
			return true
		}
	}

	return false
}

func telemetryKey(ev event.Event, kind TelemetryKind) string {
	if ev.ItemID != "" {
		switch kind {
		case TelemetryTool, TelemetryCommand, TelemetryFile:
			return string(kind) + ":" + ev.TurnID + ":" + ev.ItemID
		}
	}

	if kind == TelemetryApproval && ev.Approval != nil && ev.Approval.RequestID != "" {
		return "approval:" + ev.Approval.RequestID
	}

	if kind == TelemetryUsage {
		return "usage"
	}

	if kind == TelemetryTurn && ev.TurnID != "" {
		return "turn:" + ev.TurnID
	}

	return ""
}

func classify(ev event.Event) (TelemetryKind, string, SemanticRole) {
	switch ev.Kind {
	case "tool.started", "tool.progress", "tool.completed":
		return TelemetryTool, toolLabel(ev), toolRole(ev)
	case "command.started", "command.output", "command.completed":
		return TelemetryCommand, "Command", RoleExecution
	case "file.changed", "file.output":
		return TelemetryFile, "File change", RoleMutation
	case "approval.requested", "approval.resolved":
		return TelemetryApproval, "Approval", RoleWarning
	case "warning":
		return TelemetryWarning, "Warning", RoleWarning
	case "error":
		return TelemetryError, "Error", RoleFailure
	case "session.resumed", "thread.started", "backend.connected":
		return TelemetrySession, "Session", RoleNavigation
	case "turn.started", "turn.completed":
		return TelemetryTurn, "Turn", RoleMuted
	case "usage.updated":
		return TelemetryUsage, "Usage", RoleMuted
	default:
		return TelemetryOther, "Event", RoleMuted
	}
}

func toolLabel(ev event.Event) string {
	name, _ := ev.Data["tool_name"].(string)
	if name == "" {
		if fields := strings.Fields(ev.Summary); len(fields) > 0 {
			name = fields[0]
		}
	}

	if name == "" {
		return "Tool"
	}

	name = strings.TrimSuffix(name, ":")
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		name = name[slash+1:]
	}

	return "Tool · " + name
}

func toolRole(ev event.Event) SemanticRole {
	name, _ := ev.Data["tool_name"].(string)
	if name == "" {
		name = ev.Summary
	}

	name = strings.ToLower(name)
	switch {
	case strings.Contains(name, "search"), strings.Contains(name, "read"):
		return RoleRead
	case strings.Contains(name, "list"):
		return RoleNavigation
	case strings.Contains(name, "patch"), strings.Contains(name, "write"):
		return RoleMutation
	case strings.Contains(name, "exec"):
		return RoleExecution
	default:
		return RoleNavigation
	}
}

func state(ev event.Event) (SemanticRole, string) {
	switch ev.Kind {
	case "tool.started", "command.started", "turn.started", "approval.requested":
		return RoleActive, "running"
	case "error":
		return RoleFailure, "failed"
	case "warning":
		return RoleWarning, "warning"
	case "approval.resolved":
		if ev.Decision == event.ApprovalReject {
			return RoleWarning, "rejected"
		}

		return RoleSuccess, "accepted"

	case "turn.completed":
		if turnStatus(ev) == "interrupted" {
			return RoleWarning, "interrupted"
		}

		if failed(ev) {
			return RoleFailure, "failed"
		}

		return RoleSuccess, "done"

	case "tool.completed", "command.completed":
		if failed(ev) {
			return RoleFailure, "failed"
		}

		return RoleSuccess, "done"

	default:
		return RoleMuted, ""
	}
}

func failed(ev event.Event) bool {
	if isError, _ := ev.Data["error"].(bool); isError {
		return true
	}

	if status, _ := ev.Data["status"].(string); status == "failed" || status == "error" {
		return true
	}

	switch exit := ev.Data["exit_code"].(type) {
	case int:
		if exit != 0 {
			return true
		}

	case float64:
		if exit != 0 {
			return true
		}
	}

	return strings.Contains(strings.ToLower(ev.Summary), "failed") || strings.Contains(strings.ToLower(ev.Summary), "exit 1")
}

func details(ev event.Event) []Detail {
	switch ev.Kind {
	case "command.output":
		stream, _ := ev.Data["stream"].(string)
		if stream == "" {
			stream = "stdout"
		}

		return []Detail{{Label: stream, Value: ev.Summary}}

	case "tool.progress":
		return []Detail{{Label: "progress", Value: ev.Summary}}
	case "tool.started":
		if args, ok := ev.Data["arguments"].(string); ok && args != "" {
			return []Detail{{Label: "arguments", Value: args}}
		}

	case "tool.completed":
		if result, ok := ev.Data["result"].(string); ok && result != "" {
			return []Detail{{Label: "result", Value: result}}
		}

	case "command.completed":
		if output, ok := ev.Data["output"].(string); ok && output != "" {
			return []Detail{{Label: "stdout", Value: output}}
		}

	case "file.output":
		if ev.Summary != "" {
			return []Detail{{Label: "change", Value: ev.Summary}}
		}
	}

	return nil
}

func turnStatus(ev event.Event) string {
	status, _ := ev.Data["status"].(string)
	switch status {
	case "failed":
		return "error"
	case "interrupted", "cancelled", "canceled":
		return "interrupted"
	default:
		return "idle"
	}
}

func modelName(ev event.Event) string {
	for _, key := range []string{"model", "modelName", "model_name", "toModel"} {
		if value, ok := ev.Data[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}

	return ""
}
