# Ruga Phase 3 — Semantic TUI

## Goal

Give Ruga a distinct terminal interface built around semantic presentation rather than a traditional scrolling agent transcript.

The UI should separate:

1. conversation
2. reasoning/status
3. operational telemetry
4. raw diagnostic detail

The normal working view should remain calm and readable.

Detailed machine activity should be available immediately when wanted, without dominating the conversation.

The visual direction is:

* cyberpunk
* information-dense
* restrained
* highly scannable
* semantic rather than decorative

Do not sacrifice readability for visual effects.

## 1. Core Interaction Model

The primary interface contains:

```text
┌─ RUGA ─ project ─ branch ─ model ─ state ─ context ───────┐
│                                                           │
│  conversation                                             │
│                                                           │
│  reasoning/status                                         │
│  assistant responses                                      │
│                                                           │
├───────────────────────────────────────────────────────────┤
│ > input                                                   │
└───────────────────────────────────────────────────────────┘
```

Operational events must not be mixed directly into the conversation transcript.

A separate event inspector shows detailed activity.

Initial implementation may use a fixed horizontal split.

The intended interaction is an animated panel that appears from the right:

```text
┌──────────────────────────┬────────────────────────────────┐
│ CONVERSATION             │ EVENT STREAM                   │
│                          │                                │
│ assistant response       │ ⌕ SEARCH ...                   │
│                          │ ↳ READ ...                     │
│ reasoning/status         │ Δ PATCH ...                    │
│                          │ $ EXEC ...                     │
│                          │                                │
├──────────────────────────┴────────────────────────────────┤
│ >                                                         │
└───────────────────────────────────────────────────────────┘
```

The event panel must be hideable with one key action.

The conversation remains the primary surface.

## 2. Semantic Presentation Layer

Do not render backend event types directly.

Introduce a presentation layer between normalized application events and terminal components.

Conceptually:

```text
normalized events
      │
      ▼
presentation model
  ┌────┼────────┐
  ▼    ▼        ▼
chat  telemetry activity
```

Suggested presentation types:

```go
type ConversationItem struct {
    // human-readable conversation content
}

type TelemetryItem struct {
    // compact semantic operational event
}

type ActivityState struct {
    // current aggregate activity
}
```

The presentation model may combine, suppress, coalesce, or summarize events without changing the underlying event stream.

## 3. Semantic Roles

Visual styling must use semantic roles rather than hard-coded event colours.

Suggested roles:

```go
type SemanticRole string

const (
    RoleNavigation SemanticRole = "navigation"
    RoleRead       SemanticRole = "read"
    RoleMutation   SemanticRole = "mutation"
    RoleExecution  SemanticRole = "execution"
    RoleReasoning  SemanticRole = "reasoning"

    RoleSuccess SemanticRole = "success"
    RoleWarning SemanticRole = "warning"
    RoleFailure SemanticRole = "failure"

    RoleMuted    SemanticRole = "muted"
    RoleActive   SemanticRole = "active"
    RoleSelected SemanticRole = "selected"
)
```

A presentation item can have both a semantic kind and a state.

Example:

```text
patch
  role: mutation
  state: success

exec
  role: execution
  state: active
```

Components must request semantic styles from the active theme.

They must not embed terminal colour values directly.

## 4. Tool Call Visual Grammar

Tool calls should be first-class UI elements.

Default form:

```text
⌕ SEARCH   "ResumeThread" · internal/              6 hits  12ms
↳ READ     internal/session/session.go           L80–180    2ms
Δ PATCH    internal/session/session.go             +18 -4    8ms
$ EXEC     go test ./...                          ✓ PASS   2.1s
```

Use a stable structure:

```text
GLYPH  TYPE    PRIMARY                    DETAIL   STATUS
```

Suggested glyph vocabulary:

```text
⌕  search
↳  read
≡  list
Δ  patch
+  write/create
$  exec
◈  model/reasoning
✓  success
✕  failure
!  warning
```

Do not rely on glyphs alone. Text labels must remain present.

## 5. Density States

Telemetry items should support three useful visual states:

### Scan

One compact line.

```text
Δ PATCH   internal/ui/log.go                    +42 -11    7ms
```

### Selected

Selected item receives clearer emphasis.

Avoid changing layout unless useful.

### Expanded

The item opens in place to show detailed information.

Example:

```text
╭─ $ EXEC ────────────────────────────────────────────────╮
│ command   go test ./...                                │
│ cwd       ~/code/ruga                                  │
│ duration  2.1s                                         │
│ exit      1                                            │
│                                                        │
│ --- FAIL: TestResume                                   │
│     expected session abc, got ""                       │
│                                                        │
└────────────────────────────────────────────────────────╯
```

Pressing Enter should toggle expanded state for the selected item.

Do not open a modal for ordinary inspection.

## 6. Long Lines

Compact telemetry lines must never destroy layout.

Long values should:

* truncate to available width
* use a clear truncation marker
* retain the complete value internally
* become visible when expanded

Do not permanently discard information for display purposes.

## 7. Reasoning Presentation

Reasoning/status information should be visually distinct from assistant responses.

Prefer compact status lines such as:

```text
◈ inspecting event dispatch path
◈ checking shutdown behaviour
◈ found ordering issue
```

Reasoning should be quiet enough not to compete with the final response.

Do not dump raw protocol payloads into this area.

## 8. Hidden Activity Indicator

When the telemetry panel is closed, show a compact activity indicator.

Example:

```text
LOG 37
```

It may briefly expose meaningful recent activity:

```text
LOG 41 · PATCH
```

The indicator should not demand attention continuously.

It should communicate that work is happening without turning the primary view into a dashboard.

## 9. Animation

Animations must support comprehension.

Use them only for:

* panel open/close
* active tool state
* turn state transitions
* approval arrival
* subtle hidden-activity feedback

Examples:

```text
◐ ◓ ◑ ◒
```

or:

```text
━━━━━━╸
```

Animation must remain:

* low frequency
* non-blocking
* subtle
* terminal-safe

Do not animate ordinary static information.

Do not blink text.

If an existing Bubble Tea-compatible motion library is suitable, use it rather than building animation infrastructure.

## 10. Themes

Introduce theming after semantic roles exist.

A theme maps semantic meaning to terminal style.

Conceptually:

```go
type Theme struct {
    Navigation Style
    Read       Style
    Mutation   Style
    Execution  Style
    Reasoning  Style

    Success  Style
    Warning  Style
    Failure  Style

    Muted    Style
    Active   Style
    Selected Style
    Border   Style
}
```

The default Ruga theme should use:

* dark background
* mostly restrained text
* bright semantic accents
* thin line work
* limited heavy borders

Avoid colouring every character.

A future theme system may support styles such as:

```text
ruga
amber
ghost
mono
synthwave
```

Do not implement theme selection UI until the core theme abstraction is stable.

## 11. Event Inspector

The event inspector should support:

* scrolling
* item selection
* expansion
* collapse
* follow mode
* manual navigation
* truncated-line inspection

When the user manually scrolls away from the bottom, new events must not force the viewport back down.

Provide a clear indication when unseen events arrive.

Returning to follow mode should be easy.

## 12. Raw Log Access

The semantic event view is not the raw log.

Preserve raw event access for debugging.

Raw event data may be exposed later through:

* an alternate inspector mode
* a debug toggle
* session replay tooling

Do not make raw JSON the default expanded representation.

## 13. Architecture

Keep the event model unchanged unless a genuine missing domain concept is discovered.

Preferred flow:

```text
backend
   ↓
normalized events
   ↓
event bus
   ↓
presentation reducer
   ├─ conversation model
   ├─ telemetry model
   └─ activity model
        ↓
       TUI
```

The presentation layer owns:

* grouping
* coalescing
* truncation metadata
* semantic roles
* display labels
* display status
* expandable details

The backend layer must not contain presentation logic.

## 14. Implementation Slices

### Slice 1 — Presentation Model

Add semantic presentation types.

Map existing events into:

* conversation
* telemetry
* activity

Keep the existing UI working while this is introduced.

Success condition:

The TUI can render from presentation types rather than raw application events.

### Slice 2 — Split Interface

Separate conversation from operational telemetry.

Implement:

* conversation viewport
* event inspector
* fixed split
* toggle visibility
* independent scrolling

Success condition:

The user can work normally without tool events polluting the conversation.

### Slice 3 — Semantic Tool Lines

Add compact renderers for:

* search
* read
* list
* patch
* write
* exec
* model activity
* warnings/errors

Implement consistent geometry and glyphs.

### Slice 4 — Selection and Expansion

Add:

* telemetry cursor
* selected state
* Enter to expand/collapse
* long-line inspection
* expanded command output
* expanded patches/details where available

### Slice 5 — Sliding Panel

Replace the fixed split with an animated inspector. The event stream can be
placed on the right, left, bottom, or top edge with the `-panel` option.

Side placements split the conversation and inspector horizontally. Top and
bottom placements stack them vertically. On narrow terminals, the inspector
continues to use the full-width fallback.

The panel should:

* open smoothly
* close smoothly
* preserve state while hidden
* remain responsive during active streaming

### Slice 6 — Activity and Animation

Add subtle:

* active tool animation
* hidden activity indicator
* unseen event count
* turn-state feedback

Do not animate everything.

### Slice 7 — Theme Foundation

Move all remaining presentation colours and emphasis into the theme abstraction.

Ship one polished default theme.

Do not build a theme marketplace or complex configuration system.

## 15. Non-Goals

Do not add during this phase:

* dashboard grids
* multiple permanent sidebars
* arbitrary user layouts
* widgets unrelated to coding activity
* heavy ASCII art
* noisy animation
* raw JSON everywhere
* theme editors
* plugin APIs
* protocol redesign

## Success Condition

Phase 3 is complete when:

1. conversation is visually separate from operational activity
2. tool activity is compact and highly scannable
3. telemetry items can be selected and expanded
4. long content does not damage the layout
5. the telemetry panel can be hidden without losing awareness of activity
6. semantic roles control visual styling
7. the interface feels dense without becoming noisy
8. Ruga has a recognisable visual identity distinct from conventional coding harnesses
