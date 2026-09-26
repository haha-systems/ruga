package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxPatchBytes = 1024 * 1024

type Patch struct{ Root string }

func (Patch) Name() string { return "patch" }

func (Patch) Schema() ToolSchema {
	return ToolSchema{
		Description: "Apply a standard multi-file unified patch; prefer this over rewriting existing files.", Type: "object",
		Properties: map[string]Property{
			"patch": {Type: "string", Description: "Git-style unified patch with a/ and b/ file paths."},
		},
		Required: []string{"patch"}, AdditionalProperties: false,
	}
}

func (tool Patch) Execute(ctx context.Context, arguments json.RawMessage) ToolResult {
	var input struct {
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return toolError("invalid patch arguments: %v", err)
	}

	if strings.TrimSpace(input.Patch) == "" {
		return toolError("patch is required")
	}

	if len(input.Patch) > maxPatchBytes {
		return toolError("patch exceeds the %d-byte limit", maxPatchBytes)
	}

	files, additions, deletions, err := inspectPatch(tool.Root, input.Patch)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}
	}

	if len(files) == 0 {
		return toolError("patch contains no file changes")
	}

	if _, err := exec.LookPath("patch"); err != nil {
		return toolError("patch utility is required: %v", err)
	}

	root, err := absoluteRoot(tool.Root)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if output, err := runPatch(ctx, root, input.Patch, true); err != nil {
		return toolError("patch rejected: %s", strings.TrimSpace(output))
	}

	if output, err := runPatch(ctx, root, input.Patch, false); err != nil {
		return toolError("patch failed: %s", strings.TrimSpace(output))
	}

	result := fmt.Sprintf("ok: %d files, +%d -%d", len(files), additions, deletions)
	return ToolResult{Content: result, Summary: "∆ " + result}
}

func inspectPatch(root, value string) ([]string, int, int, error) {
	scanner := bufio.NewScanner(strings.NewReader(value))
	scanner.Buffer(make([]byte, 4096), maxPatchBytes)
	var files []string
	additions, deletions := 0, 0
	var oldPath, newPath string
	inHunk := false
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if strings.HasPrefix(line, "diff --git ") {
			inHunk = false
		}

		if !inHunk && strings.HasPrefix(line, "--- ") {
			var err error
			oldPath, err = parsePatchPath(strings.TrimPrefix(line, "--- "))
			if err != nil {
				return nil, 0, 0, err
			}

			continue
		}

		if !inHunk && strings.HasPrefix(line, "+++ ") {
			if oldPath == "" {
				return nil, 0, 0, fmt.Errorf("patch has a new-file path without an old-file path")
			}

			var err error
			newPath, err = parsePatchPath(strings.TrimPrefix(line, "+++ "))
			if err != nil {
				return nil, 0, 0, err
			}

			if oldPath != "/dev/null" && newPath != "/dev/null" && patchRelative(oldPath) != patchRelative(newPath) {
				return nil, 0, 0, fmt.Errorf("patch renames are not supported")
			}

			selected := oldPath
			if selected == "/dev/null" {
				selected = newPath
			}

			if selected == "/dev/null" || !strings.HasPrefix(selected, "a/") && !strings.HasPrefix(selected, "b/") {
				return nil, 0, 0, fmt.Errorf("patch paths must use a/ and b/ prefixes")
			}

			relative := patchRelative(selected)
			if filepath.IsAbs(relative) || relative == "" {
				return nil, 0, 0, fmt.Errorf("invalid patch path %q", selected)
			}

			if _, _, err := resolveMutationPath(root, relative); err != nil {
				return nil, 0, 0, err
			}

			files = append(files, filepath.ToSlash(relative))
			oldPath, newPath = "", ""
			continue
		}

		if strings.HasPrefix(line, "@@ ") {
			inHunk = true
			continue
		}

		if inHunk && len(line) > 0 {
			switch line[0] {
			case '+':
				additions++
			case '-':
				deletions++
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, 0, 0, fmt.Errorf("read patch: %w", err)
	}

	return files, additions, deletions, nil
}

func patchRelative(path string) string {
	return strings.TrimPrefix(strings.TrimPrefix(path, "a/"), "b/")
}

func parsePatchPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "\"") {
		if end := strings.LastIndex(value, "\""); end > 0 {
			decoded, err := strconv.Unquote(value[:end+1])
			return decoded, err
		}

		return "", fmt.Errorf("unterminated quoted patch path")
	}

	if end := strings.IndexByte(value, '\t'); end >= 0 {
		value = value[:end]
	}

	return value, nil
}

func runPatch(ctx context.Context, root, patchText string, check bool) (string, error) {
	args := []string{"-p1", "-F", "0", "-f", "-E", "-V", "none"}
	if check {
		args = append(args, "-C")
	}

	command := exec.CommandContext(ctx, "patch", args...)
	command.Dir = root
	command.Stdin = strings.NewReader(patchText)
	var output limitedWriter
	output.limit = 4096
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	return output.String(), err
}
