package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const readMaxLines = 400

type ReadFile struct{ Root string }

func (ReadFile) Name() string { return "read" }

func (ReadFile) Schema() ToolSchema {
	return ToolSchema{
		Description: "Read a bounded line range from one repository file.", Type: "object",
		Properties: map[string]Property{
			"path":       {Type: "string", Description: "Repository-relative file path."},
			"start_line": {Type: "integer", Description: "First line to include; defaults to 1."},
			"end_line":   {Type: "integer", Description: "Last line to include; defaults to the first 200 lines."},
		},
		Required: []string{"path"}, AdditionalProperties: false,
	}
}

func (ReadFile) IsReadOnly() bool { return true }

func (tool ReadFile) Execute(_ context.Context, arguments json.RawMessage) ToolResult {
	var input struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return toolError("invalid read arguments: %v", err)
	}

	if input.StartLine == 0 {
		input.StartLine = 1
	}

	if input.StartLine < 1 || input.EndLine < 0 || (input.EndLine != 0 && input.EndLine < input.StartLine) {
		return toolError("invalid line range %d-%d", input.StartLine, input.EndLine)
	}

	target, relative, err := resolvePath(tool.Root, input.Path)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}
	}

	info, err := os.Stat(target)
	if err != nil {
		return toolError("stat %s: %v", relative, err)
	}

	if !info.Mode().IsRegular() {
		return toolError("%s is not a regular file", relative)
	}

	requestedEnd := input.EndLine
	if requestedEnd == 0 {
		requestedEnd = input.StartLine + 199
	}

	endLine := requestedEnd
	if endLine-input.StartLine+1 > readMaxLines {
		endLine = input.StartLine + readMaxLines - 1
	}

	file, err := os.Open(target)
	if err != nil {
		return toolError("open %s: %v", relative, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), maxResultBytes)
	var lines []string
	lineNumber := 0
	moreLines := false
	for scanner.Scan() {
		lineNumber++
		if lineNumber < input.StartLine {
			continue
		}

		if lineNumber > endLine {
			moreLines = true
			break
		}

		lines = append(lines, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return toolError("read %s: %v", relative, err)
	}

	actualEnd := input.StartLine + len(lines) - 1
	header := fmt.Sprintf("%s:%d-%d (%d bytes)", relative, input.StartLine, max(input.StartLine-1, actualEnd), info.Size())
	if endLine < requestedEnd {
		header += fmt.Sprintf("; limited to %d lines", readMaxLines)
	}

	if moreLines {
		header += "; more lines available"
	}

	if len(lines) == 0 {
		return ToolResult{Content: header + "\n(no lines in requested range)"}
	}

	return ToolResult{Content: header + "\n" + strings.Join(lines, "\n")}
}
