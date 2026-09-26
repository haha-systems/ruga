package recording

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/haha-systems/ruga/internal/bus"
	"github.com/haha-systems/ruga/internal/event"
)

const SessionPrefix = "session-"

// DefaultDir returns the per-user location used for JSONL session recordings.
func DefaultDir() (string, error) {
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
		return filepath.Join(dataHome, "ruga", "sessions"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find user home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "ruga", "sessions"), nil
}

// ResolveSession accepts a session ID or an explicit recording path.
func ResolveSession(session string) (string, error) {
	if strings.TrimSpace(session) == "" {
		return "", errors.New("session is required")
	}
	if filepath.IsAbs(session) || strings.ContainsRune(session, filepath.Separator) || strings.HasSuffix(session, ".jsonl") {
		return session, nil
	}
	dir, err := DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, session+".jsonl"), nil
}

// Recorder copies normalized events from the application bus to a JSONL file.
// Disk writes happen on the recorder goroutine, independently of publishers.
type Recorder struct {
	path   string
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	err    error
}

// NewRecorder creates a uniquely named recording in directory and subscribes
// before returning, so callers can start publishing without losing events.
func NewRecorder(ctx context.Context, source bus.Bus, directory string) (*Recorder, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create recording directory %q: %w", directory, err)
	}
	file, err := os.CreateTemp(directory, SessionPrefix+"*.jsonl")
	if err != nil {
		return nil, fmt.Errorf("create session recording in %q: %w", directory, err)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	events, err := source.Subscribe(streamCtx)
	if err != nil {
		cancel()
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, fmt.Errorf("subscribe recorder to event bus: %w", err)
	}
	recorder := &Recorder{path: file.Name(), cancel: cancel, done: make(chan struct{})}
	go recorder.write(events, file)
	return recorder, nil
}

func (r *Recorder) Path() string { return r.path }

// Close stops the subscriber and waits for buffered events and file data to be
// flushed. It returns any asynchronous recording failure.
func (r *Recorder) Close() error {
	r.cancel()
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *Recorder) write(events <-chan event.Event, file *os.File) {
	defer close(r.done)
	writer := bufio.NewWriter(file)
	var writeErr error
	for ev := range events {
		if writeErr != nil {
			continue
		}
		payload, err := json.Marshal(ev)
		if err == nil {
			_, err = writer.Write(payload)
		}
		if err == nil {
			err = writer.WriteByte('\n')
		}
		if err == nil {
			err = writer.Flush()
		}
		if err != nil {
			writeErr = fmt.Errorf("write session recording %q: %w", r.path, err)
			slog.Error("session recording failed", "path", r.path, "error", err)
		}
	}
	if err := writer.Flush(); writeErr == nil && err != nil {
		writeErr = fmt.Errorf("flush session recording %q: %w", r.path, err)
	}
	if err := file.Close(); writeErr == nil && err != nil {
		writeErr = fmt.Errorf("close session recording %q: %w", r.path, err)
	}
	r.mu.Lock()
	r.err = writeErr
	r.mu.Unlock()
}

// Replay publishes every JSONL event in file order to the application bus.
func Replay(ctx context.Context, target bus.Bus, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open session recording %q: %w", path, err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	line := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		payload, readErr := reader.ReadBytes('\n')
		if len(payload) > 0 {
			line++
			payload = bytes.TrimSpace(payload)
			var ev event.Event
			if len(payload) == 0 {
				return fmt.Errorf("session recording %q:%d: empty JSONL record", path, line)
			}
			if err := json.Unmarshal(payload, &ev); err != nil {
				return fmt.Errorf("session recording %q:%d: decode event: %w", path, line, err)
			}
			if ev.Kind == "" {
				return fmt.Errorf("session recording %q:%d: event kind is required", path, line)
			}
			if err := target.Publish(ctx, ev); err != nil {
				return fmt.Errorf("session recording %q:%d: publish event: %w", path, line, err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read session recording %q:%d: %w", path, line+1, readErr)
		}
	}
}
