package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	defaultSearchLimit = 30
	maxSearchLimit     = 100
	maxSearchContext   = 3
)

type Search struct{ Root string }

func (Search) Name() string { return "search" }

func (Search) Schema() ToolSchema {
	return ToolSchema{
		Description: "Search repository text and return bounded matching lines.", Type: "object",
		Properties: map[string]Property{
			"query":   {Type: "string", Description: "Literal text to search for."},
			"path":    {Type: "string", Description: "Optional repository-relative path."},
			"glob":    {Type: "string", Description: "Optional ripgrep file glob."},
			"limit":   {Type: "integer", Description: "Maximum matching lines; defaults to 30."},
			"context": {Type: "integer", Description: "Context lines around matches, from 0 to 3."},
		},
		Required: []string{"query"}, AdditionalProperties: false,
	}
}

func (Search) IsReadOnly() bool { return true }

func (tool Search) Execute(ctx context.Context, arguments json.RawMessage) ToolResult {
	var input struct {
		Query   string `json:"query"`
		Path    string `json:"path"`
		Glob    string `json:"glob"`
		Limit   int    `json:"limit"`
		Context int    `json:"context"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return toolError("invalid search arguments: %v", err)
	}

	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" {
		return toolError("search query is required")
	}

	if input.Limit == 0 {
		input.Limit = defaultSearchLimit
	}

	if input.Limit < 1 || input.Context < 0 {
		return toolError("search limit must be positive and context cannot be negative")
	}

	input.Limit = min(input.Limit, maxSearchLimit)
	input.Context = min(input.Context, maxSearchContext)
	_, relative, err := resolvePath(tool.Root, input.Path)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}
	}

	root, err := absoluteRoot(tool.Root)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}
	}

	searchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := []string{"--json", "--fixed-strings", "--line-number", "--max-columns", "800", "--max-columns-preview"}
	if input.Context > 0 {
		args = append(args, "--context", strconv.Itoa(input.Context))
	}

	if input.Glob != "" {
		args = append(args, "--glob", input.Glob)
	}

	args = append(args, "--", input.Query, relative)
	command := exec.CommandContext(searchCtx, "rg", args...)
	command.Dir = root
	stdout, err := command.StdoutPipe()
	if err != nil {
		return toolError("start search: %v", err)
	}

	var stderr limitedWriter
	stderr.limit = 4096
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return toolError("start ripgrep: %v", err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 32*1024)
	var output strings.Builder
	matches, outputBytes := 0, 0
	truncated := false
	for scanner.Scan() {
		var row ripgrepRow
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			return toolError("decode ripgrep output: %v", err)
		}

		if row.Type == "match" {
			if matches >= input.Limit {
				truncated = true
				_ = command.Process.Kill()
				break
			}

			matches++
		}

		line := formatSearchRow(row)
		if line == "" {
			continue
		}

		if outputBytes+len(line)+1 > maxResultBytes-128 {
			truncated = true
			_ = command.Process.Kill()
			break
		}

		output.WriteString(line)
		output.WriteByte('\n')
		outputBytes += len(line) + 1
	}

	scanErr := scanner.Err()
	waitErr := command.Wait()
	if scanErr != nil {
		return toolError("read ripgrep output: %v", scanErr)
	}

	if searchCtx.Err() != nil {
		if ctx.Err() != nil {
			return toolError("search canceled: %v", ctx.Err())
		}

		return toolError("search timed out after 15 seconds")
	}

	if waitErr != nil && !truncated {
		if exit, ok := waitErr.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			message := strings.TrimSpace(stderr.String())
			if message == "" {
				message = waitErr.Error()
			}

			return toolError("ripgrep failed: %s", message)
		}
	}

	if matches == 0 {
		return ToolResult{Content: fmt.Sprintf("no matches for %q in %s", input.Query, relative)}
	}

	result := strings.TrimSpace(output.String())
	if truncated {
		result += "\n… [search results truncated]"
	}

	return ToolResult{Content: result}
}

type ripgrepRow struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
	} `json:"data"`
}

func formatSearchRow(row ripgrepRow) string {
	path := row.Data.Path.Text
	text := strings.TrimRight(row.Data.Lines.Text, "\r\n")
	if path == "" || text == "" {
		return ""
	}

	separator := ":"
	if row.Type == "context" {
		separator = "-"
	}

	return fmt.Sprintf("%s%s%d%s %s", path, separator, row.Data.LineNumber, separator, text)
}

type limitedWriter struct {
	bytes.Buffer
	limit int
}

func (w *limitedWriter) Write(value []byte) (int, error) {
	remaining := w.limit - w.Len()
	if remaining > 0 {
		_, _ = w.Buffer.Write(value[:min(len(value), remaining)])
	}

	return len(value), nil
}
