# Ruga

Ruga is a minimal terminal coding harness for Codex App Server. It starts the
local Codex server, opens a thread, normalizes server activity, publishes
application events through Watermill GoChannel, and displays them in a
scrollable Bubble Tea timeline.

## Run

Requirements: Go 1.25+ and the Codex CLI available on `PATH`.

```sh
go run ./cmd/harness
```

Use `-codex /path/to/codex` to select a different Codex binary. Type a prompt
and press Enter to start a turn. Assistant text streams into the timeline and
the status line shows whether Codex is idle, working, or in an error state.
Use the arrow and page keys to scroll; new events follow the bottom only while
the timeline is already at the bottom. Press `Ctrl+C` or `Esc` to exit.

Timeline events are buffered and coalesced for display so terminal rendering
does not block App Server event processing.
