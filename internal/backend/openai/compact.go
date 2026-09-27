package openai

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/haha-systems/ruga/internal/bus"
	"github.com/haha-systems/ruga/internal/event"
	"github.com/haha-systems/ruga/internal/session"
)

// DefaultContextLimit is the conservative window assumed when a model does not
// declare one. Compatible providers report token usage but not always the
// model's context window, so compaction needs a ceiling to reason about.
const DefaultContextLimit = 128000

const (
	compactionNumerator   = 3
	compactionDenominator = 4
	defaultKeepTurns      = 4
	maxSummaryBytes       = 2048
	compactionMarker      = "[compacted history]"
)

// contextLimitOrDefault reports the configured window without locking, so it is
// safe to call while the backend mutex is held.
func (b *Backend) contextLimitOrDefault() int {
	if b.contextLimit > 0 {
		return b.contextLimit
	}

	return DefaultContextLimit
}

func (b *Backend) contextLimitValue() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.contextLimitOrDefault()
}

// maybeCompact reduces the conversation when the estimated or reported token
// count crosses the compaction threshold. It rewrites both persisted and
// in-memory history and publishes context.compacted, reporting whether it
// changed anything.
func (b *Backend) maybeCompact(ctx context.Context, eventBus bus.Bus, threadID, turnID string, history []session.Message, reportedTokens int) ([]session.Message, bool, error) {
	limit := b.contextLimitValue()
	threshold := limit * compactionNumerator / compactionDenominator
	tokens := estimatedTokens(history)
	if reportedTokens > tokens {
		tokens = reportedTokens
	}

	if tokens < threshold {
		return history, false, nil
	}

	reduced, summary, dropped := compactMessages(history, defaultKeepTurns, maxSummaryBytes)
	if dropped == 0 {
		return history, false, nil
	}

	if err := b.replaceMessages(reduced); err != nil {
		return history, false, err
	}

	if err := publishCompacted(ctx, eventBus, threadID, turnID, dropped, defaultKeepTurns, summary); err != nil {
		return history, false, err
	}

	return reduced, true, nil
}

// estimatedTokens approximates the conversation size with the (bytes+3)/4 idiom
// used elsewhere for tool results. It over-counts slightly, which is the safe
// direction for a fallback trigger.
func estimatedTokens(history []session.Message) int {
	bytes := 0
	for _, message := range history {
		bytes += len(message.Role) + len(message.Content)
		for _, call := range message.ToolCalls {
			bytes += len(call.Name) + len(call.Arguments)
		}
	}

	return (bytes + 3) / 4
}

// compactMessages keeps the most recent keepTurns turns verbatim and replaces
// everything older with one synthetic system summary. Turns are atomic: an
// assistant message, its tool calls, and their results stay together, so a
// result is never orphaned from the call it answers.
func compactMessages(history []session.Message, keepTurns, maxBytes int) ([]session.Message, string, int) {
	priorSummary := ""
	body := history
	if summary, ok := leadingSummary(history); ok {
		priorSummary = summary
		body = history[1:]
	}

	turns := splitTurns(body)
	if keepTurns < 1 {
		keepTurns = 1
	}

	if len(turns) <= keepTurns {
		return history, "", 0
	}

	dropCount := len(turns) - keepTurns
	dropped := flattenTurns(turns[:dropCount])
	kept := flattenTurns(turns[dropCount:])
	summary := summarize(dropped, priorSummary, maxBytes)

	reduced := make([]session.Message, 0, len(kept)+1)
	reduced = append(reduced, session.Message{Role: "system", Content: summary})
	reduced = append(reduced, kept...)

	return reduced, summary, len(dropped)
}

// leadingSummary returns the content of a prior compaction summary, which is
// preserved as a prefix rather than treated as a droppable turn. Without this a
// compaction summary would itself count as a turn and re-trigger on the next
// round.
func leadingSummary(history []session.Message) (string, bool) {
	if len(history) == 0 || history[0].Role != "system" {
		return "", false
	}

	if !strings.HasPrefix(strings.TrimSpace(history[0].Content), compactionMarker) {
		return "", false
	}

	return history[0].Content, true
}

// splitTurns groups messages into turns that begin at each user message.
func splitTurns(history []session.Message) [][]session.Message {
	var turns [][]session.Message
	start := 0
	for i, message := range history {
		if message.Role == "user" && i != start {
			turns = append(turns, history[start:i])
			start = i
		}
	}

	if start < len(history) {
		turns = append(turns, history[start:])
	}

	return turns
}

func flattenTurns(turns [][]session.Message) []session.Message {
	var flat []session.Message
	for _, turn := range turns {
		flat = append(flat, turn...)
	}

	return flat
}

// summarize produces a deterministic, bounded summary of dropped messages. It
// retains recent user requests as one-line snippets and folds in any prior
// summary so earlier context is not silently lost.
func summarize(dropped []session.Message, priorSummary string, maxBytes int) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "%s %d earlier messages omitted.\n", compactionMarker, len(dropped))

	const (
		maxSnippets = 6
		snippetLen  = 100
	)
	snippets := 0
	for i := len(dropped) - 1; i >= 0 && snippets < maxSnippets; i-- {
		if dropped[i].Role != "user" {
			continue
		}

		snippet := oneLine(dropped[i].Content)
		if snippet == "" {
			continue
		}

		if len(snippet) > snippetLen {
			snippet = truncateUTF8(snippet, snippetLen) + "…"
		}

		fmt.Fprintf(&builder, "- user: %s\n", snippet)
		snippets++
	}

	if body := summaryBody(priorSummary); body != "" {
		builder.WriteString(body)
	}

	return truncateUTF8(builder.String(), maxBytes)
}

// summaryBody strips the leading marker line from a prior summary so folding it
// into a new one does not duplicate the header.
func summaryBody(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}

	if !strings.HasPrefix(summary, compactionMarker) {
		return summary + "\n"
	}

	if _, rest, found := strings.Cut(summary, "\n"); found {
		return strings.TrimSpace(rest) + "\n"
	}

	return ""
}

func oneLine(value string) string { return strings.Join(strings.Fields(value), " ") }

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}

	trimmed := value[:maxBytes]

	for !utf8.ValidString(trimmed) {
		trimmed = trimmed[:len(trimmed)-1]
	}

	return trimmed
}

func publishCompacted(ctx context.Context, eventBus bus.Bus, threadID, turnID string, droppedMessages, keptTurns int, summary string) error {
	return publishTo(ctx, eventBus, event.Event{
		Backend: "openai", Kind: "context.compacted", ThreadID: threadID, TurnID: turnID,
		Source:  "openai.compaction",
		Summary: fmt.Sprintf("compacted %d messages · kept %d turns", droppedMessages, keptTurns),
		Data: map[string]any{
			"dropped_messages": droppedMessages,
			"kept_turns":       keptTurns,
			"summary_bytes":    len(summary),
		},
	})
}
