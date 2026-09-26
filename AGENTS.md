# Repository guidance

- Read the relevant `PRD.md` section before changing behavior. Keep each slice usable and avoid unrelated scope.
- Keep provider SDK types inside `internal/backend/<provider>`. Normalize provider events into `internal/event`, publish them through `internal/bus`, and keep the UI independent of raw provider events.
- Keep `backend.Backend` small. Add narrow optional interfaces for capabilities that not every provider supports; avoid abstractions for hypothetical needs.
- Prefer the existing Go, Bubble Tea, Bubbles, Lip Gloss, and Watermill patterns. Add a dependency only when it provides clear value over the existing stack.
- Keep the TUI compact and responsive. Omit unavailable metadata, preserve unknown events, and avoid blocking backend event processing on rendering.
- Format Go code with `gofmt`. Run `go test ./...` for code changes and add focused tests for changed behavior.
- This checkout uses Jujutsu, not a Git working tree. Use `jj status` and `jj diff` to inspect changes, and `jj commit -m "..."` to commit them.
