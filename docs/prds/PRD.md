# Coding Harness — Initial PRD

## 1. Goal

Build an ultra-minimal terminal coding harness inspired by tools such as Crush and Grok Build.

The first backend is Codex App Server. The architecture must later support OpenAI-compatible API providers without making Codex the lowest common denominator.

The product should feel like a compact command centre rather than a traditional chat client.

The initial development strategy is vertical slices. Each slice must leave the application in a usable state.

As soon as the harness can hold a basic conversation with Codex, development should continue using the harness itself where practical.

## 2. Technology

Use:

* Go
* Bubble Tea for the TUI
* Lip Gloss where useful for terminal presentation
* `github.com/Zealbase/codex-app-server-go` for Codex App Server integration
* Watermill with the in-process GoChannel pub/sub implementation
* JSONL for later event recording and replay

Prefer existing libraries over custom infrastructure when a suitable maintained library exists.

Avoid unnecessary frameworks and abstractions.

## 3. Architectural Principles

### Backend isolation

Codex App Server is the first backend, not the architecture.

Define a small internal backend interface. Codex-specific types must remain inside the Codex adapter.

Future providers may expose fewer capabilities than Codex. Do not reduce the common model to the least capable provider.

### Event-driven core

All significant application activity should flow through an in-process event bus.

High-level flow:

```text
Codex App Server
       │
codex-app-server-go
       │
  Codex Adapter
       │
 event normalization
       │
 Watermill GoChannel
   ┌──────┼───────┐
   │      │       │
  TUI   state   recorder
```

The TUI must not consume raw Codex protocol events directly.

### Thin abstractions

Do not create a large internal framework.

Our abstractions should primarily protect application code from:

* Codex SDK types
* Watermill-specific types
* provider-specific behaviour

Only generalise when a real requirement exists.

### Event fidelity

Keep access to the original backend event or equivalent metadata where practical.

Unknown Codex events must not crash the application. Surface them as debug or generic events until explicit support is added.

### Ordered processing

Preserve event order.

A slow UI must not block the App Server reader.

High-frequency events such as text deltas and command-output deltas may be coalesced by the presentation layer to avoid excessive terminal redraws.

## 4. Core Domain

Create a normalized event model for concepts such as:

* backend connected
* backend disconnected
* turn started
* turn completed
* turn failed
* message started
* message delta
* message completed
* reasoning/status update
* command started
* command output
* command completed
* file changed
* tool started
* tool completed
* approval requested
* approval resolved
* usage updated
* warning
* error

Do not attempt to define the final event taxonomy before implementation. Extend it as real Codex events require.

Events should include useful metadata such as:

* timestamp
* backend
* thread/session identifier
* turn identifier
* event identifier
* source/raw event where useful

## 5. TUI Direction

The interface should be ultra-minimal and dense.

Primary layout:

```text
┌─ project ─ branch ─ backend ─ model ─ state ───────────────┐
│                                                            │
│  scrollable event timeline                                 │
│                                                            │
├────────────────────────────────────────────────────────────┤
│ > input                                                    │
└────────────────────────────────────────────────────────────┘
```

Avoid permanent sidebars and dashboard clutter unless later usage proves they are needed.

The event timeline is the main interface.

Different event types should have clear but restrained visual treatment.

Examples:

* user messages
* assistant messages
* reasoning/status
* shell commands
* command output
* file changes
* tool calls
* approvals
* warnings/errors

Prefer typography, spacing, symbols, borders, and subtle terminal colour over large boxes.

## 6. Development Slices

### Slice 1 — Codex Event Viewer

Implement the complete path:

```text
Codex App Server
→ SDK
→ adapter
→ normalized event
→ Watermill
→ Bubble Tea
```

Requirements:

* launch or connect to Codex App Server
* initialise the Codex client
* start a thread
* consume App Server events
* normalize supported events
* publish them to Watermill
* subscribe from the TUI
* render them in a scrollable timeline
* preserve arrival order
* handle terminal resizing
* handle unknown events safely
* exit cleanly

No text input is required yet.

Success condition:

Running the application shows live Codex events in the timeline.

### Slice 2 — Conversation Loop

Add:

* input composer at the bottom
* prompt submission
* streamed assistant output
* automatic follow-scroll while at the bottom
* manual scrolling without forced snapping
* idle/working/error state
* turn completion handling

Success condition:

The harness can be used as a basic Codex coding interface.

At this point, begin dogfooding the harness for its own development where practical.

### Slice 3 — Rich Coding Events

Add first-class rendering for:

* command execution
* streaming command output
* command exit state
* file modifications
* tool calls
* reasoning/status updates
* usage information

Large output must remain manageable.

Use compact summaries where appropriate, with expansion later if needed.

### Slice 4 — Interactive Events

Add:

* approval requests
* accept
* reject
* interrupt/cancel current turn
* keyboard focus behaviour
* robust key bindings

Approval requests must be clearly distinguishable from passive timeline entries.

### Slice 5 — Command-Centre Polish

Add:

* compact header/status line
* project
* Git branch where available
* backend
* model
* current state
* context/token usage when available
* search/filter capability
* event grouping where useful
* copy/select behaviour
* polished resize behaviour
* visual consistency

Do not add information solely because it is available.

### Slice 6 — Recording and Replay

Add an event recorder subscriber.

Persist normalized events as JSONL.

Support:

```text
ruga replay <session>
```

Replay should drive the same event bus and TUI used during live operation.

Use replay fixtures to test UI behaviour deterministically.

### Slice 7 — Second Backend

Implement an OpenAI-compatible backend.

Use this implementation to test the backend abstraction.

Refactor only where the second backend demonstrates a real mismatch.

Provider-specific capabilities may remain optional extensions.

## 7. Event Bus

Use Watermill GoChannel.

Keep a thin application-owned wrapper so Watermill types do not spread through the codebase.

Conceptually:

```go
type Bus interface {
    Publish(ctx context.Context, topic string, event Event) error
    Subscribe(ctx context.Context, topic string) (<-chan Event, error)
}
```

The exact API may change if Watermill's normal usage suggests a cleaner implementation.

Suggested topic families:

```text
backend.*
backend.codex.*
ui.*
system.*
```

Do not create an elaborate topic hierarchy unless actual consumers require it.

## 8. Suggested Project Structure

```text
cmd/
    ruga/

internal/
    backend/
        backend.go
        codex/
            backend.go
            normalize.go

    event/
        event.go
        types.go

    bus/
        bus.go
        watermill.go

    session/
        model.go

    ui/
        app.go
        timeline.go
        composer.go
        render/
            message.go
            command.go
            file.go
            tool.go
            approval.go

    recording/
        recorder.go
        replay.go
```

Treat this as a starting point, not a required final hierarchy.

## 9. Non-Goals for Initial Development

Do not initially build:

* multi-agent orchestration
* remote workers
* plugin systems
* file browsers
* Git dashboards
* editor integrations
* multiple panes
* session management UI
* complex configuration UI
* custom RPC protocols
* custom event-bus infrastructure
* distributed messaging
* persistence databases
* provider feature parity

## 10. Engineering Rules

* Keep the program runnable after each slice.
* Add tests around event normalization.
* Prefer table-driven Go tests.
* Keep protocol/backend code separate from presentation code.
* Never block the Codex event reader on terminal rendering.
* Handle context cancellation and shutdown correctly.
* Surface errors instead of silently swallowing them.
* Avoid speculative abstractions.
* Buy before build.
* Delete complexity when a library already solves it.
* Optimise for the application becoming pleasant enough to dogfood quickly.

## 11. First Milestone

The immediate target is Slice 1.

Do not implement later slices until Slice 1 works end-to-end and has a clean foundation.

The first milestone is successful when real Codex App Server events can be observed, normalized, sent through Watermill, and rendered in a stable scrollable Bubble Tea timeline.
