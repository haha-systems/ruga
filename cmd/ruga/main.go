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
	"github.com/haha-systems/ruga/internal/session"
	"github.com/haha-systems/ruga/internal/tool"
	"github.com/haha-systems/ruga/internal/ui"
)

func main() {
	var resumeID string
	args := os.Args[1:]

	if len(args) > 0 && args[0] == "resume" {
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: ruga resume <session> [flags]"))
		}

		resumeID = args[1]
		args = args[2:]
	}

	if len(os.Args) > 1 && os.Args[1] == "replay" {
		if len(os.Args) != 3 {
			fatal(fmt.Errorf("usage: ruga replay <session>"))
		}

		if err := replay(os.Args[2]); err != nil {
			fatal(err)
		}

		return
	}

	var options backendOptions
	options.panel = string(ui.PanelRight)

	flags := flag.NewFlagSet("ruga", flag.ContinueOnError)
	flags.StringVar(&options.name, "backend", "", "backend to use: codex or openai")
	flags.StringVar(&options.codexBinary, "codex", "codex", "path to the Codex CLI binary")
	flags.StringVar(&options.openAIBaseURL, "openai-base-url", "https://api.openai.com/v1", "OpenAI-compatible API base URL")
	flags.StringVar(&options.openAIModel, "openai-model", "gpt-5.4-mini", "model for the OpenAI-compatible backend")
	flags.StringVar(&options.openAIKeyEnv, "openai-api-key-env", "OPENAI_API_KEY", "environment variable containing the OpenAI-compatible API key")
	flags.Func("panel", "event stream placement: right, left, bottom, or top", func(value string) error {
		return setPanelOption(&options, value)
	})
	flags.BoolVar(&options.resumeLatest, "resume", false, "resume the most recent session for this directory")
	options.resumeID = resumeID

	if err := flags.Parse(args); err != nil {
		fatal(err)
	}

	flags.Visit(func(value *flag.Flag) {
		switch value.Name {
		case "openai-base-url":
			options.openAIBaseURLSet = true
		case "openai-model":
			options.openAIModelSet = true
		case "openai-api-key-env":
			options.openAIKeyEnvSet = true
		}
	})

	if flags.NArg() != 0 {
		fatal(fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}

	if options.resumeLatest && options.resumeID != "" {
		fatal(fmt.Errorf("choose either --resume or resume <session>"))
	}

	if err := run(options); err != nil {
		fatal(err)
	}
}

func validatePanelOption(value string) error {
	switch ui.PanelPlacement(value) {
	case ui.PanelRight, ui.PanelLeft, ui.PanelBottom, ui.PanelTop:
		return nil
	}

	return fmt.Errorf("invalid event stream placement %q (choose right, left, bottom, or top)", value)
}

func setPanelOption(options *backendOptions, value string) error {
	if err := validatePanelOption(value); err != nil {
		return err
	}

	options.panel = value
	return nil
}

type backendOptions struct {
	name             string
	panel            string
	codexBinary      string
	openAIBaseURL    string
	openAIModel      string
	openAIKeyEnv     string
	resumeLatest     bool
	resumeID         string
	openAIBaseURLSet bool
	openAIModelSet   bool
	openAIKeyEnvSet  bool
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

	stateDir, err := session.DefaultDir()
	if err != nil {
		return err
	}

	store := session.NewStore(stateDir)

	state, resuming, err := resolveSession(store, options, workingDir)
	if err != nil {
		return err
	}

	backendName := state.Backend
	applySessionBackendOptions(&options, state, resuming)

	if state.CWD != "" {
		workingDir = state.CWD
	}

	if err := store.Save(state); err != nil {
		return fmt.Errorf("initialize session state: %w", err)
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

	options.name = backendName
	backendClient, err := newBackend(options, workingDir, os.Getenv)
	if err != nil {
		return err
	}

	sessionBackend, ok := backendClient.(backend.SessionSupport)
	if !ok {
		return fmt.Errorf("backend %q does not support durable sessions", backendName)
	}

	sessionBackend.ConfigureSession(state, resuming, store.Save)

	registry, err := tool.NewRegistry(
		tool.Echo{},
		tool.ReadFile{Root: workingDir},
		tool.Search{Root: workingDir},
		tool.List{Root: workingDir},
		tool.Patch{Root: workingDir},
		tool.WriteFile{Root: workingDir},
		tool.Exec{Root: workingDir},
	)
	if err != nil {
		return fmt.Errorf("initialize tool registry: %w", err)
	}

	if toolBackend, ok := backendClient.(backend.ToolSupport); ok {
		toolBackend.ConfigureTools(registry)
	}

	if resuming {
		fmt.Fprintf(os.Stderr, "Resuming session: %s\n", state.ID)
	} else {
		fmt.Fprintf(os.Stderr, "Session ID: %s (resume with: ruga resume %s)\n", state.ID, state.ID)
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
	if backendName == "openai" {
		backendDisplay = "OpenAI-compatible"
	}

	uiErr := ui.Run(ctx, events, backendClient.Submit, actions, ui.Config{
		Project: project, Branch: branch, Backend: backendDisplay, Model: modelName,
		Panel: ui.PanelPlacement(options.panel),
	})

	stopBackendErr := backendClient.Close()
	if uiErr != nil && ctx.Err() == nil {
		return uiErr
	}

	return stopBackendErr
}

func applySessionBackendOptions(options *backendOptions, state session.Session, resuming bool) {
	if !resuming || state.Backend != "openai" {
		return
	}

	if !options.openAIBaseURLSet && state.Provider != "" {
		options.openAIBaseURL = state.Provider
	}

	if !options.openAIModelSet && state.Model != "" {
		options.openAIModel = state.Model
	}

	if !options.openAIKeyEnvSet && state.CredentialEnv != "" {
		options.openAIKeyEnv = state.CredentialEnv
	}
}

func resolveSession(store *session.Store, options backendOptions, cwd string) (session.Session, bool, error) {
	backendName := options.name
	if backendName == "" {
		backendName = "codex"
	}

	var state session.Session
	var err error

	switch {
	case options.resumeID != "":
		state, err = store.Load(options.resumeID)

		if err == nil && options.name != "" && state.Backend != options.name {
			err = fmt.Errorf("session %q uses backend %q, not %q", state.ID, state.Backend, options.name)
		}

	case options.resumeLatest:
		state, err = store.Latest(backendName, cwd)
	}

	if err != nil {
		return session.Session{}, false, err
	}

	if state.ID != "" {
		return state, true, nil
	}

	return session.New(backendName, cwd), false, nil
}

func newBackend(options backendOptions, workingDir string, lookupEnv func(string) string) (backend.Backend, error) {
	switch options.name {
	case "", "codex":
		return codex.New(options.codexBinary, workingDir), nil
	case "openai":
		return openaibackend.New(openaibackend.Config{
			BaseURL:   options.openAIBaseURL,
			Model:     options.openAIModel,
			APIKey:    lookupEnv(options.openAIKeyEnv),
			APIKeyEnv: options.openAIKeyEnv,
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
