package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultListDepth = 1
	maxListDepth     = 5
	defaultListLimit = 100
	maxListLimit     = 500
)

type List struct{ Root string }

func (List) Name() string { return "list" }

func (List) Schema() ToolSchema {
	return ToolSchema{
		Description: "List a shallow, bounded set of repository files and directories.", Type: "object",
		Properties: map[string]Property{
			"path":  {Type: "string", Description: "Optional repository-relative directory."},
			"depth": {Type: "integer", Description: "Maximum levels below the path; defaults to 1."},
			"limit": {Type: "integer", Description: "Maximum entries; defaults to 100."},
		},
		AdditionalProperties: false,
	}
}

func (List) IsReadOnly() bool { return true }

func (tool List) Execute(_ context.Context, arguments json.RawMessage) ToolResult {
	var input struct {
		Path  string `json:"path"`
		Depth int    `json:"depth"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return toolError("invalid list arguments: %v", err)
	}
	if input.Depth == 0 {
		input.Depth = defaultListDepth
	}
	if input.Limit == 0 {
		input.Limit = defaultListLimit
	}
	if input.Depth < 1 || input.Limit < 1 {
		return toolError("list depth and limit must be positive")
	}
	input.Depth = min(input.Depth, maxListDepth)
	input.Limit = min(input.Limit, maxListLimit)
	target, relative, err := resolvePath(tool.Root, input.Path)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}
	}
	info, err := os.Stat(target)
	if err != nil {
		return toolError("stat %s: %v", relative, err)
	}
	if !info.IsDir() {
		if info.Mode().IsRegular() {
			return ToolResult{Content: relative}
		}
		return toolError("%s is not a regular file or directory", relative)
	}
	var entries []string
	truncated := false
	err = filepath.WalkDir(target, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == target {
			return nil
		}
		relativeEntry, err := filepath.Rel(target, path)
		if err != nil {
			return err
		}
		depth := strings.Count(relativeEntry, string(filepath.Separator)) + 1
		if depth > input.Depth {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if len(entries) >= input.Limit {
			truncated = true
			return fs.SkipAll
		}
		name := filepath.ToSlash(relativeEntry)
		if entry.IsDir() {
			name += "/"
		}
		entries = append(entries, name)
		return nil
	})
	if err != nil {
		if !errors.Is(err, fs.SkipAll) {
			return toolError("list %s: %v", relative, err)
		}
	}
	if len(entries) == 0 {
		return ToolResult{Content: fmt.Sprintf("%s (empty)", relative)}
	}
	result := strings.Join(entries, "\n")
	if truncated {
		result += fmt.Sprintf("\n… [entry limit %d reached]", input.Limit)
	}
	return ToolResult{Content: result}
}
