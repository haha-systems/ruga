package presentation

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/haha-systems/ruga/internal/event"
)

// updateLine derives the five parts of a compact telemetry row. Values remain
// complete here; only the terminal renderer may shorten them for its width.
func (item *TelemetryItem) updateLine() {
	item.Glyph, item.Type, item.Primary, item.Detail = "◇", "EVENT", item.Summary, ""
	switch item.Kind {
	case TelemetryTool:
		item.toolLine()
	case TelemetryCommand:
		item.Glyph, item.Type = "$", "EXEC"
		item.Primary = firstCommand(item.Events, item.Summary)

	case TelemetryFile:
		item.Glyph, item.Type = "Δ", "PATCH"
	case TelemetryApproval:
		item.Glyph, item.Type = "!", "APPROVAL"
	case TelemetryWarning:
		item.Glyph, item.Type = "!", "WARNING"
	case TelemetryError:
		item.Glyph, item.Type = "✕", "ERROR"
	case TelemetrySession:
		item.Glyph, item.Type = "◇", "SESSION"
	case TelemetryTurn:
		item.Glyph, item.Type = "◈", "MODEL"
		item.Primary = "Assistant turn"

	case TelemetryUsage:
		item.Glyph, item.Type = "◈", "USAGE"
	case TelemetryOther:
		item.Primary = "Unrecognized activity"
	}
	item.DisplayStatus = displayStatus(item)
	if duration := elapsed(item.Events); duration > 0 {
		item.DisplayStatus += " " + duration.String()
	}
}

func (item *TelemetryItem) toolLine() {
	name := strings.ToLower(strings.TrimPrefix(item.Label, "Tool · "))
	if index := strings.LastIndexAny(name, "/."); index >= 0 {
		name = name[index+1:]
	}
	item.Type = strings.ToUpper(name)
	if item.Type == "" {
		item.Type = "TOOL"
	}
	args := toolArguments(item.Details)
	result := toolResult(item.Details)
	switch name {
	case "search":
		item.Glyph = "⌕"
		item.Primary = quoted(args["query"])
		if path := argumentString(args, "path"); path != "" {
			item.Primary += " · " + path
		}
		item.Detail = searchCount(result)

	case "read":
		item.Glyph = "↳"
		item.Primary = argumentString(args, "path")
		start, end := argumentNumber(args, "start_line"), argumentNumber(args, "end_line")
		if start == 0 {
			start = 1
		}
		if end > 0 {
			item.Detail = fmt.Sprintf("L%d–%d", start, end)
		} else {
			item.Detail = fmt.Sprintf("L%d+", start)
		}

	case "list":
		item.Glyph = "≡"
		item.Primary = argumentString(args, "path")
		if item.Primary == "" {
			item.Primary = "."
		}
		item.Detail = resultCount(result, "entries")

	case "patch":
		item.Glyph = "Δ"
		item.Primary = patchPath(argumentString(args, "patch"))
		item.Detail = strings.TrimPrefix(result, "ok: ")

	case "write":
		item.Glyph = "+"
		item.Primary = argumentString(args, "path")
		if result != "" {
			item.Detail = strings.TrimPrefix(result, "ok: ")
		}

	case "exec":
		item.Glyph = "$"
		item.Primary = argumentString(args, "command")
		item.Detail = argumentString(args, "cwd")

	default:
		item.Glyph = "◇"
		item.Primary = item.Summary
	}
	if item.Primary == "" {
		item.Primary = item.Summary
	}
	if item.Detail == "" && result != "" && name != "exec" {
		item.Detail = firstLine(result)
	}
}

func firstCommand(events []event.Event, fallback string) string {
	for _, ev := range events {
		if command, ok := ev.Data["command"].(string); ok && command != "" {
			return command
		}
		if ev.Kind == "command.started" && ev.Summary != "" {
			return ev.Summary
		}
	}
	return fallback
}

func toolArguments(details []Detail) map[string]any {
	for _, detail := range details {
		if detail.Label == "arguments" {
			var args map[string]any
			if json.Unmarshal([]byte(detail.Value), &args) == nil {
				return args
			}
		}
	}
	return nil
}

func toolResult(details []Detail) string {
	for _, detail := range details {
		if detail.Label == "result" {
			return detail.Value
		}
	}
	return ""
}

func argumentString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func argumentNumber(args map[string]any, key string) int {
	value, _ := args[key].(float64)
	return int(value)
}

func quoted(value any) string {
	text, _ := value.(string)
	if text == "" {
		return ""
	}
	return fmt.Sprintf("%q", text)
}

func patchPath(patch string) string {
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "+++ b/") {
			return strings.TrimPrefix(line, "+++ b/")
		}
	}
	return "Patch"
}

func resultCount(result, noun string) string {
	if result == "" {
		return ""
	}
	if strings.HasPrefix(result, "no matches") {
		return "0 " + noun
	}
	if !strings.Contains(result, "\n") && strings.HasSuffix(result, noun) {
		return result
	}
	count := 0
	for _, line := range strings.Split(result, "\n") {
		if line != "" && !strings.HasPrefix(line, "…") {
			count++
		}
	}
	return fmt.Sprintf("%d %s", count, noun)
}

var searchMatchLine = regexp.MustCompile(`:[0-9]+: `)

func searchCount(result string) string {
	if result == "" {
		return ""
	}
	if strings.HasPrefix(result, "no matches") {
		return "0 hits"
	}
	if !strings.Contains(result, "\n") && strings.HasSuffix(result, " hits") {
		return result
	}
	count := 0
	for _, line := range strings.Split(result, "\n") {
		if searchMatchLine.MatchString(line) {
			count++
		}
	}
	if count == 0 {
		return "results"
	}
	return fmt.Sprintf("%d hits", count)
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(value, "\n")
	return strings.TrimSpace(line)
}

func displayStatus(item *TelemetryItem) string {
	switch item.State {
	case RoleActive:
		return "… RUNNING"
	case RoleFailure:
		return "✕ FAIL"
	case RoleWarning:
		if item.Status == "interrupted" || item.Status == "stopped" {
			return "! " + strings.ToUpper(item.Status)
		}
		return "! ATTENTION"

	case RoleSuccess:
		if item.Type == "EXEC" {
			return "✓ PASS"
		}
		return "✓ DONE"

	default:
		return ""
	}
}

func elapsed(events []event.Event) time.Duration {
	for index := len(events) - 1; index >= 0; index-- {
		switch value := events[index].Data["elapsed_ms"].(type) {
		case int:
			return (time.Duration(value) * time.Millisecond).Round(time.Millisecond)
		case float64:
			return (time.Duration(value) * time.Millisecond).Round(time.Millisecond)
		}
	}
	return 0
}

// ExpandedDetails returns readable fields for inline inspection. The source
// Events and full Details remain available for recording and copy operations.
func (item TelemetryItem) ExpandedDetails() []Detail {
	var expanded []Detail
	if item.Primary != "" {
		label := "target"
		if item.Type == "EXEC" {
			label = "command"
		}
		expanded = append(expanded, Detail{Label: label, Value: item.Primary})
	}
	if item.Detail != "" {
		expanded = append(expanded, Detail{Label: "summary", Value: item.Detail})
	}
	if item.DisplayStatus != "" {
		expanded = append(expanded, Detail{Label: "status", Value: item.DisplayStatus})
	}
	if item.ItemID != "" {
		expanded = append(expanded, Detail{Label: "item", Value: item.ItemID})
	}
	for _, detail := range item.Details {
		if detail.Label != "arguments" {
			expanded = append(expanded, detail)
			continue
		}
		var arguments map[string]any
		if json.Unmarshal([]byte(detail.Value), &arguments) != nil {
			expanded = append(expanded, detail)
			continue
		}
		keys := make([]string, 0, len(arguments))
		for key := range arguments {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, ok := arguments[key].(string)
			if !ok {
				encoded, _ := json.MarshalIndent(arguments[key], "", "  ")
				value = string(encoded)
			}
			expanded = append(expanded, Detail{Label: key, Value: value})
		}
	}
	return expanded
}
