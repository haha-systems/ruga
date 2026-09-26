package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileCreatesAndReplacesWithoutReturningContents(t *testing.T) {
	root := t.TempDir()
	arguments := mustJSON(map[string]string{"path": "new.txt", "content": "first"})
	result := (WriteFile{Root: root}).Execute(context.Background(), arguments)
	if result.IsError || result.Content != "ok: new.txt, 5 bytes" || strings.Contains(result.Content, "first") {
		t.Fatalf("new file result = %+v", result)
	}
	arguments = mustJSON(map[string]string{"path": "new.txt", "content": "replacement"})
	result = (WriteFile{Root: root}).Execute(context.Background(), arguments)
	if result.IsError || result.Content != "ok: new.txt, 11 bytes" {
		t.Fatalf("replace result = %+v", result)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "new.txt")); string(got) != "replacement" {
		t.Fatalf("written file = %q", got)
	}
	outside := (WriteFile{Root: root}).Execute(context.Background(), mustJSON(map[string]string{"path": "../outside", "content": "bad"}))
	if !outside.IsError || !strings.Contains(outside.Content, "outside the repository") {
		t.Fatalf("outside write result = %+v", outside)
	}
}
