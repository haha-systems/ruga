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

func TestExecRejectsWorkingDirectoryOutsideRepository(t *testing.T) {
	result := (Exec{Root: t.TempDir()}).Execute(context.Background(), mustJSON(map[string]string{
		"command": "pwd", "cwd": "../outside",
	}))
	if !result.IsError || !strings.Contains(result.Content, "outside the repository") {
		t.Fatalf("outside cwd result = %+v", result)
	}
}
