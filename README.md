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

## OpenAI-compatible backend

Select the Chat Completions backend with `-backend openai`. It defaults to the
OpenAI API at `https://api.openai.com/v1` and model `gpt-5.4-mini`. Set
`OPENAI_API_KEY` in the environment for authenticated endpoints; the key is
never stored in the command line or configuration. Compatible servers that do
not require authentication can use an unset key.

```sh
export OPENAI_API_KEY="your-key"
go run ./cmd/harness -backend openai
```

Use `-openai-base-url` and `-openai-model` to select a compatible endpoint and
model. The key environment variable can be changed with `-openai-api-key-env`.

## Resuming sessions

Ruga prints a session ID when a new session starts. Resume the most recent
session for the current directory and backend with `--resume`, or choose an
older session by ID:

```sh
go run ./cmd/harness --resume
go run ./cmd/harness resume <session-id>
```

Session state is stored separately from event recordings under
`$XDG_STATE_HOME/ruga/sessions` (or `~/.local/state/ruga/sessions`). Codex
sessions resume their native App Server thread. OpenAI-compatible sessions
restore their conversation, endpoint, model, and credential environment
variable name; API keys themselves are never saved.

## OpenAI-compatible coding tools

The OpenAI-compatible backend exposes a small set of local coding tools:

- `echo` returns the supplied text unchanged.
- `read` returns a bounded line range from one file.
- `search` finds literal text and returns bounded matching lines.
- `list` lists a shallow, bounded set of files and directories.
- `patch` applies a standard multi-file unified diff.
- `write` creates or replaces a complete file.
- `exec` runs a shell command in the repository and returns bounded output.

File paths stay within the repository, and reads, searches, and all tool
results are bounded. `search` requires the ripgrep (`rg`) command, and `patch`
requires the system `patch` utility. `exec` runs in the repository with a
30-second default timeout, a 120-second maximum, and bounded output.

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

## Recording and replay

Live sessions are recorded as normalized JSONL events under
`$XDG_DATA_HOME/ruga/sessions` (or `~/.local/share/ruga/sessions` when
`XDG_DATA_HOME` is unset). The recording path is printed when the harness
starts. Replay a session by its filename stem or by passing a JSONL path:

```sh
go run ./cmd/harness replay session-abc123
go run ./cmd/harness replay /path/to/session.jsonl
```

Replay sends events through the same event bus and timeline used for live
sessions. The replay timeline is read-only; press `Ctrl+C` to exit.
