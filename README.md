# Ruga

Ruga is a minimal terminal coding harness for Codex App Server. It starts the
local Codex server, opens a thread, normalizes server activity, publishes
application events through Watermill GoChannel, and displays them in a
scrollable Bubble Tea timeline. The compact header shows the project, Git
branch when available, backend, model and latest token/context usage when the
server provides them.

## Run

Requirements: Go 1.25+ and the Codex CLI available on `PATH`.

```sh
go run ./cmd/harness
```

Use `-codex /path/to/codex` to select a different Codex binary. Type a prompt
and press Enter to start a turn. Assistant text streams into the timeline and
the status line shows whether Codex is idle, working, or in an error state.
Use the arrow and page keys to scroll; new events follow the bottom only while
the timeline is already at the bottom. Press `Ctrl+C` to exit.

Timeline events are buffered and coalesced for display so terminal rendering
does not block App Server event processing. Command starts, output, and exit
state share one timeline entry; each command retains at most 4 KiB of output,
with an omission count when more arrives. File changes, tool calls, reasoning
and status updates, and token usage have distinct event labels in the timeline.

## Interactive controls

Codex command, file-change, permission, and MCP approval requests appear in a
highlighted prompt above the timeline and take keyboard focus. Press `y` or
`Enter` to accept, `n` or `Esc` to reject. Decisions are sent back to Codex and
recorded in the timeline.

`Tab` cycles focus between the composer, timeline, and an active approval.
Timeline focus enables arrow and page scrolling, `g`/`Home` to jump to the top,
and `G`/`End` to jump to the bottom. Press `/` while the timeline is focused to
filter events by kind, source, text, command output, or approval details; `Enter`
keeps the filter and `Esc` clears it. Press `c` to copy the filtered timeline to
the system clipboard.
Terminal text selection remains available for copying a smaller passage.
`Esc` returns from the timeline to the composer or clears a composer draft.
`Ctrl+X` interrupts the active turn; `Ctrl+C` exits.
