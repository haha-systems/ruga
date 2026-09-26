package presentation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/haha-systems/ruga/internal/event"
)

func TestToolLineGrammarForCodingTools(t *testing.T) {
	tests := []struct {
		name, glyph, primary, detail, result, status string
		args                                         map[string]any
	}{
		{name: "read", glyph: "↳", primary: "internal/session/session.go", detail: "L80–180", args: map[string]any{"path": "internal/session/session.go", "start_line": 80, "end_line": 180}, result: "file content", status: "✓ DONE"},
		{name: "list", glyph: "≡", primary: "internal/", detail: "2 entries", args: map[string]any{"path": "internal/"}, result: "internal/ui/\ninternal/tool/", status: "✓ DONE"},
		{name: "patch", glyph: "Δ", primary: "internal/ui/app.go", detail: "1 files, +18 -4", args: map[string]any{"patch": "--- a/internal/ui/app.go\n+++ b/internal/ui/app.go\n@@ -1 +1 @@\n-old\n+new\n"}, result: "ok: 1 files, +18 -4", status: "✓ DONE"},
		{name: "write", glyph: "+", primary: "internal/ui/app.go", detail: "internal/ui/app.go, 10 bytes", args: map[string]any{"path": "internal/ui/app.go", "content": "new"}, result: "ok: internal/ui/app.go, 10 bytes", status: "✓ DONE"},
		{name: "exec", glyph: "$", primary: "go test ./...", detail: "", args: map[string]any{"command": "go test ./..."}, result: "exit 0 · pass · 2.1s", status: "✓ PASS 2.1s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			arguments, err := json.Marshal(tt.args)
			if err != nil {
				t.Fatal(err)
			}
			var model Model
			elapsedMS := 0
			if tt.name == "exec" {
				elapsedMS = 2100
			}
			model.Apply(event.Event{Kind: "tool.started", ItemID: "call-1", Summary: tt.name, Data: map[string]any{"tool_name": tt.name, "arguments": string(arguments)}})
			model.Apply(event.Event{Kind: "tool.completed", ItemID: "call-1", Summary: tt.name + " · succeeded", Data: map[string]any{"tool_name": tt.name, "result": tt.result, "elapsed_ms": elapsedMS, "error": false}})
			item := model.Telemetry[0]
			if item.Glyph != tt.glyph || item.Type != strings.ToUpper(tt.name) || item.Primary != tt.primary || item.Detail != tt.detail || item.DisplayStatus != tt.status {
				t.Fatalf("line parts = %+v", item)
			}
		})
	}
}
