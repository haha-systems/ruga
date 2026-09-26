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

## 9. TUI

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

## 10. Implementation Slices

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
