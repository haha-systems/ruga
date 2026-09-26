package presentation

import (
	"strings"
	"testing"

	"github.com/haha-systems/ruga/internal/event"
)

func TestReducerSeparatesConversationReasoningAndTelemetry(t *testing.T) {
	var model Model
	for _, ev := range []event.Event{
		{Kind: "turn.started", TurnID: "turn-1"},
		{Kind: "user.message", TurnID: "turn-1", Summary: "Explain the change"},
		{Kind: "message.started", TurnID: "turn-1", ItemID: "answer-1"},
		{Kind: "message.delta", TurnID: "turn-1", ItemID: "answer-1", Summary: "A clear "},
		{Kind: "status.update", TurnID: "turn-1", ItemID: "reason-1", Summary: "Checking "},
		{Kind: "status.update", TurnID: "turn-1", ItemID: "reason-1", Summary: "tests"},
		{Kind: "message.delta", TurnID: "turn-1", ItemID: "answer-1", Summary: "answer"},
		{Kind: "message.completed", TurnID: "turn-1", ItemID: "answer-1", Summary: "A clear answer"},
		{Kind: "turn.completed", TurnID: "turn-1", Data: map[string]any{"status": "completed"}},
	} {
		model.Apply(ev)
	}
	if len(model.Conversation) != 3 || len(model.Telemetry) != 1 {
		t.Fatalf("surfaces: conversation=%d telemetry=%d", len(model.Conversation), len(model.Telemetry))
	}
	if model.Conversation[1].Kind != ConversationAssistant || model.Conversation[1].Text != "A clear answer" || len(model.Conversation[1].Events) != 3 {
		t.Fatalf("assistant response = %+v", model.Conversation[1])
	}
	if model.Conversation[2].Kind != ConversationReasoning || model.Conversation[2].Text != "Checking tests" || model.Conversation[2].Role != RoleReasoning {
		t.Fatalf("reasoning = %+v", model.Conversation[2])
	}
	if model.Activity.TurnStatus != "idle" || model.Activity.EventCount != 9 {
		t.Fatalf("activity = %+v", model.Activity)
	}
	if model.Order[2] != (Entry{Surface: SurfaceConversation, Index: 1}) || model.Order[3] != (Entry{Surface: SurfaceConversation, Index: 2}) {
		t.Fatalf("arrival order = %+v", model.Order)
	}
}

func TestReducerCoalescesOperationalLifecycleAndKeepsCompleteData(t *testing.T) {
	var model Model
	output := strings.Repeat("failure details\n", 500)
	start := event.Event{Backend: "codex", Kind: "command.started", TurnID: "turn-1", ItemID: "cmd-1", Summary: "go test ./...", Raw: []byte(`{"private":"original"}`)}
	model.Apply(start)
	model.Apply(event.Event{Kind: "command.output", TurnID: "turn-1", ItemID: "cmd-1", Summary: output[:100], Data: map[string]any{"stream": "stderr"}})
	model.Apply(event.Event{Kind: "command.output", TurnID: "turn-1", ItemID: "cmd-1", Summary: output[100:], Data: map[string]any{"stream": "stderr"}})
	model.Apply(event.Event{Kind: "command.completed", TurnID: "turn-1", ItemID: "cmd-1", Summary: "go test ./... · exit 1", Data: map[string]any{"exit_code": 1}})
	if len(model.Telemetry) != 1 {
		t.Fatalf("command rows = %d, want 1", len(model.Telemetry))
	}
	item := model.Telemetry[0]
	if item.Kind != TelemetryCommand || item.Role != RoleExecution || item.State != RoleFailure || item.Status != "failed" {
		t.Fatalf("command semantics = %+v", item)
	}
	if len(item.Details) != 1 || item.Details[0].Value != output || len(item.Events) != 4 || string(item.Events[0].Raw) != string(start.Raw) {
		t.Fatal("command output or source events were truncated")
	}
	if model.Activity.ActiveTools != 0 || !item.Matches("failure details") {
		t.Fatalf("activity/filter = %+v", model.Activity)
	}
}

func TestReducerClassifiesToolWithoutProviderSpecificTypes(t *testing.T) {
	var model Model
	model.Apply(event.Event{Backend: "openai", Kind: "tool.started", TurnID: "turn-1", ItemID: "call-1", Summary: "search", Data: map[string]any{"tool_name": "search", "arguments": `{"query":"ResumeThread"}`}})
	model.Apply(event.Event{Backend: "openai", Kind: "tool.completed", TurnID: "turn-1", ItemID: "call-1", Summary: "search · succeeded", Data: map[string]any{"tool_name": "search", "result": "6 hits", "error": false}})
	if len(model.Telemetry) != 1 {
		t.Fatalf("tool rows = %d, want 1", len(model.Telemetry))
	}
	item := model.Telemetry[0]
	if item.Label != "Tool · search" || item.Role != RoleRead || item.State != RoleSuccess || item.Details[0].Value != `{"query":"ResumeThread"}` || item.Details[1].Value != "6 hits" {
		t.Fatalf("tool semantics = %+v", item)
	}
	if item.Glyph != "⌕" || item.Type != "SEARCH" || item.Primary != `"ResumeThread"` || item.Detail != "6 hits" || item.DisplayStatus != "✓ DONE" {
		t.Fatalf("tool line = %+v", item)
	}
	if model.Activity.ActiveTools != 0 || model.Activity.Latest != "SEARCH" {
		t.Fatalf("activity = %+v", model.Activity)
	}
}

func TestUnknownEventRemainsInTelemetryWithSource(t *testing.T) {
	var model Model
	model.Apply(event.Event{Backend: "codex", Kind: "backend.unknown", Source: "future/event", Summary: "future/event", Raw: []byte(`{"new":true}`)})
	if len(model.Telemetry) != 1 || model.Telemetry[0].Kind != TelemetryOther || !model.Telemetry[0].Matches("future/event") || string(model.Telemetry[0].Events[0].Raw) != `{"new":true}` {
		t.Fatalf("unknown event = %+v", model.Telemetry)
	}
}

func TestInterruptedTurnStopsUnfinishedTelemetry(t *testing.T) {
	var model Model
	model.Apply(event.Event{Kind: "turn.started", TurnID: "turn-1"})
	model.Apply(event.Event{Kind: "tool.started", TurnID: "turn-1", ItemID: "tool-1", Summary: "search"})
	model.Apply(event.Event{Kind: "turn.completed", TurnID: "turn-1", Data: map[string]any{"status": "interrupted"}})
	if len(model.Telemetry) != 2 || model.Telemetry[0].Status != "interrupted" || model.Telemetry[1].Status != "stopped" {
		t.Fatalf("telemetry after interruption = %+v", model.Telemetry)
	}
	if model.Activity.TurnStatus != "interrupted" || model.Activity.ActiveTools != 0 {
		t.Fatalf("activity after interruption = %+v", model.Activity)
	}
}
