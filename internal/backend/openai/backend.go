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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ThreeDotsLabs/watermill"

	"github.com/haha-systems/ruga/internal/backend"
	"github.com/haha-systems/ruga/internal/bus"
	"github.com/haha-systems/ruga/internal/event"
	"github.com/haha-systems/ruga/internal/session"
	"github.com/haha-systems/ruga/internal/tool"
)

const (
	defaultBaseURL = "https://api.openai.com/v1"
	maxToolRounds  = 64
)

// parallelToolInstruction nudges compatible servers toward issuing independent
// tool calls together, since many default to one call per round trip.
const parallelToolInstruction = "You may request several tool calls in one response. " +
	"Issue independent tool calls together instead of one per turn; " +
	"only serialize calls when a later call depends on an earlier result."

// Config contains connection settings for an OpenAI-compatible endpoint.
type Config struct {
	BaseURL   string
	Model     string
	APIKey    string
	APIKeyEnv string

	// ContextLimit is the model's context window in tokens. A conservative
	// default is used when it is zero or negative.
	ContextLimit int
}

// Backend keeps protocol details local to this adapter and publishes only
// application-owned events.
type Backend struct {
	baseURL      string
	model        string
	apiKey       string
	apiKeyEnv    string
	contextLimit int
	client       *http.Client

	mu       sync.Mutex
	eventBus bus.Bus
	session  string
	history  []message
	state    session.Session
	save     func(session.Session) error
	resuming bool
	busy     bool
	tools    *tool.Registry
}

func New(config Config) *Backend {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	return &Backend{
		baseURL:      baseURL,
		model:        strings.TrimSpace(config.Model),
		apiKey:       config.APIKey,
		apiKeyEnv:    config.APIKeyEnv,
		contextLimit: config.ContextLimit,
		client:       http.DefaultClient,
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
	if b.state.ID == "" {
		b.state = session.New("openai", "")
	}

	b.state.Backend = "openai"
	b.state.Provider = b.baseURL
	b.state.Model = b.model
	b.state.CredentialEnv = b.apiKeyEnv
	b.session = b.state.ID
	b.history = make([]message, 0, len(b.state.Messages))
	for _, saved := range b.state.Messages {
		b.history = append(b.history, messageFromSession(saved))
	}

	state := b.state
	save := b.save
	b.mu.Unlock()
	if save != nil {
		if err := save(state); err != nil {
			b.mu.Lock()
			b.eventBus = nil
			b.mu.Unlock()
			return fmt.Errorf("persist OpenAI-compatible session: %w", err)
		}
	}

	if err := b.publish(ctx, event.Event{Kind: "backend.connected", Summary: "OpenAI-compatible backend connected"}); err != nil {
		b.mu.Lock()
		b.eventBus = nil
		b.mu.Unlock()
		return err
	}

	if b.resuming {
		turns := 0
		for _, saved := range b.history {
			if saved.Role == "assistant" {
				turns++
			}
		}

		summary := fmt.Sprintf("↻ resumed session · %d turns", turns)
		if cwd := session.ShortPath(state.CWD); cwd != "" {
			summary += " · " + cwd
		}

		if err := b.publish(ctx, event.Event{Kind: "session.resumed", Summary: summary}); err != nil {
			return err
		}
	}

	return nil
}

func (b *Backend) ConfigureSession(state session.Session, resuming bool, save func(session.Session) error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state, b.save, b.resuming = state, save, resuming
}

func (b *Backend) ConfigureTools(registry *tool.Registry) {
	b.mu.Lock()
	b.tools = registry
	b.mu.Unlock()
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
	conversation := append([]session.Message(nil), b.state.Messages...)
	threadID := b.session
	eventBus := b.eventBus
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.busy = false
		b.mu.Unlock()
	}()

	user := session.Message{Role: "user", Content: prompt}
	if err := b.persistMessages(user); err != nil {
		return fmt.Errorf("persist OpenAI-compatible user message: %w", err)
	}

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

	turnStarted := time.Now()
	toolCallCount, toolResultBytes := 0, 0
	reportedTokens := 0
	var toolDuration, toolWallDuration time.Duration
	for round := 0; round <= maxToolRounds; round++ {
		reduced, compacted, err := b.maybeCompact(ctx, eventBus, threadID, turnID, conversation, reportedTokens)
		if err != nil {
			return b.failTurn(ctx, eventBus, threadID, turnID, fmt.Errorf("compact OpenAI-compatible context: %w", err))
		}

		if compacted {
			conversation = reduced
			reportedTokens = 0
		}

		messages := messagesFrom(conversation)
		request := completionRequest{
			Model: b.model, Messages: messages, Stream: true,
			StreamOptions: &streamOptions{IncludeUsage: true},
		}
		b.mu.Lock()
		registry := b.tools
		b.mu.Unlock()
		if registry != nil {
			for _, definition := range registry.Definitions() {
				schema := definition.Schema
				schema.Description = ""
				request.Tools = append(request.Tools, toolDefinition{
					Type: "function",
					Function: toolFunctionSchema{
						Name: definition.Name, Description: definition.Schema.Description, Parameters: schema,
					},
				})
			}

			if len(request.Tools) > 0 {
				parallel := true
				request.ParallelToolCalls = &parallel
				request.Messages = append([]message{{Role: "system", Content: parallelToolInstruction}}, messages...)
			}
		}

		response, err := b.stream(ctx, eventBus, threadID, turnID, request)
		if err != nil {
			return b.failTurn(ctx, eventBus, threadID, turnID, err)
		}

		if len(response.ToolCalls) > 0 && round >= maxToolRounds {
			return b.failTurn(ctx, eventBus, threadID, turnID, fmt.Errorf("OpenAI-compatible tool loop exceeded %d rounds", maxToolRounds))
		}

		if response.Usage != nil {
			if err := publishUsage(ctx, eventBus, threadID, turnID, response.Usage); err != nil {
				return err
			}

			reportedTokens = response.Usage.PromptTokens
		}

		toolCallCount += len(response.ToolCalls)
		assistant := session.Message{Role: "assistant", Content: response.Content}
		for _, call := range response.ToolCalls {
			assistant.ToolCalls = append(assistant.ToolCalls, session.ToolCall{
				ID: call.ID, Name: call.FunctionCall.Name, Arguments: call.FunctionCall.Arguments,
			})
		}

		if err := b.persistMessages(assistant); err != nil {
			return b.failTurn(ctx, eventBus, threadID, turnID, fmt.Errorf("persist OpenAI-compatible assistant message: %w", err))
		}

		conversation = append(conversation, assistant)
		if len(response.ToolCalls) == 0 {
			if err := publishTo(ctx, eventBus, event.Event{
				Backend: "openai", Kind: "message.completed", ThreadID: threadID, TurnID: turnID,
				Summary: response.Content,
			}); err != nil {
				return err
			}

			if err := publishTo(ctx, eventBus, event.Event{
				Backend: "openai", Kind: "turn.completed", ThreadID: threadID, TurnID: turnID,
				Data: map[string]any{
					"status": "completed", "tool_calls": toolCallCount, "tool_result_bytes": toolResultBytes,
					"tool_result_tokens_estimate": (toolResultBytes + 3) / 4,
					"tool_elapsed_ms":             toolDuration.Milliseconds(),
					"tool_wall_elapsed_ms":        toolWallDuration.Milliseconds(),
					"model_round_trips":           round + 1, "elapsed_ms": time.Since(turnStarted).Milliseconds(),
				},
			}); err != nil {
				return err
			}

			return nil
		}

		if response.Content != "" {
			if err := publishTo(ctx, eventBus, event.Event{
				Backend: "openai", Kind: "message.completed", ThreadID: threadID, TurnID: turnID,
				Summary: response.Content,
			}); err != nil {
				return err
			}
		}

		calls := make([]tool.Call, len(response.ToolCalls))
		for index, call := range response.ToolCalls {
			calls[index] = tool.Call{ID: call.ID, Name: call.FunctionCall.Name, Arguments: json.RawMessage(call.FunctionCall.Arguments)}
			if err := publishToolStarted(ctx, eventBus, threadID, turnID, calls[index]); err != nil {
				return err
			}
		}

		batchStarted := time.Now()
		results := executeToolCalls(ctx, registry, calls)
		toolWallDuration += time.Since(batchStarted)
		for _, result := range results {
			toolDuration += result.Duration
			toolResultBytes += len(result.Result.Content)
			toolMessage := session.Message{
				Role: "tool", Name: result.Call.Name, ToolCallID: result.Call.ID, Content: result.Result.Content,
			}
			if err := b.persistMessages(toolMessage); err != nil {
				return b.failTurn(ctx, eventBus, threadID, turnID, fmt.Errorf("persist tool result %q: %w", result.Call.ID, err))
			}

			conversation = append(conversation, toolMessage)
			if err := publishToolCompleted(ctx, eventBus, threadID, turnID, result); err != nil {
				return err
			}
		}
	}

	return nil
}

func (b *Backend) stream(ctx context.Context, eventBus bus.Bus, threadID, turnID string, payload completionRequest) (assistantResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return assistantResponse{}, fmt.Errorf("encode OpenAI-compatible request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return assistantResponse{}, fmt.Errorf("create OpenAI-compatible request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if b.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.apiKey)
	}

	response, err := b.client.Do(req)
	if err != nil {
		return assistantResponse{}, fmt.Errorf("send OpenAI-compatible request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		return assistantResponse{}, fmt.Errorf("OpenAI-compatible request returned %s: %s", response.Status, strings.TrimSpace(string(responseBody)))
	}

	var answer strings.Builder
	toolCalls := make(map[int]*toolCall)
	var streamedUsage *usage
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

		if chunk.Usage != nil {
			streamedUsage = chunk.Usage
		}

		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
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

			for _, delta := range choice.Delta.ToolCalls {
				current := toolCalls[delta.Index]
				if current == nil {
					current = &toolCall{Type: "function"}
					toolCalls[delta.Index] = current
				}

				if delta.ID != "" {
					current.ID = delta.ID
				}

				if delta.Type != "" {
					current.Type = delta.Type
				}

				current.FunctionCall.Name += delta.Function.Name
				current.FunctionCall.Arguments += delta.Function.Arguments
			}
		}

		return nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := process(); err != nil {
				return assistantResponse{}, err
			}

			continue
		}

		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}

	if err := scanner.Err(); err != nil {
		return assistantResponse{}, fmt.Errorf("read OpenAI-compatible stream: %w", err)
	}

	if err := process(); err != nil {
		return assistantResponse{}, err
	}

	indices := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		indices = append(indices, index)
	}

	sort.Ints(indices)
	responseMessage := assistantResponse{Content: answer.String(), Usage: streamedUsage}
	for _, index := range indices {
		call := toolCalls[index]
		if call.ID == "" || call.FunctionCall.Name == "" {
			return assistantResponse{}, fmt.Errorf("OpenAI-compatible stream returned an incomplete tool call at index %d", index)
		}

		responseMessage.ToolCalls = append(responseMessage.ToolCalls, *call)
	}

	if !messageStarted && len(responseMessage.ToolCalls) == 0 {
		return assistantResponse{}, fmt.Errorf("OpenAI-compatible stream completed without assistant text or tool calls")
	}

	return responseMessage, nil
}

type assistantResponse struct {
	Content   string
	ToolCalls []toolCall
	Usage     *usage
}

// replaceMessages rewrites both the persisted and in-memory history, so a
// compaction survives resume instead of re-inflating the full transcript.
func (b *Backend) replaceMessages(messages []session.Message) error {
	b.mu.Lock()
	state := b.state
	state.Messages = append([]session.Message(nil), messages...)
	state.Summary = latestSummary(messages)
	state.CompactedAt = time.Now().UTC()
	state.UpdatedAt = state.CompactedAt
	b.state = state
	b.history = make([]message, 0, len(messages))
	for _, saved := range messages {
		b.history = append(b.history, messageFromSession(saved))
	}

	save := b.save
	b.mu.Unlock()
	if save != nil {
		if err := save(state); err != nil {
			return err
		}
	}

	return nil
}

func messagesFrom(messages []session.Message) []message {
	converted := make([]message, 0, len(messages))
	for _, saved := range messages {
		converted = append(converted, messageFromSession(saved))
	}

	return converted
}

func latestSummary(messages []session.Message) string {
	if len(messages) == 0 || messages[0].Role != "system" {
		return ""
	}

	return messages[0].Content
}

func (b *Backend) persistMessages(messages ...session.Message) error {
	b.mu.Lock()
	state := b.state
	state.Messages = append(append([]session.Message(nil), state.Messages...), messages...)
	state.UpdatedAt = time.Now().UTC()
	save := b.save
	b.mu.Unlock()
	if save != nil {
		if err := save(state); err != nil {
			return err
		}
	}

	b.mu.Lock()
	b.state = state
	for _, saved := range messages {
		b.history = append(b.history, messageFromSession(saved))
	}

	b.mu.Unlock()
	return nil
}

func messageFromSession(saved session.Message) message {
	result := message{Role: saved.Role, Content: saved.Content, Name: saved.Name, ToolCallID: saved.ToolCallID}
	for _, savedCall := range saved.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, toolCall{
			ID: savedCall.ID, Type: "function",
			FunctionCall: toolFunctionCall{Name: savedCall.Name, Arguments: savedCall.Arguments},
		})
	}

	return result
}

func (b *Backend) failTurn(_ context.Context, eventBus bus.Bus, threadID, turnID string, err error) error {
	_ = publishTo(context.Background(), eventBus, event.Event{
		Backend: "openai", Kind: "error", ThreadID: threadID, TurnID: turnID,
		Summary: err.Error(), Data: map[string]any{"status": "failed"},
	})
	return err
}

func executeToolCalls(ctx context.Context, registry *tool.Registry, calls []tool.Call) []tool.Execution {
	if registry != nil {
		return registry.Execute(ctx, calls)
	}

	results := make([]tool.Execution, len(calls))
	for index, call := range calls {
		results[index] = tool.Execution{Call: call, Result: tool.ToolResult{Content: "tool runtime is unavailable", IsError: true}}
	}

	return results
}

func publishUsage(ctx context.Context, eventBus bus.Bus, threadID, turnID string, reported *usage) error {
	total := reported.TotalTokens
	if total == 0 {
		total = reported.PromptTokens + reported.CompletionTokens
	}

	raw, _ := json.Marshal(reported)
	return publishTo(ctx, eventBus, event.Event{
		Backend: "openai", Kind: "usage.updated", ThreadID: threadID, TurnID: turnID,
		Source:  "chat.completion.chunk",
		Summary: fmt.Sprintf("%d in · %d out · %d total tokens", reported.PromptTokens, reported.CompletionTokens, total),
		Data: map[string]any{
			"input_tokens":  reported.PromptTokens,
			"output_tokens": reported.CompletionTokens,
			"total_tokens":  total,
		}, Raw: raw,
	})
}

func publishToolStarted(ctx context.Context, eventBus bus.Bus, threadID, turnID string, call tool.Call) error {
	raw, _ := json.Marshal(map[string]any{"id": call.ID, "name": call.Name, "arguments": string(call.Arguments)})
	return publishTo(ctx, eventBus, event.Event{
		Backend: "openai", Kind: "tool.started", ThreadID: threadID, TurnID: turnID, ItemID: call.ID,
		Source: "openai.tool_call", Summary: call.Name,
		Data: map[string]any{
			"tool_call_id": call.ID, "tool_name": call.Name, "arguments": string(call.Arguments),
			"arguments_bytes": len(call.Arguments),
		}, Raw: raw,
	})
}

func publishToolCompleted(ctx context.Context, eventBus bus.Bus, threadID, turnID string, execution tool.Execution) error {
	status := "succeeded"
	if execution.Result.IsError {
		status = "failed"
	}

	summary := execution.Call.Name + " · " + status
	if !execution.Result.IsError && execution.Result.Summary != "" {
		summary = execution.Result.Summary
	}

	raw, _ := json.Marshal(map[string]any{"tool_call_id": execution.Call.ID, "result": execution.Result.Content, "error": execution.Result.IsError})
	return publishTo(ctx, eventBus, event.Event{
		Backend: "openai", Kind: "tool.completed", ThreadID: threadID, TurnID: turnID, ItemID: execution.Call.ID,
		Source: "openai.tool_result", Summary: summary,
		Data: map[string]any{
			"tool_call_id": execution.Call.ID, "tool_name": execution.Call.Name, "result": execution.Result.Content,
			"error": execution.Result.IsError, "result_bytes": len(execution.Result.Content),
			"result_tokens_estimate": (len(execution.Result.Content) + 3) / 4,
			"elapsed_ms":             execution.Duration.Milliseconds(),
		}, Raw: raw,
	})
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

var (
	_ backend.Backend      = (*Backend)(nil)
	_ backend.ModelDisplay = (*Backend)(nil)
)
