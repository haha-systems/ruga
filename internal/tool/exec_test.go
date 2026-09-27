package tool

import (
	"context"
	"strings"
	"testing"
)

func TestExecBoundsOutputAndReportsExitStatus(t *testing.T) {
	command := "printf HEAD; i=0; while [ $i -lt 2000 ]; do printf x; i=$((i+1)); done; printf TAIL; exit 7"
	result := (Exec{Root: t.TempDir()}).Execute(context.Background(), mustJSON(map[string]any{
		"command": command, "output_limit_bytes": 256,
	}))
	if !result.IsError || !strings.Contains(result.Content, "exit 7") || !strings.Contains(result.Content, "output bytes omitted") || !strings.Contains(result.Content, "HEAD") ||
		!strings.Contains(result.Content, "TAIL") {

		t.Fatalf("bounded command result = %+v", result)
	}

	if len(result.Content) > maxResultBytes {
		t.Fatalf("command result exceeded global limit: %d bytes", len(result.Content))
	}
}

func TestExecRejectsOversizedCommandWithStructuredError(t *testing.T) {
	oversized := strings.Repeat("a", maxExecCommand+1)
	result := (Exec{Root: t.TempDir()}).Execute(context.Background(), mustJSON(map[string]string{"command": oversized}))
	if !result.IsError {
		t.Fatal("oversized command did not error")
	}

	if !strings.Contains(result.Content, "over the") || !strings.Contains(result.Content, "write a file") {
		t.Fatalf("oversized command error = %q, want byte count and file suggestion", result.Content)
	}
}

func TestExecAcceptsCommandAtLimit(t *testing.T) {
	// A command just under the ceiling must run rather than be rejected.
	padding := strings.Repeat("#", maxExecCommand-16)
	result := (Exec{Root: t.TempDir()}).Execute(context.Background(), mustJSON(map[string]string{"command": padding + "; true"}))
	if strings.Contains(result.Content, "over the") {
		t.Fatalf("command at the ceiling was rejected: %q", result.Content)
	}
}

func TestExecRejectsWorkingDirectoryOutsideRepository(t *testing.T) {
	result := (Exec{Root: t.TempDir()}).Execute(context.Background(), mustJSON(map[string]string{
		"command": "pwd", "cwd": "../outside",
	}))
	if !result.IsError || !strings.Contains(result.Content, "outside the repository") {
		t.Fatalf("outside cwd result = %+v", result)
	}
}
