# Ruga

Ruga is a terminal coding harness for Codex App Server and OpenAI-compatible
backends. It normalizes backend activity, publishes application events through
Watermill GoChannel, and presents conversation separately from operational
telemetry. The compact header shows the project, branch when available,
backend, model, activity, and token/context usage when provided.

## Run

Requirements: Go 1.25+ and the Codex CLI available on `PATH`.

```sh
go run ./cmd/ruga
```

Use `-codex /path/to/codex` to select a different Codex binary. Type a prompt
and press Enter to start a turn. The conversation view shows user messages,
quiet reasoning/status lines, and assistant responses. Press `Ctrl+E` to reveal
the event inspector; on wider terminals it slides in from the right, and on
smaller terminals it uses the full width.

Events are buffered and reduced into presentation records without changing the
recorded event stream. Tool and command activity appears as compact semantic
lines in the inspector. Select a line and press Enter to reveal arguments,
results, command output, and other details inline. Compact text is shortened to
fit the terminal; the presentation record and copied text retain complete data.

## OpenAI-compatible backend

Select the Chat Completions backend with `-backend openai`. It defaults to the
OpenAI API at `https://api.openai.com/v1` and model `gpt-5.4-mini`. Set
`OPENAI_API_KEY` in the environment for authenticated endpoints; the key is
never stored in the command line or configuration. Compatible servers that do
not require authentication can use an unset key.

```sh
export OPENAI_API_KEY="your-key"
go run ./cmd/ruga -backend openai
```

Use `-openai-base-url` and `-openai-model` to select a compatible endpoint and
model. The key environment variable can be changed with `-openai-api-key-env`.

## Resuming sessions

Ruga prints a session ID when a new session starts. Resume the most recent
session for the current directory and backend with `--resume`, or choose an
older session by ID:

```sh
go run ./cmd/ruga --resume
go run ./cmd/ruga resume <session-id>
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
highlighted prompt above the conversation and take keyboard focus. Press `y`
or `Enter` to accept, `n` or `Esc` to reject. Decisions are recorded in the
event stream.

`Tab` cycles focus between the composer, conversation, visible inspector, and
an active approval. In conversation focus, the arrow and page keys scroll, and
`g`/`Home` and `G`/`End` jump to the top and bottom. In inspector focus,
`Up`/`Down` selects items, `Enter` expands or collapses a selected item, page
keys scroll, and `G`/`End` returns to follow mode. Press `/` to filter the
inspector, `Esc` to clear the filter or return to the composer, and `c` to copy
the focused view with full details. An unseen count appears when new telemetry
arrives while the inspector is hidden or scrolled away. `Ctrl+X` interrupts
the active turn; `Ctrl+C` exits.

## Recording and replay

Live sessions are recorded as normalized JSONL events under
`$XDG_DATA_HOME/ruga/sessions` (or `~/.local/share/ruga/sessions` when
`XDG_DATA_HOME` is unset). The recording path is printed when the harness
starts. Replay a session by its filename stem or by passing a JSONL path:

```sh
go run ./cmd/ruga replay session-abc123
go run ./cmd/ruga replay /path/to/session.jsonl
```

Replay sends events through the same event bus and presentation reducer used
for live sessions. The read-only TUI stays open after replay completes so the
inspector can be explored; press `Ctrl+C` to exit.

## Roadmap

Deferred direction is recorded in [`docs/ROADMAP.md`](docs/ROADMAP.md):

- **Interface:** multiline composer, Markdown rendering via `glamour`, selectable
  themes, and a movable event stream (top/bottom/left/right).
- **Cognition:** parallel tool calls, context compaction, memory via a Ghostdive
  adapter, quota-aware cognition (QAC), and Cumulative Epistemic State (CES).

The CES architecture itself lives in [`docs/CES.md`](docs/CES.md).
