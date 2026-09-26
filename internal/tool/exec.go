package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultExecTimeout = 30
	maxExecTimeout     = 120
	defaultExecOutput  = 4096
	maxExecOutput      = 8192
)

type Exec struct{ Root string }

func (Exec) Name() string { return "exec" }

func (Exec) Schema() ToolSchema {
	return ToolSchema{
		Description: "Run a shell command in the repository with bounded output and timeout.", Type: "object",
		Properties: map[string]Property{
			"command":            {Type: "string", Description: "Command to run."},
			"cwd":                {Type: "string", Description: "Optional repository-relative working directory."},
			"timeout_seconds":    {Type: "integer", Description: "Timeout from 1 to 120 seconds; defaults to 30."},
			"output_limit_bytes": {Type: "integer", Description: "Combined stdout/stderr limit; defaults to 4096 bytes."},
		},
		Required: []string{"command"}, AdditionalProperties: false,
	}
}

func (tool Exec) Execute(ctx context.Context, arguments json.RawMessage) ToolResult {
	var input struct {
		Command          string `json:"command"`
		CWD              string `json:"cwd"`
		TimeoutSeconds   int    `json:"timeout_seconds"`
		OutputLimitBytes int    `json:"output_limit_bytes"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return toolError("invalid exec arguments: %v", err)
	}

	input.Command = strings.TrimSpace(input.Command)
	if input.Command == "" {
		return toolError("command is required")
	}

	if len(input.Command) > 4096 {
		return toolError("command exceeds the 4096-byte limit")
	}

	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = defaultExecTimeout
	}

	if input.TimeoutSeconds < 1 {
		return toolError("timeout_seconds must be positive")
	}

	input.TimeoutSeconds = min(input.TimeoutSeconds, maxExecTimeout)
	if input.OutputLimitBytes == 0 {
		input.OutputLimitBytes = defaultExecOutput
	}

	if input.OutputLimitBytes < 256 {
		return toolError("output_limit_bytes must be at least 256")
	}

	input.OutputLimitBytes = min(input.OutputLimitBytes, maxExecOutput)
	root, err := absoluteRoot(tool.Root)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}
	}

	workingDirectory := root
	if input.CWD != "" {
		workingDirectory, _, err = resolvePath(root, input.CWD)
		if err != nil {
			return ToolResult{Content: err.Error(), IsError: true}
		}

		if info, err := os.Stat(workingDirectory); err != nil || !info.IsDir() {
			return toolError("working directory is not a directory: %s", input.CWD)
		}
	}

	commandContext, cancel := context.WithTimeout(ctx, time.Duration(input.TimeoutSeconds)*time.Second)
	defer cancel()
	command := exec.CommandContext(commandContext, "sh", "-c", input.Command)
	command.Dir = workingDirectory
	stdout := outputCapture{limit: input.OutputLimitBytes / 2}
	stderr := outputCapture{limit: input.OutputLimitBytes - stdout.limit}
	command.Stdout, command.Stderr = &stdout, &stderr
	started := time.Now()
	err = command.Run()
	duration := time.Since(started)
	exitCode := 0
	resultError := false
	status := "PASS"
	if err != nil {
		resultError = true
		status = "failed"
		if commandContext.Err() != nil {
			status = "timed out"
			if ctx.Err() != nil {
				status = "canceled"
			}

			exitCode = -1
		} else if exit, ok := err.(*exec.ExitError); ok {
			exitCode = exit.ExitCode()
		} else {
			exitCode = -1
		}
	}

	var result strings.Builder
	fmt.Fprintf(&result, "exit %d · %s · %s", exitCode, status, duration.Round(time.Millisecond))
	appendOutput(&result, "stdout", stdout)
	appendOutput(&result, "stderr", stderr)
	commandSummary := input.Command
	if len(commandSummary) > 96 {
		commandSummary = commandSummary[:93] + "…"
	}

	summary := fmt.Sprintf("$ %s · %s · %s", commandSummary, status, duration.Round(time.Millisecond))
	return ToolResult{Content: result.String(), Summary: summary, IsError: resultError}
}

type outputCapture struct {
	limit int
	total int
	head  []byte
	tail  []byte
}

func (capture *outputCapture) Write(value []byte) (int, error) {
	written := len(value)
	capture.total += len(value)
	headLimit := capture.limit / 2
	if remaining := headLimit - len(capture.head); remaining > 0 {
		take := min(remaining, len(value))
		capture.head = append(capture.head, value[:take]...)
		value = value[take:]
	}

	if len(value) > 0 {
		tailLimit := capture.limit - headLimit
		capture.tail = append(capture.tail, value...)
		if len(capture.tail) > tailLimit {
			capture.tail = bytes.Clone(capture.tail[len(capture.tail)-tailLimit:])
		}
	}

	return written, nil
}

func (capture outputCapture) String() string {
	content := string(capture.head)
	if capture.total <= capture.limit {
		content += string(capture.tail)
		return content
	}

	omitted := capture.total - len(capture.head) - len(capture.tail)
	return content + fmt.Sprintf("\n… %d output bytes omitted …\n", omitted) + string(capture.tail)
}

func appendOutput(result *strings.Builder, name string, capture outputCapture) {
	value := capture.String()
	if value == "" {
		return
	}

	fmt.Fprintf(result, "\n%s:\n%s", name, strings.TrimRight(value, "\n"))
}
