package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const maxWriteBytes = 1024 * 1024

type WriteFile struct{ Root string }

func (WriteFile) Name() string { return "write" }

func (WriteFile) Schema() ToolSchema {
	return ToolSchema{
		Description: "Write a complete file, mainly for new files; use patch for existing files.", Type: "object",
		Properties: map[string]Property{
			"path":    {Type: "string", Description: "Repository-relative target file path."},
			"content": {Type: "string", Description: "Complete file content, up to 1 MiB."},
		},
		Required: []string{"path", "content"}, AdditionalProperties: false,
	}
}

func (tool WriteFile) Execute(_ context.Context, arguments json.RawMessage) ToolResult {
	var input struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return toolError("invalid write arguments: %v", err)
	}
	if len(input.Content) > maxWriteBytes {
		return toolError("file content exceeds the %d-byte write limit", maxWriteBytes)
	}
	target, relative, err := resolveMutationPath(tool.Root, input.Path)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}
	}
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return toolError("refusing to overwrite symlink %s", relative)
		}
		if !info.Mode().IsRegular() {
			return toolError("%s is not a regular file", relative)
		}
	} else if !os.IsNotExist(err) {
		return toolError("stat %s: %v", relative, err)
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".ruga-write-*")
	if err != nil {
		return toolError("create temporary file for %s: %v", relative, err)
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return toolError("set permissions for %s: %v", relative, err)
	}
	if _, err := file.WriteString(input.Content); err != nil {
		_ = file.Close()
		return toolError("write %s: %v", relative, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return toolError("sync %s: %v", relative, err)
	}
	if err := file.Close(); err != nil {
		return toolError("close %s: %v", relative, err)
	}
	if err := os.Rename(temp, target); err != nil {
		return toolError("replace %s: %v", relative, err)
	}
	return ToolResult{Content: fmt.Sprintf("ok: %s, %d bytes", relative, len(input.Content))}
}
