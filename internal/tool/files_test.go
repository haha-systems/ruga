package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileReturnsRequestedRangeAndBoundsDefault(t *testing.T) {
	root := t.TempDir()
	var content strings.Builder
	for line := 1; line <= 220; line++ {
		fmt.Fprintf(&content, "line %d\n", line)
	}

	if err := os.WriteFile(filepath.Join(root, "sample.txt"), []byte(content.String()), 0o600); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}

	read := ReadFile{Root: root}
	result := read.Execute(context.Background(), json.RawMessage(`{"path":"sample.txt","start_line":4,"end_line":6}`))
	if result.IsError || !strings.Contains(result.Content, "sample.txt:4-6") || !strings.Contains(result.Content, "line 4\nline 5\nline 6") || strings.Contains(result.Content, "line 7") {
		t.Fatalf("range result = %+v", result)
	}

	defaultResult := read.Execute(context.Background(), json.RawMessage(`{"path":"sample.txt"}`))
	if defaultResult.IsError || !strings.Contains(defaultResult.Content, "line 200") || strings.Contains(defaultResult.Content, "line 201") ||
		!strings.Contains(defaultResult.Content, "more lines available") {

		t.Fatalf("default result was not bounded: %+v", defaultResult)
	}

	outside := read.Execute(context.Background(), json.RawMessage(`{"path":"../outside"}`))
	if !outside.IsError || !strings.Contains(outside.Content, "outside the repository") {
		t.Fatalf("outside path result = %+v", outside)
	}
}

func TestSearchUsesRipgrepAndEnforcesMatchLimit(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is not installed")
	}

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0o700); err != nil {
		t.Fatalf("Mkdir(): %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("needle one\nother\nneedle two\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}

	search := Search{Root: root}
	result := search.Execute(context.Background(), json.RawMessage(`{"query":"needle","path":"src","limit":1}`))
	if result.IsError || !strings.Contains(result.Content, "src/a.go:1: needle one") || !strings.Contains(result.Content, "search results truncated") ||
		strings.Contains(result.Content, "needle two") {

		t.Fatalf("bounded search result = %+v", result)
	}

	contextResult := search.Execute(context.Background(), json.RawMessage(`{"query":"needle","path":"src/a.go","limit":2,"context":1}`))
	if contextResult.IsError || !strings.Contains(contextResult.Content, "src/a.go-2- other") {
		t.Fatalf("search context result = %+v", contextResult)
	}
}

func TestListUsesShallowDepthAndEntryLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub", "deep"), 0o700); err != nil {
		t.Fatalf("MkdirAll(): %v", err)
	}

	for name, value := range map[string]string{
		"a.txt": "a", "sub/b.txt": "b", "sub/deep/c.txt": "c",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}

	list := List{Root: root}
	shallow := list.Execute(context.Background(), json.RawMessage(`{"path":".","depth":1}`))
	if shallow.IsError || !strings.Contains(shallow.Content, "a.txt") || !strings.Contains(shallow.Content, "sub/") || strings.Contains(shallow.Content, "sub/b.txt") {
		t.Fatalf("shallow listing = %+v", shallow)
	}

	deep := list.Execute(context.Background(), json.RawMessage(`{"path":".","depth":3,"limit":1}`))
	if deep.IsError || !strings.Contains(deep.Content, "entry limit 1 reached") {
		t.Fatalf("limited listing = %+v", deep)
	}

	outside := list.Execute(context.Background(), json.RawMessage(`{"path":"../outside"}`))
	if !outside.IsError || !strings.Contains(outside.Content, "outside the repository") {
		t.Fatalf("outside path listing = %+v", outside)
	}
}
