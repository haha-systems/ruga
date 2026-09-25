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
	}{
		{name: "thread start", method: "thread/started", payload: `{"threadId":"th-1"}`, wantKind: "thread.started", wantSummary: "thread/started"},
		{name: "message delta", method: "item/agentMessage/delta", payload: `{"threadId":"th-1","turnId":"tu-2","itemId":"it-3","text":"hello"}`, wantKind: "message.delta", wantSummary: "hello"},
		{name: "error", method: "error", payload: `{"message":"failed"}`, wantKind: "error", wantSummary: "failed"},
		{name: "unknown", method: "future/event", payload: `{"threadId":"th-1","extra":true}`, wantKind: "backend.unknown", wantSummary: "future/event"},
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

func TestNormalizeExtractsIdentifiers(t *testing.T) {
	ev := normalize("item/agentMessage/delta", json.RawMessage(`{"threadId":"th-1","turnId":"tu-2","itemId":"it-3","text":"hi"}`))
	if ev.ThreadID != "th-1" || ev.TurnID != "tu-2" || ev.ItemID != "it-3" {
		t.Fatalf("identifiers = (%q, %q, %q)", ev.ThreadID, ev.TurnID, ev.ItemID)
	}
}
