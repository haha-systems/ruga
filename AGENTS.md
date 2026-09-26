# Repository guidance

- Read the relevant `docs/prds/PRD.md`, `PRD-2.md`, or `PRD-3.md` section before changing behavior. Keep each slice usable and avoid unrelated scope. `PRD-3.md` describes the semantic TUI direction; implement it incrementally rather than in one pass. `docs/ROADMAP.md` records deferred direction (parallel tool calls, context compaction, Ghostdive memory, QAC, CES) and the seams for each; consult it before starting work in those areas.
- The module is `github.com/haha-systems/ruga` and the entry point is `cmd/ruga` (binary name `ruga`). Run it with `go run ./cmd/ruga`.
- Keep provider SDK types inside `internal/backend/<provider>` (`codex`, `openai`). Normalize provider events into `internal/event`, publish them through `internal/bus`, and keep the UI independent of raw provider events.
- Keep `backend.Backend` small. Add narrow optional interfaces for capabilities that not every provider supports (`backend.Interactive`, `ModelDisplay`, `SessionSupport`, `ToolSupport`); type-assert for them in `cmd/ruga` and avoid abstractions for hypothetical needs.
- Reduce normalized events in `internal/presentation`, not in the UI. Styling must flow through semantic roles (`presentation.SemanticRole`) resolved by `internal/ui/theme.go`; never hard-code glyph colours or backend event types in renderers. Preserve the underlying event stream even when the presentation model coalesces or suppresses events.
- Keep durable session state (`internal/session`) separate from event recordings (`internal/recording`). Sessions live under `$XDG_STATE_HOME/ruga/sessions`; JSONL recordings live under `$XDG_DATA_HOME/ruga/sessions`. Never persist API keys; store only the credential environment variable name.
- Provider-neutral tools live in `internal/tool` behind the `tool.Registry`. Tools must keep paths inside the repository, bound their output, and declare `ReadOnly` only when they are safe to run concurrently. `search` needs `rg` and `patch` needs the system `patch` utility.
- Keep the TUI compact and responsive. Omit unavailable metadata, preserve unknown events, and avoid blocking backend event processing on rendering. The event inspector (live and replay) must not force-scroll away from manual navigation.
- Prefer the existing Go, Bubble Tea, Bubbles, Lip Gloss, and Watermill patterns. Add a dependency only when it provides clear value over the existing stack.
- Keep backends and command wiring testable by injecting environment lookups and other side effects rather than calling `os.Getenv` directly.
- Format Go code with `gofmt`. Run `go test ./...` for code changes and add focused tests for changed behavior.
- This checkout uses Jujutsu, not a Git working tree. Use `jj status` and `jj diff` to inspect changes, and `jj commit -m "..."` to commit them.
