# Known Irritations

Things that irritate me or might irritate other humans.

## General

### Can't change Codex backend options

It'd be nice to be able to interactively change Codex backend options like:

- Model
- Effort
- Permissions/Sandboxing

## Composer

The composer is a multi-line input that is the primary method of communication with agents.

### Multiline composer (implemented)

The composer wraps long lines and supports multiple lines. Press Enter to add a
line and Ctrl+S to send. It grows to four rows, then scrolls internally.

### Text selection selects the entire terminal row

Selecting/copying text is a primary activity in TUI agents. We need this work well.

## Conversation Panel

The conversation panel is where you communicate with agents and they communicate with you. It's not for puking large diffs into, flooding with tool calls and logging.

### Status update flooding and render bug (not reproduced on current code)

The reported Codex text was:

 ◈ Status updated: item/startedStatus updated: item/completed

Current normalization routes known item start and completion events to
operational telemetry, not the conversation. Revisit this if it returns, using
a recording of the event that caused it.

### Text selection selcts the entire terminal row

The same issue as above for the composer. Each repsonse should be individually selectable and copyable.

### Resuming a session doesn't show previous turns (implemented)

On resume, the TUI now shows user and assistant messages. Codex reads prior
turns from its thread. The OpenAI-compatible backend keeps a display transcript
separate from the compacted request context.

## Event Inspector

The event inspector is essentially an inspectable firehose. Anything that isn't conversation, any event, should go here. *ALL* events in the inspector should have detailed summaries and information about the source/target of the event etc.

### Filter events by kind

The inspector already has text search with `/`. Add event-kind filters or
presets to hide repetitive events such as `hook/started` and `hook/finished`.

## Approval Pop-in

The approval pop-in is designed to be minimally invasive as possible. It should gently ask for your approval, not demand it.

### Command detail is noisy (improved)

The approval panel keeps the reason and presents structured command actions as
short readable details instead of a JSON blob.
