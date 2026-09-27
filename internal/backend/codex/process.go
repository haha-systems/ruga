package codex

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/haha-systems/ruga/internal/session"
)

const codexStderrLogLimit = 1 << 20

type codexProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	done   chan struct{}

	closeOnce sync.Once
	closeErr  error
}

func startCodexProcess(ctx context.Context, binary string, args []string, cwd string, stderr io.Writer) (*codexProcess, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = cwd
	command.Stderr = stderr

	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}

	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}

	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}

	process := &codexProcess{cmd: command, stdin: stdin, stdout: stdout, done: make(chan struct{})}
	go func() {
		_ = command.Wait()
		close(process.done)
	}()

	return process, nil
}

func openCodexStderrLog() (*os.File, error) {
	stateDir, err := session.DefaultDir()
	if err != nil {
		return nil, err
	}

	logDir := filepath.Join(filepath.Dir(stateDir), "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, err
	}

	path := filepath.Join(logDir, "codex-app-server.stderr.log")
	return os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
}

func (p *codexProcess) Close() error {
	if p == nil {
		return nil
	}

	p.closeOnce.Do(func() { p.closeErr = p.shutdown() })
	return p.closeErr
}

func (p *codexProcess) shutdown() error {
	_ = p.stdin.Close()
	if p.waitWithin(5 * time.Second) {
		return nil
	}

	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	if p.waitWithin(2 * time.Second) {
		return nil
	}

	_ = p.cmd.Process.Kill()
	<-p.done
	return nil
}

func (p *codexProcess) waitWithin(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-p.done:
		return true
	case <-timer.C:
		return false
	}
}

type boundedLogWriter struct {
	writer    io.Writer
	written   int64
	truncated bool
}

func (w *boundedLogWriter) Write(value []byte) (int, error) {
	length := len(value)
	if w.truncated {
		return length, nil
	}

	const marker = "\n[Ruga: Codex stderr log truncated]\n"
	limit := int64(codexStderrLogLimit - len(marker))
	remaining := limit - w.written
	if remaining <= 0 {
		_, err := io.WriteString(w.writer, marker)
		w.truncated = true
		return length, err
	}

	if int64(length) <= remaining {
		written, err := w.writer.Write(value)
		w.written += int64(written)
		return written, err
	}

	part := value[:remaining]
	written, err := w.writer.Write(part)
	w.written += int64(written)
	if err != nil {
		return written, err
	}

	_, err = io.WriteString(w.writer, marker)
	w.truncated = true
	return length, err
}
