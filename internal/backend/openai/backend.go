// Package openai implements an OpenAI-compatible Chat Completions backend.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/haha-systems/ruga/internal/backend"
	"github.com/haha-systems/ruga/internal/bus"
	"github.com/haha-systems/ruga/internal/event"
)

const defaultBaseURL = "https://api.openai.com/v1"

// Config contains connection settings for an OpenAI-compatible endpoint.
type Config struct {
	BaseURL string
	Model   string
	APIKey  string
}

// Backend keeps protocol details local to this adapter and publishes only
// application-owned events.
type Backend struct {
	baseURL string
	model   string
	apiKey  string
	client  *http.Client

	mu       sync.Mutex
	eventBus bus.Bus
	session  string
	history  []message
	busy     bool
}

func New(config Config) *Backend {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Backend{
		baseURL: baseURL,
		model:   strings.TrimSpace(config.Model),
		apiKey:  config.APIKey,
		client:  http.DefaultClient,
	}
}

func (b *Backend) Start(ctx context.Context, eventBus bus.Bus) error {
	if b.model == "" {
		return fmt.Errorf("OpenAI-compatible model is required")
	}
	b.mu.Lock()
	if b.eventBus != nil {
		b.mu.Unlock()
		return fmt.Errorf("OpenAI-compatible backend is already started")
	}
	b.eventBus = eventBus
	b.session = watermill.NewUUID()
	b.mu.Unlock()
	if err := b.publish(ctx, event.Event{Kind: "backend.connected", Summary: "OpenAI-compatible backend connected"}); err != nil {
		b.mu.Lock()
		b.eventBus = nil
		b.mu.Unlock()
		return err
	}
	return nil
}

func (b *Backend) ModelName() string { return b.model }

func (b *Backend) Close() error {
	b.mu.Lock()
	b.eventBus = nil
	b.history = nil
	b.mu.Unlock()
	return nil
}

func (b *Backend) Submit(ctx context.Context, prompt string) error {
	b.mu.Lock()
	if b.eventBus == nil {
		b.mu.Unlock()
		return fmt.Errorf("OpenAI-compatible backend is not started")
	}
	if b.busy {
		b.mu.Unlock()
		return fmt.Errorf("OpenAI-compatible backend is still working on the current turn")
	}
	b.busy = true
	conversation := append([]message(nil), b.history...)
	threadID := b.session
	eventBus := b.eventBus
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.busy = false
		b.mu.Unlock()
	}()

	user := message{Role: "user", Content: prompt}
	conversation = append(conversation, user)
	if err := publishTo(ctx, eventBus, event.Event{
		Backend: "openai", Kind: "user.message", ThreadID: threadID,
		Summary: prompt, Data: map[string]any{"text": prompt},
	}); err != nil {
		return err
	}
	turnID := watermill.NewUUID()
	if err := publishTo(ctx, eventBus, event.Event{
		Backend: "openai", Kind: "turn.started", ThreadID: threadID, TurnID: turnID,
	}); err != nil {
		return err
	}
	request := completionRequest{Model: b.model, Messages: conversation, Stream: true}
	if err := b.stream(ctx, eventBus, threadID, turnID, request); err != nil {
		_ = publishTo(context.Background(), eventBus, event.Event{
			Backend: "openai", Kind: "error", ThreadID: threadID, TurnID: turnID,
			Summary: err.Error(), Data: map[string]any{"status": "failed"},
		})
		return err
	}
	return nil
}

func (b *Backend) stream(ctx context.Context, eventBus bus.Bus, threadID, turnID string, payload completionRequest) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode OpenAI-compatible request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create OpenAI-compatible request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if b.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.apiKey)
	}
	response, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("send OpenAI-compatible request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		return fmt.Errorf("OpenAI-compatible request returned %s: %s", response.Status, strings.TrimSpace(string(responseBody)))
	}

	var answer strings.Builder
	messageStarted := false
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var dataLines []string
	process := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if data == "[DONE]" {
			return nil
		}
		var chunk completionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decode OpenAI-compatible stream event: %w", err)
		}
		if chunk.Error != nil {
			return fmt.Errorf("OpenAI-compatible stream error: %s", chunk.Error.Message)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content == "" {
				continue
			}
			if !messageStarted {
				if err := publishTo(ctx, eventBus, event.Event{
					Backend: "openai", Kind: "message.started", ThreadID: threadID, TurnID: turnID,
				}); err != nil {
					return err
				}
				messageStarted = true
			}
			answer.WriteString(choice.Delta.Content)
			raw, _ := json.Marshal(chunk)
			if err := publishTo(ctx, eventBus, event.Event{
				Backend: "openai", Kind: "message.delta", ThreadID: threadID, TurnID: turnID,
				Summary: choice.Delta.Content, Source: "chat.completion.chunk", Raw: raw,
				Data: map[string]any{"model": chunk.Model},
			}); err != nil {
				return err
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := process(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read OpenAI-compatible stream: %w", err)
	}
	if err := process(); err != nil {
		return err
	}
	if !messageStarted {
		return fmt.Errorf("OpenAI-compatible stream completed without assistant text")
	}
	if err := publishTo(ctx, eventBus, event.Event{
		Backend: "openai", Kind: "message.completed", ThreadID: threadID, TurnID: turnID,
		Summary: answer.String(),
	}); err != nil {
		return err
	}
	if err := publishTo(ctx, eventBus, event.Event{
		Backend: "openai", Kind: "turn.completed", ThreadID: threadID, TurnID: turnID,
		Data: map[string]any{"status": "completed"},
	}); err != nil {
		return err
	}
	b.mu.Lock()
	b.history = append(b.history, payload.Messages[len(payload.Messages)-1], message{Role: "assistant", Content: answer.String()})
	b.mu.Unlock()
	return nil
}

func (b *Backend) publish(ctx context.Context, ev event.Event) error {
	b.mu.Lock()
	eventBus := b.eventBus
	threadID := b.session
	b.mu.Unlock()
	ev.ThreadID = threadID
	if ev.Backend == "" {
		ev.Backend = "openai"
	}
	return publishTo(ctx, eventBus, ev)
}

func publishTo(ctx context.Context, eventBus bus.Bus, ev event.Event) error {
	ev.ID = watermill.NewUUID()
	ev.Timestamp = time.Now()
	if err := eventBus.Publish(ctx, ev); err != nil {
		return fmt.Errorf("publish OpenAI-compatible %s event: %w", ev.Kind, err)
	}
	return nil
}

var _ backend.Backend = (*Backend)(nil)
var _ backend.ModelDisplay = (*Backend)(nil)
