package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/haha-systems/ruga/internal/backend"
	"github.com/haha-systems/ruga/internal/backend/codex"
	openaibackend "github.com/haha-systems/ruga/internal/backend/openai"
	"github.com/haha-systems/ruga/internal/bus"
	"github.com/haha-systems/ruga/internal/recording"
	"github.com/haha-systems/ruga/internal/ui"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "replay" {
		if len(os.Args) != 3 {
			fatal(fmt.Errorf("usage: harness replay <session>"))
		}
		if err := replay(os.Args[2]); err != nil {
			fatal(err)
		}
		return
	}

	var options backendOptions
	flags := flag.NewFlagSet("harness", flag.ContinueOnError)
	flags.StringVar(&options.name, "backend", "codex", "backend to use: codex or openai")
	flags.StringVar(&options.codexBinary, "codex", "codex", "path to the Codex CLI binary")
	flags.StringVar(&options.openAIBaseURL, "openai-base-url", "https://api.openai.com/v1", "OpenAI-compatible API base URL")
	flags.StringVar(&options.openAIModel, "openai-model", "gpt-5.4-mini", "model for the OpenAI-compatible backend")
	flags.StringVar(&options.openAIKeyEnv, "openai-api-key-env", "OPENAI_API_KEY", "environment variable containing the OpenAI-compatible API key")
	if err := flags.Parse(os.Args[1:]); err != nil {
		fatal(err)
	}
	if flags.NArg() != 0 {
		fatal(fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}
	if err := run(options); err != nil {
		fatal(err)
	}
}

type backendOptions struct {
	name          string
	codexBinary   string
	openAIBaseURL string
	openAIModel   string
	openAIKeyEnv  string
}

func run(options backendOptions) (resultErr error) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	eventBus := bus.New()
	var closeOnce sync.Once
	var closeErr error
	closeBus := func() error {
		closeOnce.Do(func() { closeErr = eventBus.Close() })
		return closeErr
	}
	defer func() { _ = closeBus() }()

	// Subscribe before connecting so the initial thread event cannot be lost.
	events, err := eventBus.Subscribe(ctx)
	if err != nil {
		return err
	}
	workingDir, err := filepath.Abs(".")
	if err != nil {
		return err
	}
	recordingDir, err := recording.DefaultDir()
	if err != nil {
		return err
	}
	recorder, err := recording.NewRecorder(context.Background(), eventBus, recordingDir)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Recording session:", recorder.Path())
	defer func() {
		if err := closeBus(); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
		if err := recorder.Close(); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	backendClient, err := newBackend(options, workingDir, os.Getenv)
	if err != nil {
		return err
	}

	if err := backendClient.Start(ctx, eventBus); err != nil {
		_ = backendClient.Close()
		return err
	}
	var actions ui.Actions
	if interactive, ok := backendClient.(backend.Interactive); ok {
		actions = ui.Actions{
			ResolveApproval: interactive.ResolveApproval,
			Interrupt:       interactive.Interrupt,
		}
	}
	branch := gitBranch(workingDir)
	project := filepath.Base(workingDir)
	modelName := ""
	if display, ok := backendClient.(backend.ModelDisplay); ok {
		modelName = display.ModelName()
	}
	backendDisplay := "Codex"
	if options.name == "openai" {
		backendDisplay = "OpenAI-compatible"
	}
	uiErr := ui.Run(ctx, events, backendClient.Submit, actions, ui.Config{
		Project: project, Branch: branch, Backend: backendDisplay, Model: modelName,
	})
	stopBackendErr := backendClient.Close()
	if uiErr != nil && ctx.Err() == nil {
		return uiErr
	}
	return stopBackendErr
}

func newBackend(options backendOptions, workingDir string, lookupEnv func(string) string) (backend.Backend, error) {
	switch options.name {
	case "", "codex":
		return codex.New(options.codexBinary, workingDir), nil
	case "openai":
		return openaibackend.New(openaibackend.Config{
			BaseURL: options.openAIBaseURL,
			Model:   options.openAIModel,
			APIKey:  lookupEnv(options.openAIKeyEnv),
		}), nil
	default:
		return nil, fmt.Errorf("unknown backend %q (choose codex or openai)", options.name)
	}
}

func replay(session string) error {
	path, err := recording.ResolveSession(session)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	eventBus := bus.New()
	var replayCloseOnce sync.Once
	var replayCloseErr error
	closeBus := func() error {
		replayCloseOnce.Do(func() { replayCloseErr = eventBus.Close() })
		return replayCloseErr
	}
	defer func() { _ = closeBus() }()
	events, err := eventBus.Subscribe(ctx)
	if err != nil {
		return err
	}
	projectDir, err := filepath.Abs(".")
	if err != nil {
		return err
	}
	finished := make(chan error, 1)
	go func() {
		replayErr := recording.Replay(ctx, eventBus, path)
		finished <- errors.Join(replayErr, closeBus())
	}()
	uiErr := ui.Run(ctx, events, nil, ui.Actions{}, ui.Config{
		Project: filepath.Base(projectDir), Branch: gitBranch(projectDir), Backend: "Replay", ReadOnly: true,
	})
	replayErr := <-finished
	if uiErr != nil && ctx.Err() == nil {
		return errors.Join(uiErr, replayErr)
	}
	return replayErr
}

func gitBranch(dir string) string {
	command := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	value, err := command.Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(value))
	if branch == "HEAD" {
		return ""
	}
	return branch
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ruga:", err)
	os.Exit(1)
}
