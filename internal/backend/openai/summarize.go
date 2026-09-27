package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/haha-systems/ruga/internal/session"
)

// summarizerInstruction is the system prompt for model-driven compaction. It
// asks for a continuation summary rather than a general recap, so the next
// request can proceed without the original turns.
const summarizerInstruction = "Summarise the conversation so far for your own continuation. " +
	"Preserve the user's goals, decisions, constraints, and unresolved threads. " +
	"Be concise and factual; omit pleasantries."

// modelSummary asks the model to summarise the dropped turns for continuation.
// It is best-effort: on any error the caller falls back to the deterministic
// summary, so compaction never blocks a turn.
func (b *Backend) modelSummary(ctx context.Context, priorSummary string, dropped []session.Message, maxBytes int) (string, error) {
	transcript := renderTranscript(dropped)
	if strings.TrimSpace(transcript) == "" {
		return "", fmt.Errorf("no dropped content to summarise")
	}

	request := completionRequest{
		Model: b.model,
		Messages: []message{
			{Role: "system", Content: summarizerInstruction},
			{Role: "user", Content: transcript},
		},
	}

	content, err := b.complete(ctx, request)
	if err != nil {
		return "", err
	}

	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("summariser returned no content")
	}

	header := fmt.Sprintf("%s %d earlier messages summarised.\n", compactionMarker, len(dropped))
	if body := summaryBody(priorSummary); body != "" {
		header += body
	}

	return truncateUTF8(header+content+"\n", maxBytes), nil
}

// complete performs a single non-streaming completion and returns the assistant
// text.
func (b *Backend) complete(ctx context.Context, payload completionRequest) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode OpenAI-compatible request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create OpenAI-compatible request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if b.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.apiKey)
	}

	response, err := b.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("send OpenAI-compatible request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		return "", fmt.Errorf("OpenAI-compatible request returned %s: %s", response.Status, strings.TrimSpace(string(responseBody)))
	}

	var decoded completionResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return "", fmt.Errorf("decode OpenAI-compatible response: %w", err)
	}

	if decoded.Error != nil {
		return "", fmt.Errorf("OpenAI-compatible response error: %s", decoded.Error.Message)
	}

	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("OpenAI-compatible response contained no choices")
	}

	return decoded.Choices[0].Message.Content, nil
}

// renderTranscript flattens dropped messages into a compact text transcript for
// the summariser, labelling roles so the model can tell requests from results.
func renderTranscript(dropped []session.Message) string {
	var builder strings.Builder
	for _, message := range dropped {
		label := message.Role
		if message.Name != "" {
			label += " " + message.Name
		}

		content := strings.TrimSpace(message.Content)
		if len(message.ToolCalls) > 0 {
			var calls []string
			for _, call := range message.ToolCalls {
				calls = append(calls, call.Name+" "+call.Arguments)
			}

			if content != "" {
				content += " "
			}

			content += "[tool calls: " + strings.Join(calls, "; ") + "]"
		}

		if content == "" {
			continue
		}

		fmt.Fprintf(&builder, "%s: %s\n", label, content)
	}

	return strings.TrimSpace(builder.String())
}
