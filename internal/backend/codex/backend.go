package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/xiy/ruga/internal/bus"
	"github.com/xiy/ruga/internal/event"
	codexgo "github.com/zealbase/codex-app-server-go"
)

type Backend struct {
	binary   string
	cwd      string
	mu       sync.Mutex
	client   *codexgo.Client
	thread   *codexgo.SessionThread
	sub      *codexgo.EventSubscription
	eventBus bus.Bus
	busy     bool
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
	)
	if err != nil {
		return fmt.Errorf("connect to Codex App Server: %w", err)
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
	b.thread = thread
	b.mu.Unlock()
	return nil
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
	client, thread, eventBus := b.client, b.thread, b.eventBus
	threadID := thread.ID()
	b.mu.Unlock()

	if err := eventBus.Publish(ctx, event.Event{
		ID: watermill.NewUUID(), Timestamp: time.Now(), Backend: "app",
		Kind: "user.message", ThreadID: threadID, Summary: prompt,
		Data: map[string]any{"text": prompt},
	}); err != nil {
		b.setIdle()
		return err
	}
	if _, err := client.TurnStart(ctx, codexgo.TurnStartRequest{ThreadID: threadID, Input: prompt}); err != nil {
		b.setIdle()
		_ = eventBus.Publish(ctx, event.Event{
			ID: watermill.NewUUID(), Timestamp: time.Now(), Backend: "codex",
			Kind: "error", ThreadID: threadID, Summary: "Could not submit prompt: " + err.Error(),
		})
		return err
	}
	return nil
}

func (b *Backend) setIdle() {
	b.mu.Lock()
	b.busy = false
	b.mu.Unlock()
}

func (b *Backend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sub != nil {
		b.sub.Close()
		b.sub = nil
	}
	if b.thread != nil {
		b.thread.Close()
		b.thread = nil
	}
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
	kind, summary := methodKind(method), method
	if method == "item/agentMessage/delta" {
		summary = messageDeltaText(data, value)
		if summary == "" {
			summary = method
		}
	} else if method == "error" {
		if text, ok := data["message"].(string); ok {
			summary = text
		}
	} else if item, ok := data["item"].(map[string]any); ok {
		if itemType, ok := item["type"].(string); ok {
			summary = kind + " " + itemType
		}
	}
	return event.Event{
		ID: watermill.NewUUID(), Timestamp: time.Now(), Backend: "codex", Kind: kind,
		ThreadID: stringField(data, "threadId"), TurnID: stringField(data, "turnId"),
		ItemID: stringField(data, "itemId"), Source: method, Summary: summary,
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
	case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		return "status.update"
	case "error":
		return "error"
	default:
		return "backend.unknown"
	}
}

func stringField(data map[string]any, field string) string {
	value, _ := data[field].(string)
	return value
}
