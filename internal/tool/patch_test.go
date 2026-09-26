package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchAppliesMultipleFilesAndReportsCompactCounts(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{"change.txt": "one\ntwo\nthree\n", "delete.txt": "bye\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}

	patchText := "diff --git a/change.txt b/change.txt\n--- a/change.txt\n+++ b/change.txt\n@@ -1,3 +1,3 @@\n-one\n+ONE\n two\n three\ndiff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+new\ndiff --git a/delete.txt b/delete.txt\ndeleted file mode 100644\n--- a/delete.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-bye\n"
	arguments, err := json.Marshal(map[string]string{"patch": patchText})
	if err != nil {
		t.Fatalf("Marshal(): %v", err)
	}

	result := (Patch{Root: root}).Execute(context.Background(), arguments)
	if result.IsError || result.Content != "ok: 3 files, +2 -2" {
		t.Fatalf("patch result = %+v", result)
	}

	if got, _ := os.ReadFile(filepath.Join(root, "change.txt")); string(got) != "ONE\ntwo\nthree\n" {
		t.Fatalf("changed file = %q", got)
	}

	if got, _ := os.ReadFile(filepath.Join(root, "new.txt")); string(got) != "new\n" {
		t.Fatalf("new file = %q", got)
	}

	if _, err := os.Stat(filepath.Join(root, "delete.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file stat error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "change.txt.orig")); !os.IsNotExist(err) {
		t.Fatalf("unexpected patch backup file: %v", err)
	}
}

func TestPatchRejectsOutsidePathsAndAmbiguousHunks(t *testing.T) {
	root := t.TempDir()
	outside := "diff --git a/../outside.txt b/../outside.txt\n--- a/../outside.txt\n+++ b/../outside.txt\n@@ -1 +1 @@\n-before\n+after\n"
	result := (Patch{Root: root}).Execute(context.Background(), mustJSON(map[string]string{"patch": outside}))
	if !result.IsError || !strings.Contains(result.Content, "outside the repository") {
		t.Fatalf("outside patch result = %+v", result)
	}

	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("actual\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}

	ambiguous := "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-expected\n+changed\n"
	result = (Patch{Root: root}).Execute(context.Background(), mustJSON(map[string]string{"patch": ambiguous}))
	if !result.IsError || !strings.Contains(result.Content, "patch rejected") {
		t.Fatalf("ambiguous patch result = %+v", result)
	}

	if got, _ := os.ReadFile(filepath.Join(root, "file.txt")); string(got) != "actual\n" {
		t.Fatalf("failed patch modified file: %q", got)
	}
}

func mustJSON(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}
