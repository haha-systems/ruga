package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/haha-systems/ruga/internal/backend"
	"github.com/haha-systems/ruga/internal/backend/codex"
	"github.com/haha-systems/ruga/internal/bus"
	"github.com/haha-systems/ruga/internal/ui"
)

func main() {
	var codexBinary string
	flag.StringVar(&codexBinary, "codex", "codex", "path to the Codex CLI binary")
	flag.Parse()
	if err := run(codexBinary); err != nil {
		fatal(err)
	}
}

func run(codexBinary string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, stop := context.WithCancel(ctx)

	eventBus := bus.New()
	defer eventBus.Close()
	defer stop()

	// Subscribe before connecting so the initial thread event cannot be lost.
	events, err := eventBus.Subscribe(ctx)
	if err != nil {
		return err
	}

	workingDir, err := filepath.Abs(".")
	if err != nil {
		return err
	}
	var backendClient backend.Backend = codex.New(codexBinary, workingDir)
	defer backendClient.Close()

	if err := backendClient.Start(ctx, eventBus); err != nil {
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
	if err := ui.Run(ctx, events, backendClient.Submit, actions, ui.Config{
		Project: project, Branch: branch, Backend: "Codex", Model: modelName,
	}); err != nil && ctx.Err() == nil {
		return err
	}
	stop()
	return nil
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
