# Ruga Roadmap

This document captures direction beyond the implemented PRDs. It records what
has been built, what is being designed, and what is deliberately deferred. It
is a reference for design work, not a commitment to a sequence.

Read the relevant `docs/prds/*` section before changing behaviour; this roadmap
records the *why* and the seams, while the PRDs hold the contracts.

## Status at a glance

| Area | State | Source |
|------|-------|--------|
| Core harness, event bus, recording/replay | Implemented | `PRD.md` |
| Sessions, tool runtime, OpenAI-compatible backend | Implemented | `PRD-2.md` |
| Semantic TUI (presentation, inspector, semantic roles) | In progress | `PRD-3.md` |
| Multiline composer | Design | §6 |
| Markdown rendering | Implemented | §7 |
| Selectable themes | Design (theme carries Markdown style) | §8 |
| Movable event stream | Implemented | §9, `PRD-3.md` Slice 5 |
| Configuration file | Design | §10 |
| Clear focused-panel state | Design | §11 |
| Approval modes | Design | §12 |
| Approval takes focus | Design | §13 |
| Quota stats in the top line | Design | §14 |
| Parallel tool calls | Design | this document, §1 |
| Context compaction for OpenAI-compatible | Design (blocked on usage accounting) | §2 |
| Memory via Ghostdive adapter | Design (blocked on context pressure) | §3 |
| Quota-aware cognition (QAC) | Design (blocked on CES) | §4 |
| Cumulative Epistemic State (CES) | Design (largest change) | §5, `CES.md` |

The cognition features (§1–§5) form one coherent stack rather than five
independent features:

```text
CES      decides WHAT kind of cognition happens next
QAC      decides WHO / which cognitive tier performs it
memory   decides what persists and is recalled
context  decides what fits in the window
tools    decide how the system acts (parallel, bounded)
```

Read them in that dependency order. CES creates the units of cognition that
QAC allocates over; context pressure is what makes memory worth recalling;
parallel tools are how a bounded phase acts quickly.

---

## 1. Parallel tool calls

### Intent

Let a single assistant turn issue several independent tool calls that run at
once, instead of one call per model round trip.

### What already exists

The runtime is already batch-oriented and safe:

- `tool.Registry.Execute(ctx, []tool.Call)` already runs a whole batch
  concurrently when *every* resolved tool declares itself read-only via the
  `tool.ReadOnly` capability (`internal/tool/tool.go`). Results always preserve
  the original call order and panics are recovered per call.
- `echo`, `read`, `search`, and `list` are `ReadOnly`; `patch`, `write`, and
  `exec` are not.
- The OpenAI-compatible stream parser already assembles multiple `tool_calls`
  by index, sorts them, and returns the full set on `assistantResponse.ToolCalls`
  (`internal/backend/openai/backend.go`).

So a multi-call assistant message would already flow end to end. Nothing
currently solicits one.

### Gaps

1. **Nothing asks for parallel calls.** The OpenAI request sends no `system`
   message and no `parallel_tool_calls` field; many compatible servers default
   to sequential tool use unless told otherwise.
2. **All-or-nothing batching.** One non-read-only call forces the entire batch
   sequential. A batch of three reads plus one write runs fully serially.
3. **Timing double-counts.** `turn.completed.tool_elapsed_ms` is the *sum* of
   per-call durations, which overstates wall-clock for a parallel batch.
4. **No UI grouping.** The inspector shows N starts then N completions as flat
   items. The `ActiveTools` counter already handles N concurrent items.

### Seams

- `protocol.go`: add `parallel_tool_calls` and a short system instruction; the
  request type is already local to the provider package.
- `tool.go`: partition a batch into a concurrent read-only phase followed by a
  serialized mutation phase, still preserving input order in results.
- `backend.go`: record batch wall-clock alongside summed per-call durations.

### Constraints

- Parallelism is only as safe as the underlying tools: `search`/`read` shell out
  to `rg` and the filesystem.
- The OpenAI backend has no approvals or interrupt wiring, so a parallel batch
  is cancelled as a unit via `ctx`.
- The Codex backend delegates tool execution to App Server; parallelism there is
  Codex's decision, not Ruga's.

### Trigger

Low risk and independent. Can be done now; it needs no other feature.

---

## 2. Context compaction for the OpenAI-compatible path

### Intent

Keep a long session inside the model's context window without losing the thread
of the work.

### What already exists

- Per tool result bounding: `tool.bound` caps each result at 16 KiB with an
  explicit truncation marker, matching PRD-2 §6.
- `turn.completed` reports `tool_result_bytes` and an estimated token count.

### Gaps

There is **no aggregate context management**. `Submit` replays the full history
every round trip and it grows monotonically for the life of the session. Growth
is bounded per call but unbounded per session; eventually the endpoint rejects
the request on context length.

1. **No usage telemetry for OpenAI.** The stream parser ignores the `usage`
   object, emits no `usage.updated` event, and so Ruga has no signal to trigger
   compaction. Codex gets usage from App Server; the OpenAI path is blind.
   `internal/presentation` already handles a `usage.updated` event, so it would
   render immediately once published.
2. **No session field for a summary.** `session.Session` has messages but no
   summary or compaction timestamp, so a compaction cannot survive resume.
3. **Persisted state and in-memory history are separate.** `persistMessages`
   writes to the store while `b.history` is derived; compaction must rewrite
   both or resume re-inflates.

### The hard part: tool-call pairing

Compaction cannot be naive truncation. Every OpenAI `tool` message must follow
the assistant message carrying its matching `tool_calls` id; dropping the
assistant message orphans the results and most servers reject them. The
compaction unit must be the **turn** — an assistant message plus its calls and
results travel together, or the whole group is replaced by a summary.

### Approaches

- **A. Sliding window + synthesis (preferred first).** Keep the most recent N
  turns verbatim; when the estimate crosses a threshold, replace the oldest
  turns with one synthetic `system` message. The summary may be mechanical or a
  single extra low-cost completion. Deterministic, resumable, testable offline.
- **B. Model-driven compaction.** Near the limit, ask the model to summarise for
  continuation and splice the reply in as the new prefix. Better summaries; an
  extra round trip; requires token accounting first.

### Seams

- Capture `usage` in the stream parser and publish `usage.updated`.
- Trigger inside `Submit`'s loop; store the reduced form on `session.Session`.
- Publish a `context.compacted` event so it flows through presentation and
  recording like every other event.

### Constraints

- Not all compatible servers return `usage`; keep a character-based estimator
  (`(bytes+3)/4` idiom) as a fallback trigger.
- The context limit differs per model; make the threshold configurable with a
  conservative default.
- Providers with prompt caching reward a stable prefix; compact rarely and in
  large steps rather than trimming every turn.
- PRD-2 covers measurement and tool-result narrowing but not window management;
  this is a genuine new domain concept and warrants a PRD note before code.

### Trigger

Blocked on usage accounting, which is also a prerequisite for §4. Do usage
first.

---

## 3. Memory via a Ghostdive adapter

### Intent

Give Ruga durable, governed, cross-session recall, starting with
[Ghostdive](../../ghostdive) as the first implementation.

### Ghostdive in brief

Governed multi-layer memory as a service (`github.com/haha-systems/ghostdive`).
Go HTTP API with a small REST surface:

- `POST /v1/spaces/{spaceID}/memories` — observe
- `POST /v1/spaces/{spaceID}/memories/retrieve` — retrieve
- `POST /v1/spaces/{spaceID}/memories/{recordID}/transitions` — promote/reject

It also ships an MCP server, and its design invariants matter here: reads do not
trigger consolidation; the online path is aggressively low latency; memory
prefixes are stable and cacheable; consolidation proposes but never authorises.

### Why it fits

- The REST surface maps onto additive `tool.Tool` implementations registered in
  the same `tool.NewRegistry(...)` call as the existing tools. No backend
  changes: both Codex (MCP tool calls already normalise) and the OpenAI path
  pick them up for free. Retrieval is read-only and therefore parallelises under
  §1.
- Stable cacheable memory prefixes are the same concern as OpenAI compaction:
  memory becomes the durable prefix, the current request the volatile suffix.
- Codex already speaks MCP, so the cheapest first step for Codex is
  configuration, not code.

### The adapter boundary

Define **Ruga's own** small `internal/memory` interface (roughly
`Observe`/`Recall`) with a `ghostdive` implementation behind it using plain
`net/http`. Do not leak Ghostdive types or its module dependency into the
backends, mirroring the rule that provider SDK types stay inside
`internal/backend/<provider>`. Keep it flag-gated and off by default.

### Two layers

1. **Tool-level (first, smallest).** Memory tools usable by any backend. Config
   by flag/env, consistent with `OPENAI_API_KEY`: only the env var *name* is
   persisted, never the key.
2. **Prefix/session-level (later, more valuable).** Once usage accounting and
   compaction exist, use a Ghostdive snapshot as the stable memory prefix in the
   request messages, with compaction managing everything after it.

### Constraints

- Write policy is a real decision: auto-observe every turn floods a space;
  never observing makes recall useless. Observe freely, but let promotion stay
  governed — do not let Ruga self-promote.
- `ObserveInput` wants structured subject/predicate/object/summary, not a raw
  transcript. Mapping normalised events to episodic records is the actual design
  work.
- No PRD section covers memory today; add one before implementation.

### Trigger

Most valuable once context is scarce (§2) and once there are units of cognition
worth persisting (§5). Ship the tool-level slice first; it is safe and cheap.

---

## 4. Quota-aware cognition (QAC)

### Intent

Choose *which* model or cognitive tier performs an operation, based on
uncertainty and remaining budget, rather than always using one model.

### What QAC is

`github.com/haha-systems/qac` — a dependency-free, deterministic Go library. It
evaluates supplied signals and resource limits. It performs no I/O, invokes no
resources, and mutates no budgets.

- `Resource{ID, Capability, Cost, Scarcity, Available, Metadata}`
- `Context` — normalised uncertainty, importance, novelty, expected gain,
  saturated failed attempts
- `Request{Context, Resources, Budget}`; `BudgetState` holds per-resource
  `Enabled`/`RemainingInvocations`/`Cooldown`
- `Policy.Decide → Decision{Action, From, To, Score, Threshold, Factors, Reason,
  Eligibility}`, where `Action` is `continue`, `escalate`, `release`, or `stop`
- `policy/threshold` — immutable, concurrent-safe, adjacency-only, with separate
  escalation and release thresholds for hysteresis

### The dependency

QAC allocates over *units of cognition*, and Ruga does not have them yet: there
is exactly one model per session. A `Decide` call wants per-operation signals
(uncertainty, novelty, expected gain, failures) and per-resource cost/scarcity/
budget, none of which exist today. So:

- **Coarse (possible now).** Pick between backends/models per session or turn,
  fed by session state. Thin value; essentially a config picker.
- **Real value (needs §5).** Allocate a tier *per phase*: cheap for bounded
  operations, strong where uncertainty warrants it. This is exactly the intended
  separation — CES decides WHAT, QAC decides WHO.

### Seam

A new `internal/alloc` wrapping `qac`, keeping `qac` types inside that package
and feeding it from `usage.updated` (remaining budget) plus session failure
counts. The library surface is small enough to hide behind an interface if we
prefer not to take the import yet.

### Trigger

Blocked on CES. Doing QAC first yields a config picker, not cognition
allocation.

---

## 5. Cumulative Epistemic State (CES)

### Intent

Restructure Ruga's reasoning from "one prompt, one turn" into a deterministic
sequence of bounded cognitive phases over a persistent belief state. See
`docs/CES.md` for the full architecture.

### The core idea

The shared computational object is **not a conversation**; it is the system's
current epistemic state: what it observed, believes, hypothesises, still does
not know, must preserve, intends, did, and saw happen. Cognition is:

```text
TRIAGE → ABDUCE → FRAME → EXECUTE → CLOSE
```

Each phase receives the original task plus a **projection** of the state, does
one kind of reasoning, produces a structured artifact, and stops. Each phase
uses a **fresh model context** rather than the previous transcript. The
orchestrator — not the model — decides which phase runs next. Contradictions
reopen earlier phases, so the process supports belief revision rather than
forward-only progress.

### Why it is the largest change

Today `Submit(prompt)` means "run the model until it stops calling tools", with
the OpenAI backend hiding a 16-round tool loop and Codex delegating to one App
Server thread. CES replaces that with an orchestrator driving several bounded
model operations over an artifact store.

### What it requires

- A **phase engine** above `backend.Backend`; the model becomes the cognitive
  substrate the orchestrator drives.
- An **epistemic state store** — observations, claims, hypotheses, unknowns,
  constraints, frames, actions, outcomes, plus explicit relations
  (`supports`/`contradicts`/`tests`/`depends_on`). Structurally like
  `internal/session` but a separate store; do not overload session messages.
- **Counterevidence-closed projections** — a testable rule: if a hypothesis is
  projected, evidence contradicting it cannot be omitted.
- **Fresh context per phase.** Trivial on the OpenAI path; awkward on Codex,
  whose threads are stateful.

### What already helps

- Phases are still events, so they flow through `internal/bus`, are recorded by
  `internal/recording`, rendered by `internal/presentation`, and replay — the
  existing spine holds unchanged.
- "Limited epistemic authority" maps onto the existing tool split: only EXECUTE
  gets mutating tools; other phases use the read-only set, already expressible
  via `tool.ReadOnly`.

### Relationship to §2

CES's fresh-context-per-phase *is itself* a context-management strategy and is a
partial substitute for heavy sliding-window compaction. They should still be
designed together, since each phase also needs bounded input.

### Constraints

- This is architectural, not a slice. PRD.md calls for vertical slices that leave
  the app usable; CES deserves its own PRD before code.
- Prototype on the OpenAI-compatible path, where fresh contexts are natural and
  the tool loop already lives.
- The OpenAI-compatible backend is currently the only practical substrate for
  phase isolation; Codex's stateful threads resist it.
- "Append-only, revision-aware, no silent overwrite" is a strong invariant worth
  a focused test, in the spirit of Ghostdive's memory invariants.

### Trigger

Do this before QAC (§4), since CES creates the units QAC allocates over.

---

## 6. Multiline composer

### Intent

Let the user compose and edit a multi-line prompt before sending, instead of
submitting on the first Enter.

### What already exists

The composer is a single-line `textinput.Model` (`internal/ui/app.go`), so the
input surface is one row and Enter always submits. Pressing Enter starts a turn
immediately.

### Approach

Swap `textinput` for `bubbles/textarea`, which is already available through the
existing Bubbles dependency. Then choose a submission convention, since Enter
can no longer mean "send":

- **Ctrl+S or Alt+Enter to submit**, Enter inserts a newline (common in
  terminal agents). Predictable, but a changed muscle memory.
- **Enter submits with an explicit continuation affordance**, and a trailing
  backslash or unclosed fence inserts a newline.

The first is simpler and easier to document in the footer keys.

### Seams

- `internal/ui/app.go`: `model.input` becomes a `textarea.Model`; `resize` and
  the footer keys change.
- The composer grows with content up to a cap, then scrolls internally, so the
  chrome height calculation in `resize` must account for a variable number of
  composer rows.
- `fitLine` and the prompt-width math already exist and are reused.

### Constraints

- Keep Enter-submits as an option if multiline becomes a mode, to avoid
  surprising existing users.
- Do not let a growing composer crowd out the conversation or the inspector;
  cap its height and scroll it.
- Pasted text with newlines already arrives with newlines; make sure paste does
  not accidentally submit.

### Trigger

Independent and self-contained. Safe to do any time; low risk.

---

## 7. Markdown rendering — implemented

Completed assistant responses render as styled Markdown via
`charmbracelet/glamour` (`internal/ui/markdown.go`). `renderAssistantMarkdown`
computes the plain wrapped text first and upgrades to glamour only when the
terminal is wide enough (`minimumMarkdownWidth`) and colour is supported;
otherwise it returns the plain text with `rendered=false` and the caller keeps
the normal text style. Styling flows through `Theme.MarkdownStyle`, so Markdown
and the semantic roles share one theme (`internal/ui/theme.go`).

How it landed against the original plan:

- **Assistant-only.** Only `ConversationAssistant` items render as Markdown;
  reasoning/status lines keep their compact semantic form (PRD-3 §7) and tool
  telemetry keeps its glyph grammar (PRD-3 §4). Tool and command output are
  never rendered as Markdown.
- **On completion, not per delta.** Rendering keys off a new
  `ConversationItem.Completed` flag (`internal/presentation`) instead of
  re-rendering on every `message.delta`, which was the main risk in the original
  design.
- **Faithful source.** The presentation layer keeps the raw text; Markdown is a
  display concern only, so copied text stays exact.
- **Graceful fallback.** Narrow widths and colourless terminals fall back to the
  existing plain wrapped text (`fitLine`/`wrapPreservingLines` unchanged).

New dependency: `glamour` (with its transitive deps), justified by the clear
value over hand-rolled Markdown. The §8 theme work already has the
`MarkdownStyle` field it needs.

---

## 8. Selectable themes

### Intent

Let the user choose a visual theme, building on the semantic-role abstraction
already introduced by the semantic TUI.

### What already exists

`internal/ui/theme.go` already defines a `Theme` with semantic roles, canvas,
text, title, divider, and approval-panel styles, and a single built-in default.
PRD-3 §10 anticipated named themes (`ruga`, `amber`, `ghost`, `mono`,
`synthwave`) but explicitly deferred selection UI until the abstraction is
stable. The abstraction now exists; what is missing is more than one theme and a
way to pick one.

### Approach

- Add a small set of named themes as additional `Theme` values, reusing the same
  semantic roles. No renderer changes are needed — that is the point of the
  abstraction.
- Select via a flag/environment variable (consistent with `-backend`,
  `-openai-model`), not a full config system. Example: `-theme mono`.
- Persist the choice in session state so resume keeps it, following the pattern
  used for backend/model.

### Seams

- `internal/ui/theme.go`: a registry of named themes plus a lookup by name.
- `cmd/ruga/main.go`: a `-theme` flag wired into `ui.Config`.
- `internal/session`: store the chosen theme alongside backend/model on resume.

### Constraints

- Honour PRD-3 §15: do not build a theme marketplace, editor, or complex
  configuration.
- Every theme must keep contrast and readability; a `mono` theme must work
  without colour assumptions.
- Markdown rendering (§7) should consume the same theme so the two do not drift.

### Trigger

Depends conceptually on the semantic TUI theme abstraction being settled (PRD-3
Slice 7). Small and independent otherwise.

---

## 9. Movable event stream — implemented

The `-panel` option places the event stream on the right, left, bottom, or top
edge. Side placements split the available width; top and bottom placements
split the available height. The inspector retains its animated open/close
behaviour, and narrow terminals keep the full-width inspector fallback. The
placement contract and layout are documented in `PRD-3.md` Slice 5.

---

## 10. Configuration file

### Intent

Let users keep a small set of Ruga defaults across runs without repeating
flags, while keeping command-line overrides obvious.

### Design

- Use a human-editable file under the user's XDG config directory.
- Define precedence as command-line flags, then file values, then built-in
  defaults. Report malformed or unsupported values with the setting name.
- Start with stable user preferences such as panel placement and theme. Decide
  whether backend and model belong in this file separately from their existing
  session-resume behavior.

### Seams and constraints

- Keep loading and precedence in command setup, with environment lookup
  injectable for tests.
- Persist credential environment-variable names only; never persist API keys.
- Choose a simple file format before implementation and avoid a large config
  framework for this small set of settings.
- Test precedence, defaults, invalid values, and missing files.

### Trigger

Useful once a few stable preferences have accumulated. The existing flags remain
the contract while the file format and supported keys are designed.

---

## 11. Clear focused-panel state

### Intent

Make the pane that currently receives keyboard navigation easy to identify at
a glance, including when the conversation and event stream are side by side or
stacked.

### Approach

- Give the focused pane a stronger heading or boundary treatment and keep the
  unfocused pane visually quieter.
- Reflect actual keyboard focus, not merely which pane has recent activity.
- Resolve emphasis through semantic theme roles so every theme, including
  monochrome, can show the distinction.

### Seams and constraints

- `internal/ui/app.go` owns focus and pane headings; `internal/ui/theme.go`
  resolves semantic styling.
- Keep the indicator legible in narrow-terminal fallback and all four panel
  placements. Do not rely on colour alone.
- Preserve existing navigation and inspector scroll position.

### Trigger

Independent UI refinement now that movable panel placement is implemented.

---

## 12. Approval modes

### Intent

Let users choose an explicit approval policy for operations that request
permission, with clear and predictable behavior across supported backends.

### Design

Specify the available modes and their exact effects before implementation. Keep
the current ask-before-approval behavior as the default, and define which
operations each mode can approve, reject, or leave to an interactive prompt.

### Seams and constraints

- Keep policy provider-neutral in command/session configuration; adapt it only
  through the backend's approval capability.
- A backend that cannot enforce a selected mode must report that limitation
  rather than silently applying a different policy.
- Never let a broad convenience mode obscure which operation is being approved.

### Trigger

Design after the configuration-file settings and backend capability boundaries
are understood. Approval policy affects execution, so define its contract in a
PRD before changing behavior.

---

## 13. Approval takes focus

### Intent

When an approval request arrives, move keyboard focus to its decision controls
so the request cannot be missed while the user is working in another pane.

### Behavior to define

- Save the current focus target when the approval panel opens and restore it
  after the request is resolved or dismissed.
- Make the pending request visibly urgent while keeping its requested action
  and available decisions clear.
- Define behavior for multiple pending requests and for read-only replay, where
  no live decision can be submitted.

### Seams and constraints

- Coordinate the approval state in `internal/ui/app.go` with normalized
  approval events and the optional `backend.Interactive` capability.
- Keep manual event-inspector navigation stable when focus changes, consistent
  with the PRD-3 inspector contract.

### Trigger

Pair with approval-mode design (§12), while preserving a small independent UI
slice that improves the existing interactive approval flow.

---

## 14. Quota stats in the top line

### Intent

Show a compact view of available quota or usage in the top status line, where it
can inform the user without taking space from the conversation or inspector.

### Approach

- Render only quota fields supplied by the backend and omit unavailable
  metadata. Keep the line compact and use semantic theme roles.
- Distinguish provider-reported quota from token usage or local estimates; do
  not present an estimate as an account limit.
- Normalize provider data into the event/presentation model before rendering.

### Seams and constraints

- `internal/backend/<provider>` captures provider usage; `internal/event` and
  `internal/presentation` normalize and summarize it; `internal/ui` renders the
  top line.
- Reuse `usage.updated` where its fields match. Define a separate normalized
  field when quota and per-request token usage differ.
- OpenAI-compatible usage accounting (§2) is a prerequisite for displaying
  those fields on that backend; retain omission for providers without quota
  data.

### Trigger

Start with the provider that already exposes reliable quota data, then add
providers as their normalized usage becomes available.

---

## Dependency summary

```text
usage accounting ──▶ context compaction (§2)
        │
        └──────────▶ QAC budgets (§4)

CES (§5) ──▶ QAC (§4)
CES (§5) ──▶ persisted trajectories ──▶ memory (§3)

parallel tools (§1) — independent, do anytime
```

Interface track, largely independent of the cognition stack:

```text
markdown rendering (§7) ──▶ themes (§8)   share the theme abstraction
multiline composer (§6)   — independent
focused-panel state (§11) — after movable event stream (§9)
approval takes focus (§13) — builds on approval flow
```

Cross-cutting roadmap dependencies:

```text
configuration file (§10) ──▶ stable user preference defaults
approval modes (§12) ──▶ approval focus behavior (§13)
usage accounting (§2) ──▶ quota stats in top line (§14)
```

Suggested order if pursued: **§1** (cheap, independent) → **usage accounting** →
**§2** → **§5** → **§4** → **§3** prefix layer. The tool-level memory slice of
§3 can land at any time. The interface track (§6–§11, §13–§14) can run in
parallel with or between the cognition work; §7 is done and §8 (which builds on
the `MarkdownStyle` seam it added) is next there, followed by **§6**, **§9**,
**§11**, then **§14** once usage data is available. Approval work (§12–§13) and
the configuration file (§10) are cross-cutting and need their contracts settled
before implementation. None of
these block the cognition stack.

## Non-goals for this roadmap

Consistent with PRD.md §9 and PRD-3 §15, this roadmap does not add:

- multi-agent orchestration or remote workers
- plugin or protocol redesign
- a memory UI or dashboard
- a theme marketplace or complex configuration
- provider feature parity for its own sake

Each feature above is adopted only when it makes the harness more useful to
dogfood, which remains the primary measure of progress.
