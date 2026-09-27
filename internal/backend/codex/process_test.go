package codex

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexAppServerProcessCapturesStderr(t *testing.T) {
	if os.Getenv("RUGA_CODEX_STDERR_HELPER") == "1" {
		fmt.Fprintln(os.Stderr, "codex stderr test marker")
		return
	}

	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("RUGA_CODEX_STDERR_HELPER", "1")
	stderrLog, err := openCodexStderrLog()
	if err != nil {
		t.Fatalf("open Codex stderr log: %v", err)
	}

	logPath := stderrLog.Name()
	defer stderrLog.Close()

	process, err := startCodexProcess(context.Background(), os.Args[0], []string{
		"-test.run=TestCodexAppServerProcessCapturesStderr",
	}, "", &boundedLogWriter{writer: stderrLog})
	if err != nil {
		t.Fatalf("start Codex process: %v", err)
	}

	if err := process.Close(); err != nil {
		t.Fatalf("close Codex process: %v", err)
	}

	if err := stderrLog.Close(); err != nil {
		t.Fatalf("close Codex stderr log: %v", err)
	}

	if want := filepath.Join(stateHome, "ruga", "logs", "codex-app-server.stderr.log"); logPath != want {
		t.Fatalf("Codex stderr log path = %q, want %q", logPath, want)
	}

	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read Codex stderr log: %v", err)
	}

	if !strings.Contains(string(contents), "codex stderr test marker") {
		t.Fatalf("captured stderr = %q", contents)
	}

	if info, err := os.Stat(logPath); err != nil {
		t.Fatalf("stat Codex stderr log: %v", err)
	} else if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("Codex stderr log mode = %04o, want 0600", mode)
	}
}

func TestBoundedLogWriterLimitsOutput(t *testing.T) {
	var output bytes.Buffer
	writer := boundedLogWriter{writer: &output}
	value := bytes.Repeat([]byte("x"), codexStderrLogLimit+100)

	written, err := writer.Write(value)
	if err != nil {
		t.Fatalf("write stderr log: %v", err)
	}

	if written != len(value) {
		t.Fatalf("written bytes = %d, want %d", written, len(value))
	}

	if output.Len() != codexStderrLogLimit || !bytes.Contains(output.Bytes(), []byte("log truncated")) {
		t.Fatalf("captured log length = %d; truncation marker present = %t", output.Len(), bytes.Contains(output.Bytes(), []byte("log truncated")))
	}
}
