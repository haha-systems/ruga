package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/xiy/ruga/internal/backend"
	"github.com/xiy/ruga/internal/backend/codex"
	"github.com/xiy/ruga/internal/bus"
	"github.com/xiy/ruga/internal/ui"
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
	if err := ui.Run(ctx, events, backendClient.Submit); err != nil && ctx.Err() == nil {
		return err
	}
	stop()
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ruga:", err)
	os.Exit(1)
}
