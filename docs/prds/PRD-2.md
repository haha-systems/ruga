# Ruga Phase 2 — Sessions and Tool Runtime

## Goal

Turn Ruga from a conversational client into a resumable coding harness.

This phase adds:

1. durable session resume
2. tool calling for OpenAI-compatible backends
3. a minimal coding tool set designed for low token use
4. the foundations for measuring tool efficiency

Keep Codex behaviour native where Codex already provides the capability.

Do not recreate Codex functionality inside Ruga unnecessarily.

## 1. Session Model

Ruga owns the concept of a session.

A session should contain enough information to reopen work regardless of backend:

```go
type Session struct {
    ID              string
    Backend         string
    BackendSession  string
    Provider        string
    Model           string
    CWD             string
    CreatedAt       time.Time
    UpdatedAt       time.Time
}
```

Backend-specific metadata may be stored separately.

### Codex

Persist the Codex thread ID.

Resume using the App Server thread resume API rather than reconstructing the conversation ourselves.

The SDK already exposes `ResumeThread`.

Prefer native Codex state as the source of truth.

### OpenAI-compatible

Ruga is the source of truth.

Persist the canonical conversation sequence, including:

* system/developer instructions
* user messages
* assistant messages
* tool calls
* tool results

Resume by reconstructing the provider request from this state.

Provider-native continuation IDs may be stored as an optimisation, but Ruga must not depend on them because OpenAI-compatible providers differ.

### Commands

Support at minimum:

```text
ruga
ruga --resume
ruga resume <session>
```

`--resume` should resume the most recent suitable session for the current working directory.

A full session browser can come later.

## 2. Persistence

Use simple local persistence.

Do not introduce a service or external database.

Keep two concepts separate:

* conversation/session state required for resume
* raw application events used for debugging/replay

The existing event stream must not become the only representation of conversational state.

Persist state incrementally after completed messages, tool calls, tool results, and relevant backend state changes.

A crash should lose as little completed work as practical.

## 3. Backend Capabilities

The existence of two real backends now justifies explicit capability reporting.

Keep it small.

For example:

```go
type Capabilities struct {
    NativeResume      bool
    Tools             bool
    ParallelToolCalls bool
}
```

Do not build a general capability framework yet.

## 4. OpenAI-Compatible Tool Loop

Implement the standard agent loop inside the OpenAI-compatible backend:

```text
messages
   ↓
model
   ↓
tool call(s)?
   │
   ├─ no ─→ assistant response
   │
   └─ yes
        ↓
   execute tools
        ↓
   append calls + results
        ↓
      model
```

Preserve provider tool-call IDs exactly.

Persist both the call and result before continuing.

Support streamed tool-call arguments if the provider emits them incrementally.

Multiple independent read-only calls may execute concurrently.

Mutating calls must preserve declared ordering unless their independence is known.

The backend adapter owns provider protocol differences.

The rest of Ruga should deal with normalized tool-call events.

## 5. Tool Registry

Create one small registry:

```go
type Tool interface {
    Name() string
    Schema() ToolSchema
    Execute(context.Context, json.RawMessage) ToolResult
}
```

The registry provides tools to capable backends and executes calls returned by them.

Tool activity must also produce normal Ruga events so Codex and OpenAI-compatible sessions can use the same TUI presentation.

## 6. Initial Tool Set

Start with only six tools.

### `read`

Read part of a file.

Inputs:

* path
* optional start line
* optional end line

Behaviour:

* bounded output by default
* never read an entire large file implicitly
* return the requested range plus file/range metadata
* do not prefix every line with a line number unless useful

### `search`

Search repository text.

Back with ripgrep where available.

Inputs:

* query
* optional path
* optional glob
* optional result limit
* optional context

Default output:

```text
internal/foo.go:37: matching text
internal/bar.go:91: another match
```

No context by default.

Hard-limit result count and output size.

### `list`

List files/directories.

Inputs:

* path
* optional depth
* optional limit

Defaults should favour a shallow compact view.

Do not dump a whole repository.

### `patch`

Apply a patch.

Prefer a standard patch representation rather than whole-file replacement.

Requirements:

* support multiple hunks
* support multiple files
* support creating and deleting files
* reject ambiguous or failed patches cleanly

Successful result should be extremely small:

```text
ok: 2 files, +14 -6
```

Do not return modified file contents.

### `write`

Write a complete file.

Primarily useful for new files and generated content.

Do not return the file after writing it.

Result:

```text
ok: internal/foo.go, 1842 bytes
```

Existing-file editing should normally use `patch`.

### `exec`

Execute a command.

Inputs:

* command
* optional working directory
* optional timeout
* optional output limit

Return:

* exit code
* bounded stdout/stderr
* duration where useful

Long output should preserve useful beginning and ending sections:

```text
[first output]

… 731 lines omitted …

[last output]

exit 1
```

Never put unbounded process output into model context.

## 7. Token-Efficiency Rules

Tool schemas themselves consume context.

Therefore:

* keep the tool count small
* keep descriptions short
* avoid large enums and deeply nested schemas
* do not expose redundant tools
* use normal shell commands for uncommon operations

Tool results should be narrow by default.

The model should request more information explicitly rather than receive large speculative context.

Mutation tools should return summaries, not content.

Search before read.

Read ranges before full files.

Patch instead of rewrite.

Allow several tool calls in one assistant turn to reduce model round trips.

Every truncated result must clearly state that truncation occurred.

## 8. Measurement

Record per turn:

* model input tokens
* model output tokens
* number of tool calls
* tool-result bytes
* tool-result estimated tokens
* model round trips
* elapsed tool time

This gives us a concrete optimisation target.

A useful eventual metric is:

```text
context tokens consumed / successful code change
```

Dogfooding should drive tuning of defaults.

## 9. Context Management

A long session must stay inside the model's context window without losing the
thread of the work. Tool-result narrowing (§6, §7) bounds each result; it does
not bound the session. `Submit` replays the whole conversation every round trip
and that conversation grows monotonically, so an endpoint eventually rejects the
request on context length. Manage the window explicitly rather than waiting for
the provider to fail.

### Accounting

Usage comes from the backend, not from guesswork:

* The OpenAI-compatible backend requests `stream_options.include_usage` and
  publishes `usage.updated` with input, output, and total tokens (§8).
* Compaction triggers on reported input tokens when they are available.
* Not every compatible server returns usage. Fall back to a character estimate
  using the `(bytes+3)/4` idiom already used for tool results, and never present
  the estimate as authoritative.

The context limit is a property of the model, not the protocol. Take it as
configuration with a conservative default and allow a per-session override.

### The turn is the unit

Compaction must never split a turn. An assistant message that carries tool calls
and the tool results that answer them form one group: dropping the assistant
message orphans its results, and dropping a result breaks the call/result
pairing most servers require. Compact whole turns only — either a turn travels
verbatim, or the entire turn is replaced by one summary.

This mirrors §4's rule that calls and results are persisted together, and it
extends to the parallel batches §1 of the roadmap introduced: a multi-call turn
is still one turn.

### Strategy

Use a sliding window plus synthesis, with a selectable summary:

* Keep the most recent N turns verbatim.
* When the estimate crosses a threshold, replace the oldest turns with a single
  synthetic `system` message that summarises them.
* Two strategies are available and selected by configuration:
  * **Deterministic** (default): a mechanical summary built from the dropped
    messages. It is offline, resumable, and testable without a model.
  * **Model**: a single non-streaming completion is asked to summarise the
    dropped turns for continuation. It gives better summaries at the cost of one
    extra round trip and tighter coupling to the model.

The model strategy is best-effort. Any failure — a transport error, no content,
or a non-2xx response — falls back to the deterministic summary, so compaction
never blocks or fails a turn. Either way the chosen summary folds in any prior
summary and stays bounded, and the `context.compacted` event records which
strategy produced it.

### Stable prefix

Compaction rewrites the request prefix, which defeats provider prompt caching.
Compact rarely and in large steps rather than trimming on every turn. Preserve
the standing system instruction (for example the tool-batching guidance) across
compaction so it does not silently disappear.

### State

A compaction must survive resume:

* `session.Session` gains a summary and a compaction timestamp.
* `Submit` triggers compaction inside its loop.
* Compaction rewrites both the persisted messages and the in-memory history;
  rewriting only one lets a resume re-inflate the full transcript.
* Publish a `context.compacted` event so compaction flows through presentation,
  recording, and replay like every other event.

### Relationship to CES

CES (roadmap §5) isolates each phase in a fresh context, which is itself a
context-management strategy and a partial substitute for sliding-window
compaction. Design the two together: each CES phase also needs bounded input, so
projection rules and window rules must not contradict each other.

### Non-goals

* No provider-hosted conversation state as the source of truth; Ruga remains the
  source of truth for the OpenAI-compatible path (§1).
* No lossy trimming that splits a turn's tool call/result pairing, under any
  strategy.

## 10. TUI

Resume should feel almost invisible.

On resume, show a compact event such as:

```text
↻ resumed session · 42 turns · ~/code/ruga
```

Tool events should use the existing timeline.

Examples:

```text
⌕ search  "ResumeThread"  internal/
  6 matches

↳ read    internal/session/session.go:80-180

∆ patch   internal/session/session.go
  +18 -4

$ go test ./...
  PASS · 2.1s
```

Do not dump tool JSON into the normal UI.

Raw arguments remain available for debugging.

## 11. Implementation Slices

### Slice 1 — Resume

* Ruga session identity
* persistence
* Codex native resume
* OpenAI-compatible transcript resume
* `--resume`
* `resume <id>`

Dogfood it before continuing.

### Slice 2 — Tool Runtime

* registry
* schemas
* OpenAI-compatible tool-call parsing
* tool execution loop
* normalized events
* persistence of calls/results
* multiple tool calls per model turn

Initially implement one trivial test tool to validate the loop.

### Slice 3 — Read-Only Coding Tools

Implement:

* `read`
* `search`
* `list`

Dogfood these before adding mutation.

### Slice 4 — Mutation

Implement:

* `patch`
* `write`
* `exec`

Ruga should now be capable of making and testing changes to itself using an OpenAI-compatible model.

## Success Condition

Phase 2 is complete when:

1. a Ruga session can be exited and resumed
2. both Codex and OpenAI-compatible sessions preserve useful continuity
3. an OpenAI-compatible model can inspect the repository
4. it can edit Ruga
5. it can run Ruga's tests
6. it can continue working from the tool results
7. tool output remains compact under normal development workloads

At that point Ruga is no longer merely dogfooding its chat interface.

**Ruga can build Ruga through either backend.**
