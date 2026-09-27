package openai

import (
	"strings"
	"testing"

	"github.com/haha-systems/ruga/internal/session"
)

func TestCompactMessagesKeepsRecentTurnsAndSummarizesOlder(t *testing.T) {
	history := []session.Message{
		{Role: "user", Content: "first request"},
		{Role: "assistant", Content: "calling echo", ToolCalls: []session.ToolCall{{ID: "call-1", Name: "echo", Arguments: `{"text":"one"}`}}},
		{Role: "tool", Name: "echo", ToolCallID: "call-1", Content: "one"},
		{Role: "user", Content: "second request"},
		{Role: "assistant", Content: "answer two"},
		{Role: "user", Content: "third request"},
		{Role: "assistant", Content: "answer three"},
	}

	reduced, summary, dropped := compactMessages(history, 2, maxSummaryBytes)
	if dropped != 3 {
		t.Fatalf("dropped = %d, want 3 messages from the first turn", dropped)
	}

	if summary == "" || !strings.Contains(summary, compactionMarker) {
		t.Fatalf("summary = %q, want compaction marker", summary)
	}

	if len(reduced) != 5 {
		t.Fatalf("reduced length = %d, want summary + two kept turns: %+v", len(reduced), reduced)
	}

	if reduced[0].Role != "system" {
		t.Fatalf("first reduced message = %q, want system summary", reduced[0].Role)
	}

	wantTail := []string{"second request", "answer two", "third request", "answer three"}
	got := []string{reduced[1].Content, reduced[2].Content, reduced[3].Content, reduced[4].Content}
	for i, want := range wantTail {
		if got[i] != want {
			t.Fatalf("kept message %d = %q, want %q", i, got[i], want)
		}
	}
}

func TestCompactMessagesKeepsToolCallAndResultTogether(t *testing.T) {
	history := []session.Message{
		{Role: "user", Content: "old"},
		{Role: "assistant", ToolCalls: []session.ToolCall{{ID: "call-old", Name: "echo", Arguments: `{}`}}},
		{Role: "tool", ToolCallID: "call-old", Content: "old result"},
		{Role: "user", Content: "new"},
		{Role: "assistant", Content: "answer"},
	}

	reduced, _, _ := compactMessages(history, 1, maxSummaryBytes)
	for _, message := range reduced {
		if message.Role == "tool" && message.ToolCallID == "call-old" {
			t.Fatalf("orphaned tool result survived compaction: %+v", message)
		}

		if message.ToolCallID == "call-old" {
			t.Fatalf("orphaned tool call survived compaction: %+v", message)
		}
	}

	if len(reduced) != 3 || reduced[1].Content != "new" || reduced[2].Content != "answer" {
		t.Fatalf("kept turn = %+v", reduced)
	}
}

func TestCompactMessagesIsNoOpWhenTurnsFit(t *testing.T) {
	history := []session.Message{
		{Role: "user", Content: "only"},
		{Role: "assistant", Content: "turn"},
	}

	reduced, summary, dropped := compactMessages(history, 4, maxSummaryBytes)
	if dropped != 0 || summary != "" || len(reduced) != 2 {
		t.Fatalf("no-op compaction changed history: %+v", reduced)
	}
}

func TestCompactMessagesFoldsPriorSummary(t *testing.T) {
	history := []session.Message{
		{Role: "system", Content: compactionMarker + " 3 earlier messages omitted.\n- user: ancient request\n"},
		{Role: "user", Content: "recent one"},
		{Role: "assistant", Content: "answer one"},
		{Role: "user", Content: "recent two"},
		{Role: "assistant", Content: "answer two"},
	}

	reduced, summary, _ := compactMessages(history, 1, maxSummaryBytes)
	if !strings.Contains(summary, "ancient request") {
		t.Fatalf("prior summary was dropped: %q", summary)
	}

	if strings.Count(summary, compactionMarker) != 1 {
		t.Fatalf("summary marker duplicated: %q", summary)
	}

	if reduced[0].Role != "system" {
		t.Fatalf("reduced[0] = %+v, want system summary", reduced[0])
	}
}

func TestCompactMessagesDoesNotRetriggerOnOwnSummary(t *testing.T) {
	// A leading summary must never be counted as a droppable turn, or a
	// conversation that stays over the threshold would compact every round.
	history := []session.Message{
		{Role: "system", Content: compactionMarker + " 6 earlier messages omitted.\n"},
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "a"},
		{Role: "user", Content: "two"},
		{Role: "assistant", Content: "b"},
	}

	if _, _, dropped := compactMessages(history, 2, maxSummaryBytes); dropped != 0 {
		t.Fatalf("compaction re-triggered on its own summary: dropped = %d", dropped)
	}
}

func TestEstimatedTokensCountsContentAndCalls(t *testing.T) {
	small := []session.Message{{Role: "user", Content: "hi"}}
	large := []session.Message{
		{Role: "user", Content: strings.Repeat("x", 400)},
		{Role: "assistant", ToolCalls: []session.ToolCall{{ID: "1", Name: "echo", Arguments: strings.Repeat("y", 400)}}},
	}

	if estimatedTokens(large) <= estimatedTokens(small) {
		t.Fatalf("estimate did not grow with content: small=%d large=%d", estimatedTokens(small), estimatedTokens(large))
	}
}
