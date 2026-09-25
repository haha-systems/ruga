package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/haha-systems/ruga/internal/bus"
	"github.com/haha-systems/ruga/internal/event"
	codexgo "github.com/zealbase/codex-app-server-go"
)

type Backend struct {
	binary             string
	cwd                string
	mu                 sync.Mutex
	client             *codexgo.Client
	thread             *codexgo.SessionThread
	threadID           string
	modelName          string
	turnID             string
	sub                *codexgo.EventSubscription
	eventBus           bus.Bus
	busy               bool
	interruptRequested bool
	pendingApprovals   map[string]chan event.ApprovalDecision
}

func New(binary, cwd string) *Backend { return &Backend{binary: binary, cwd: cwd} }

func (b *Backend) Start(ctx context.Context, eventBus bus.Bus) error {
	path, err := exec.LookPath(b.binary)
	if err != nil {
		return fmt.Errorf("find Codex CLI %q: %w", b.binary, err)
	}
	client, err := codexgo.New(
		codexgo.WithStdioProcess(path, "app-server", "--stdio"),
		codexgo.WithProcessDir(b.cwd),
		codexgo.WithRequestHandler(serverRequestHandler{backend: b}),
	)
	if err != nil {
		return fmt.Errorf("connect to Codex App Server: %w", err)
	}
	// Model metadata is useful for the status line but is not required to use
	// the backend. Older or restricted servers may not support config/read.
	configCtx, cancelConfig := context.WithTimeout(ctx, 2*time.Second)
	result, readErr := client.ConfigRead(configCtx, codexgo.ConfigReadRequest{CWD: b.cwd})
	cancelConfig()
	if readErr == nil {
		var config struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(result.Config, &config) == nil {
			b.modelName = strings.TrimSpace(config.Model)
		}
	}
	sub := client.Events()
	b.mu.Lock()
	b.client, b.sub, b.eventBus = client, sub, eventBus
	b.mu.Unlock()

	if err := eventBus.Publish(ctx, event.Event{
		ID: watermill.NewUUID(), Timestamp: time.Now(), Backend: "codex",
		Kind: "backend.connected", Summary: "Codex App Server connected",
	}); err != nil {
		_ = b.Close()
		return err
	}

	// Drain the SDK subscription independently from the UI and bus dispatcher.
	go b.forward(ctx, sub, eventBus)

	thread, err := client.StartThread(ctx, codexgo.WithThreadCWD(b.cwd))
	if err != nil {
		_ = eventBus.Publish(ctx, event.Event{
			ID: watermill.NewUUID(), Timestamp: time.Now(), Backend: "codex",
			Kind: "error", Summary: "Could not start Codex thread: " + err.Error(),
		})
		return fmt.Errorf("start Codex thread: %w", err)
	}
	b.mu.Lock()
	b.thread, b.threadID = thread, thread.ID()
	b.mu.Unlock()
	return nil
}

func (b *Backend) ModelName() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.modelName
}

func (b *Backend) forward(ctx context.Context, sub *codexgo.EventSubscription, eventBus bus.Bus) {
	for {
		select {
		case <-ctx.Done():
			return
		case sdkEvent, ok := <-sub.C():
			if !ok {
				return
			}
			ev := normalizeEvent(sdkEvent)
			if ev.Kind == "turn.completed" || ev.Kind == "error" {
				b.mu.Lock()
				b.busy = false
				b.turnID = ""
				b.interruptRequested = false
				b.mu.Unlock()
			} else if ev.Kind == "turn.started" && ev.TurnID != "" {
				b.mu.Lock()
				if b.busy {
					b.turnID = ev.TurnID
				}
				b.mu.Unlock()
			}
			if err := eventBus.Publish(ctx, ev); err != nil && ctx.Err() == nil {
				_ = eventBus.Publish(ctx, event.Event{
					ID: watermill.NewUUID(), Timestamp: time.Now(), Backend: "codex",
					Kind: "error", Summary: "Could not queue backend event: " + err.Error(),
				})
			}
		}
	}
}

func (b *Backend) Submit(ctx context.Context, prompt string) error {
	b.mu.Lock()
	if b.client == nil || b.thread == nil || b.eventBus == nil {
		b.mu.Unlock()
		return fmt.Errorf("Codex thread is not started")
	}
	if b.busy {
		b.mu.Unlock()
		return fmt.Errorf("Codex is still working on the current turn")
	}
	b.busy = true
	client, eventBus := b.client, b.eventBus
	threadID := b.threadID
	b.mu.Unlock()

	if err := eventBus.Publish(ctx, event.Event{
		ID: watermill.NewUUID(), Timestamp: time.Now(), Backend: "app",
		Kind: "user.message", ThreadID: threadID, Summary: prompt,
		Data: map[string]any{"text": prompt},
	}); err != nil {
		b.setIdle()
		return err
	}
	turn, err := client.TurnStart(ctx, codexgo.TurnStartRequest{ThreadID: threadID, Input: prompt})
	if err != nil {
		b.setIdle()
		_ = eventBus.Publish(ctx, event.Event{
			ID: watermill.NewUUID(), Timestamp: time.Now(), Backend: "codex",
			Kind: "error", ThreadID: threadID, Summary: "Could not submit prompt: " + err.Error(),
		})
		return err
	}
	b.mu.Lock()
	if b.busy && turn.ID != "" {
		b.turnID = turn.ID
	}
	interrupt := b.busy && b.interruptRequested
	b.interruptRequested = false
	b.mu.Unlock()
	if interrupt {
		return b.Interrupt(ctx)
	}
	return nil
}

func (b *Backend) Interrupt(ctx context.Context) error {
	b.mu.Lock()
	if !b.busy || b.client == nil || b.threadID == "" {
		b.mu.Unlock()
		return fmt.Errorf("no active turn to interrupt")
	}
	if b.turnID == "" {
		b.interruptRequested = true
		b.mu.Unlock()
		return nil
	}
	client := b.client
	threadID, turnID := b.threadID, b.turnID
	b.mu.Unlock()
	if err := client.TurnInterrupt(ctx, codexgo.TurnInterruptRequest{ThreadID: threadID, TurnID: turnID}); err != nil {
		return err
	}
	b.rejectPendingApprovals()
	return nil
}

func (b *Backend) rejectPendingApprovals() {
	b.mu.Lock()
	b.rejectPendingApprovalsLocked()
	b.mu.Unlock()
}

func (b *Backend) rejectPendingApprovalsLocked() {
	for requestID, pending := range b.pendingApprovals {
		if b.eventBus != nil {
			_ = b.eventBus.Publish(context.Background(), event.Event{
				ID: watermill.NewUUID(), Timestamp: time.Now(), Backend: "app",
				Kind: "approval.resolved", Summary: string(event.ApprovalReject),
				Approval: &event.ApprovalRequest{RequestID: requestID}, Decision: event.ApprovalReject,
			})
		}
		delete(b.pendingApprovals, requestID)
		pending <- event.ApprovalReject
	}
}

func (b *Backend) ResolveApproval(ctx context.Context, requestID string, decision event.ApprovalDecision) error {
	if decision != event.ApprovalAccept && decision != event.ApprovalReject {
		return fmt.Errorf("unsupported approval decision %q", decision)
	}
	b.mu.Lock()
	pending := b.pendingApprovals[requestID]
	if pending == nil {
		b.mu.Unlock()
		return fmt.Errorf("approval request %q is no longer pending", requestID)
	}
	resolved := event.Event{
		ID: watermill.NewUUID(), Timestamp: time.Now(), Backend: "app",
		Kind: "approval.resolved", Summary: string(decision),
		Approval: &event.ApprovalRequest{RequestID: requestID}, Decision: decision,
	}
	if b.eventBus != nil {
		if err := b.eventBus.Publish(ctx, resolved); err != nil {
			b.mu.Unlock()
			return err
		}
	}
	delete(b.pendingApprovals, requestID)
	pending <- decision
	b.mu.Unlock()
	return nil
}

func (b *Backend) requestApproval(ctx context.Context, approval event.ApprovalRequest) (event.ApprovalDecision, error) {
	approval.RequestID = watermill.NewUUID()
	decision := make(chan event.ApprovalDecision, 1)
	b.mu.Lock()
	if b.pendingApprovals == nil {
		b.pendingApprovals = make(map[string]chan event.ApprovalDecision)
	}
	if b.eventBus == nil {
		b.mu.Unlock()
		return event.ApprovalReject, fmt.Errorf("event bus is not available for approval request")
	}
	b.pendingApprovals[approval.RequestID] = decision
	eventBus := b.eventBus
	b.mu.Unlock()

	requested := event.Event{
		ID: approval.RequestID, Timestamp: time.Now(), Backend: "codex",
		Kind: "approval.requested", ThreadID: approval.ThreadID, TurnID: approval.TurnID,
		ItemID: approval.ItemID, Summary: approvalSummary(approval), Approval: &approval,
	}
	if err := eventBus.Publish(ctx, requested); err != nil {
		b.mu.Lock()
		delete(b.pendingApprovals, approval.RequestID)
		b.mu.Unlock()
		return event.ApprovalReject, err
	}
	select {
	case result := <-decision:
		return result, nil
	case <-ctx.Done():
		select {
		case result := <-decision:
			return result, nil
		default:
		}
		b.mu.Lock()
		_, stillPending := b.pendingApprovals[approval.RequestID]
		delete(b.pendingApprovals, approval.RequestID)
		if stillPending && b.eventBus != nil {
			_ = b.eventBus.Publish(context.Background(), event.Event{
				ID: watermill.NewUUID(), Timestamp: time.Now(), Backend: "app",
				Kind: "approval.resolved", Summary: "request canceled",
				Approval: &event.ApprovalRequest{RequestID: approval.RequestID}, Decision: event.ApprovalReject,
			})
		}
		b.mu.Unlock()
		return event.ApprovalReject, ctx.Err()
	}
}

func approvalSummary(approval event.ApprovalRequest) string {
	switch approval.Kind {
	case "command":
		return "Command execution approval requested"
	case "file_change":
		return "File change approval requested"
	case "permissions":
		return "Permissions approval requested"
	case "mcp_tool":
		return "MCP tool approval requested"
	default:
		return "Approval requested"
	}
}

func (b *Backend) setIdle() {
	b.mu.Lock()
	b.busy = false
	b.turnID = ""
	b.interruptRequested = false
	b.mu.Unlock()
}

func (b *Backend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rejectPendingApprovalsLocked()
	if b.sub != nil {
		b.sub.Close()
		b.sub = nil
	}
	if b.thread != nil {
		b.thread.Close()
		b.thread = nil
	}
	b.threadID, b.turnID = "", ""
	if b.client != nil {
		err := b.client.Close()
		b.client = nil
		return err
	}
	return nil
}

func normalize(method string, raw json.RawMessage) event.Event {
	return normalizeWithValue(method, raw, nil)
}

func normalizeEvent(sdkEvent codexgo.Event) event.Event {
	return normalizeWithValue(sdkEvent.Method, sdkEvent.Raw, sdkEvent.Value)
}

func normalizeWithValue(method string, raw json.RawMessage, value any) event.Event {
	data := make(map[string]any)
	_ = json.Unmarshal(raw, &data)
	item := objectField(data, "item")
	itemType := stringField(item, "type")
	kind := methodKind(method)
	if method == "item/started" || method == "item/completed" {
		kind = itemMethodKind(method, itemType)
	}
	summary := method
	switch kind {
	case "message.delta":
		summary = messageDeltaText(data, value)
		if summary == "" {
			summary = method
		}
	case "command.started":
		summary = commandSummary(item, false)
	case "command.completed":
		summary = commandSummary(item, true)
	case "command.output":
		summary = commandOutput(data)
	case "file.changed":
		summary = fileSummary(changesFrom(data, item))
	case "file.output":
		summary = stringField(data, "output")
		if summary == "" {
			summary = stringField(data, "delta")
		}
	case "tool.started", "tool.completed":
		summary = toolSummary(item, kind == "tool.completed")
	case "tool.progress":
		summary = stringField(data, "message")
	case "status.update":
		summary = statusSummary(method, data, item)
	case "usage.updated":
		summary = usageSummary(data)
	case "error":
		summary = firstString(data, "message", "error")
	case "warning":
		summary = firstString(data, "message", "summary", "details")
	default:
		if itemType != "" {
			summary = kind + " " + itemType
		}
	}
	itemID := stringField(data, "itemId")
	if itemID == "" {
		itemID = stringField(item, "id")
	}
	if kind == "command.output" {
		delta, _ := data["deltaBase64"].(string)
		if delta != "" {
			if decoded, err := base64.StdEncoding.DecodeString(delta); err == nil {
				summary = string(decoded)
			}
		}
	}
	return event.Event{
		ID: watermill.NewUUID(), Timestamp: time.Now(), Backend: "codex", Kind: kind,
		ThreadID: stringField(data, "threadId"), TurnID: stringField(data, "turnId"),
		ItemID: itemID, Source: method, Summary: summary,
		Data: data, Raw: append(json.RawMessage(nil), raw...),
	}
}

func messageDeltaText(data map[string]any, value any) string {
	if sdkEvent, ok := value.(codexgo.ItemAgentMessageDeltaEvent); ok {
		if sdkEvent.Text != "" {
			return sdkEvent.Text
		}
		if text := rawDeltaText(sdkEvent.Delta); text != "" {
			return text
		}
	}
	if text, ok := data["text"].(string); ok {
		return text
	}
	if delta, ok := data["delta"]; ok {
		if text, ok := delta.(string); ok {
			return text
		}
		if fields, ok := delta.(map[string]any); ok {
			if text, ok := fields["text"].(string); ok {
				return text
			}
		}
	}
	return ""
}

func rawDeltaText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var fields map[string]any
	if json.Unmarshal(raw, &fields) == nil {
		if text, ok := fields["text"].(string); ok {
			return text
		}
	}
	return ""
}

func methodKind(method string) string {
	switch method {
	case "thread/started":
		return "thread.started"
	case "turn/started":
		return "turn.started"
	case "turn/completed":
		return "turn.completed"
	case "item/started":
		return "item.started"
	case "item/completed":
		return "item.completed"
	case "item/agentMessage/delta":
		return "message.delta"
	case "item/commandExecution/outputDelta", "command/exec/outputDelta", "process/outputDelta":
		return "command.output"
	case "item/fileChange/patchUpdated":
		return "file.changed"
	case "item/fileChange/outputDelta":
		return "file.output"
	case "thread/tokenUsage/updated":
		return "usage.updated"
	case "item/mcpToolCall/progress":
		return "tool.progress"
	case "item/plan/delta", "turn/plan/updated", "item/reasoning/summaryTextDelta", "item/reasoning/textDelta", "item/reasoning/summaryPartAdded":
		return "status.update"
	case "warning", "configWarning", "guardianWarning", "deprecationNotice":
		return "warning"
	case "error":
		return "error"
	default:
		return "backend.unknown"
	}
}

func itemMethodKind(method, itemType string) string {
	switch itemType {
	case "commandExecution":
		if method == "item/started" {
			return "command.started"
		}
		return "command.completed"
	case "fileChange":
		return "file.changed"
	case "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "subAgentActivity", "webSearch", "imageView", "imageGeneration":
		if method == "item/started" {
			return "tool.started"
		}
		return "tool.completed"
	case "reasoning", "plan":
		return "status.update"
	case "agentMessage":
		if method == "item/started" {
			return "message.started"
		}
		return "message.completed"
	default:
		return methodKind(method)
	}
}

func commandSummary(item map[string]any, completed bool) string {
	command := stringField(item, "command")
	if command == "" {
		command = "Command"
	}
	if !completed {
		return command
	}
	parts := []string{command}
	if code, ok := numberField(item, "exitCode"); ok {
		parts = append(parts, fmt.Sprintf("exit %d", int64(code)))
	} else if status := stringField(item, "status"); status != "" {
		parts = append(parts, status)
	}
	if duration, ok := numberField(item, "durationMs"); ok {
		parts = append(parts, fmt.Sprintf("%dms", int64(duration)))
	}
	return strings.Join(parts, " · ")
}

func commandOutput(data map[string]any) string {
	if output := stringField(data, "output"); output != "" {
		return output
	}
	if delta := stringField(data, "delta"); delta != "" {
		return delta
	}
	if encoded := stringField(data, "deltaBase64"); encoded != "" {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err == nil {
			return string(decoded)
		}
		return "[invalid base64 command output]"
	}
	return ""
}

func fileSummary(changes []any) string {
	if len(changes) == 0 {
		return "File changes updated"
	}
	parts := make([]string, 0, min(len(changes), 8))
	for i, raw := range changes {
		if i == 8 {
			parts = append(parts, fmt.Sprintf("+%d more", len(changes)-i))
			break
		}
		change, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		path := stringField(change, "path")
		kind := stringField(change, "kind")
		if path == "" {
			continue
		}
		if kind != "" {
			parts = append(parts, kind+" "+path)
		} else {
			parts = append(parts, path)
		}
	}
	return strings.Join(parts, ", ")
}

func changesFrom(data, item map[string]any) []any {
	if changes, ok := data["changes"].([]any); ok {
		return changes
	}
	if changes, ok := item["changes"].([]any); ok {
		return changes
	}
	return nil
}

func toolSummary(item map[string]any, completed bool) string {
	name := stringField(item, "tool")
	if namespace := stringField(item, "namespace"); namespace != "" {
		name = namespace + "." + name
	}
	if server := stringField(item, "server"); server != "" {
		name = server + "/" + name
	}
	if name == "" {
		name = "Tool"
	}
	if completed {
		status := stringField(item, "status")
		if success, ok := item["success"].(bool); ok {
			if success {
				status = "succeeded"
			} else {
				status = "failed"
			}
		}
		if status != "" {
			return name + " · " + status
		}
		return name + " · completed"
	}
	return name
}

func statusSummary(method string, data, item map[string]any) string {
	if text := firstString(data, "text", "delta", "summary"); text != "" {
		return text
	}
	if text := firstString(item, "text", "summary", "content"); text != "" {
		return text
	}
	if method == "turn/plan/updated" {
		if plan, ok := data["plan"]; ok {
			return compactJSON(plan, 500)
		}
	}
	if method == "item/reasoning/summaryPartAdded" {
		return "Reasoning summary updated"
	}
	return "Status updated"
}

func usageSummary(data map[string]any) string {
	usage, _ := data["tokenUsage"].(map[string]any)
	if usage == nil {
		usage, _ = data["usage"].(map[string]any)
	}
	last, _ := usage["last"].(map[string]any)
	if last == nil {
		last = usage
	}
	input, _ := numberField(last, "inputTokens")
	output, _ := numberField(last, "outputTokens")
	total, totalOK := numberField(last, "totalTokens")
	if !totalOK {
		total = input + output
	}
	parts := []string{fmt.Sprintf("%d in · %d out · %d total tokens", int64(input), int64(output), int64(total))}
	if window, ok := numberField(usage, "modelContextWindow"); ok && window > 0 {
		parts = append(parts, fmt.Sprintf("context %d", int64(window)))
	}
	return strings.Join(parts, " · ")
}

func compactJSON(value any, limit int) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "Details unavailable"
	}
	text := string(encoded)
	if len(text) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "…"
	}
	return text
}

func objectField(values map[string]any, field string) map[string]any {
	value, _ := values[field].(map[string]any)
	return value
}

func firstString(values map[string]any, fields ...string) string {
	for _, field := range fields {
		if value := stringField(values, field); value != "" {
			return value
		}
	}
	return ""
}

func numberField(values map[string]any, field string) (float64, bool) {
	value, ok := values[field].(float64)
	return value, ok
}

func stringField(data map[string]any, field string) string {
	value, _ := data[field].(string)
	return value
}
