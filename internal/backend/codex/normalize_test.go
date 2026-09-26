package codex

import (
	"encoding/json"
	"testing"

	codexgo "github.com/zealbase/codex-app-server-go"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		payload     string
		wantKind    string
		wantSummary string
		wantItemID  string
	}{
		{name: "thread start", method: "thread/started", payload: `{"threadId":"th-1"}`, wantKind: "thread.started", wantSummary: "thread/started"},
		{name: "message delta", method: "item/agentMessage/delta", payload: `{"threadId":"th-1","turnId":"tu-2","itemId":"it-3","text":"hello"}`, wantKind: "message.delta", wantSummary: "hello"},
		{name: "message completed", method: "item/completed", payload: `{"item":{"id":"it-message","type":"agentMessage","text":"full answer"}}`, wantKind: "message.completed", wantSummary: "full answer", wantItemID: "it-message"},
		{name: "error", method: "error", payload: `{"message":"failed"}`, wantKind: "error", wantSummary: "failed"},
		{name: "unknown", method: "future/event", payload: `{"threadId":"th-1","extra":true}`, wantKind: "backend.unknown", wantSummary: "future/event"},
		{name: "command start", method: "item/started", payload: `{"threadId":"th-1","turnId":"tu-1","item":{"id":"it-cmd","type":"commandExecution","command":"go test ./..."}}`, wantKind: "command.started", wantSummary: "go test ./...", wantItemID: "it-cmd"},
		{name: "command complete", method: "item/completed", payload: `{"item":{"id":"it-cmd","type":"commandExecution","command":"go test ./...","exitCode":1,"durationMs":25}}`, wantKind: "command.completed", wantSummary: "go test ./... · exit 1 · 25ms", wantItemID: "it-cmd"},
		{name: "command output", method: "item/commandExecution/outputDelta", payload: `{"itemId":"it-cmd","stream":"stderr","delta":"failed"}`, wantKind: "command.output", wantSummary: "failed", wantItemID: "it-cmd"},
		{name: "command output base64", method: "process/outputDelta", payload: `{"itemId":"it-cmd","deltaBase64":"UE9ORw=="}`, wantKind: "command.output", wantSummary: "PONG", wantItemID: "it-cmd"},
		{name: "file changes", method: "item/fileChange/patchUpdated", payload: `{"itemId":"it-file","changes":[{"path":"internal/ui/app.go","kind":"update"}]}`, wantKind: "file.changed", wantSummary: "update internal/ui/app.go", wantItemID: "it-file"},
		{name: "tool start", method: "item/started", payload: `{"item":{"id":"it-tool","type":"mcpToolCall","server":"docs","tool":"search"}}`, wantKind: "tool.started", wantSummary: "docs/search", wantItemID: "it-tool"},
		{name: "tool complete", method: "item/completed", payload: `{"item":{"id":"it-tool","type":"dynamicToolCall","namespace":"repo","tool":"inspect","success":false}}`, wantKind: "tool.completed", wantSummary: "repo.inspect · failed", wantItemID: "it-tool"},
		{name: "reasoning", method: "item/reasoning/summaryTextDelta", payload: `{"itemId":"it-reason","delta":"Checking the build"}`, wantKind: "status.update", wantSummary: "Checking the build", wantItemID: "it-reason"},
		{name: "usage", method: "thread/tokenUsage/updated", payload: `{"tokenUsage":{"last":{"inputTokens":40,"outputTokens":10,"totalTokens":50},"modelContextWindow":128000}}`, wantKind: "usage.updated", wantSummary: "40 in · 10 out · 50 total tokens · context 128000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := normalize(tt.method, json.RawMessage(tt.payload))
			if ev.Kind != tt.wantKind {
				t.Fatalf("Kind = %q, want %q", ev.Kind, tt.wantKind)
			}
			if ev.Summary != tt.wantSummary {
				t.Fatalf("Summary = %q, want %q", ev.Summary, tt.wantSummary)
			}
			if tt.wantItemID != "" && ev.ItemID != tt.wantItemID {
				t.Fatalf("ItemID = %q, want %q", ev.ItemID, tt.wantItemID)
			}
			if ev.Backend != "codex" || ev.Source != tt.method || ev.ID == "" || ev.Timestamp.IsZero() {
				t.Fatalf("missing event metadata: %+v", ev)
			}
			if string(ev.Raw) != tt.payload {
				t.Fatalf("Raw = %s, want %s", ev.Raw, tt.payload)
			}
		})
	}
}

func TestNormalizeUsesTypedMessageDelta(t *testing.T) {
	ev := normalizeWithValue(
		"item/agentMessage/delta",
		json.RawMessage(`{"threadId":"th-1","itemId":"it-3"}`),
		codexgo.ItemAgentMessageDeltaEvent{ThreadID: "th-1", ItemID: "it-3", Text: "PONG"},
	)
	if ev.Summary != "PONG" {
		t.Fatalf("Summary = %q, want PONG", ev.Summary)
	}
}

func TestNormalizeReadsDeltaField(t *testing.T) {
	ev := normalize("item/agentMessage/delta", json.RawMessage(`{"delta":"PONG"}`))
	if ev.Summary != "PONG" {
		t.Fatalf("Summary = %q, want PONG", ev.Summary)
	}
}

func TestNormalizeCommandAndToolCompletionExposeSemanticFields(t *testing.T) {
	command := normalize("item/completed", json.RawMessage(`{"item":{"type":"commandExecution","command":"go test ./...","exitCode":2,"aggregatedOutput":"failed details"}}`))
	if command.Data["exit_code"] != 2 || command.Data["output"] != "failed details" {
		t.Fatalf("command completion data = %+v", command.Data)
	}
	tool := normalize("item/completed", json.RawMessage(`{"item":{"type":"mcpToolCall","tool":"search","success":false}}`))
	if tool.Data["error"] != true {
		t.Fatalf("tool completion data = %+v", tool.Data)
	}
}

func TestNormalizeExtractsIdentifiers(t *testing.T) {
	ev := normalize("item/agentMessage/delta", json.RawMessage(`{"threadId":"th-1","turnId":"tu-2","itemId":"it-3","text":"hi"}`))
	if ev.ThreadID != "th-1" || ev.TurnID != "tu-2" || ev.ItemID != "it-3" {
		t.Fatalf("identifiers = (%q, %q, %q)", ev.ThreadID, ev.TurnID, ev.ItemID)
	}
}
