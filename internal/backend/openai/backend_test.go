package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/haha-systems/ruga/internal/backend"
	"github.com/haha-systems/ruga/internal/bus"
	"github.com/haha-systems/ruga/internal/event"
	"github.com/haha-systems/ruga/internal/session"
	"github.com/haha-systems/ruga/internal/tool"
)

func TestBackendStreamsNormalizedEventsAndKeepsConversation(t *testing.T) {
	var requests []completionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s, want POST /v1/chat/completions", r.Method, r.URL.Path)
		}

		if got := r.Header.Get("Authorization"); got != "Bearer test-secret" {
			t.Errorf("Authorization = %q, want bearer test key", got)
		}

		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("Accept = %q, want text/event-stream", got)
		}

		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}

		requests = append(requests, request)
		if request.Model != "test-model" || !request.Stream {
			t.Errorf("request model/stream = %q/%v", request.Model, request.Stream)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 1 {
			_, _ = fmt.Fprint(w, "data: {\"id\":\"c1\",\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\"hel\"},\"finish_reason\":null}]}\r\n\r\n")
			_, _ = fmt.Fprint(w, "data: {\"id\":\"c1\",\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":null}]}\r\n\r\n")
		} else {
			if len(request.Messages) != 3 || request.Messages[0].Content != "hi" || request.Messages[1].Content != "hello" || request.Messages[2].Content != "again" {
				t.Errorf("second request history = %+v", request.Messages)
			}

			_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		}

		_, _ = fmt.Fprint(w, "data: [DONE]\r\n\r\n")
	}))
	defer server.Close()

	var contract backend.Backend = New(Config{BaseURL: server.URL + "/v1", Model: "test-model", APIKey: "test-secret"})
	client := contract.(*Backend)
	if display, ok := contract.(backend.ModelDisplay); !ok || display.ModelName() != "test-model" {
		t.Fatalf("model display capability missing or wrong: %v", ok)
	}

	events := &captureBus{}
	if err := contract.Start(context.Background(), events); err != nil {
		t.Fatalf("Start(): %v", err)
	}

	if err := contract.Submit(context.Background(), "hi"); err != nil {
		t.Fatalf("Submit(): %v", err)
	}

	firstKinds := kinds(events.snapshot())
	wantFirst := []string{"backend.connected", "user.message", "turn.started", "message.started", "message.delta", "message.delta", "message.completed", "turn.completed"}
	if !reflect.DeepEqual(firstKinds, wantFirst) {
		t.Fatalf("event kinds = %v, want %v", firstKinds, wantFirst)
	}

	got := events.snapshot()
	if got[4].Summary != "hel" || got[5].Summary != "lo" || got[6].Summary != "hello" {
		t.Fatalf("normalized assistant content events = %+v", got[4:7])
	}

	for _, ev := range got {
		if ev.Backend != "openai" || ev.ID == "" || ev.Timestamp.IsZero() {
			t.Fatalf("event is missing normalized metadata: %+v", ev)
		}
	}

	if err := contract.Submit(context.Background(), "again"); err != nil {
		t.Fatalf("second Submit(): %v", err)
	}

	if len(requests) != 2 || len(requests[0].Messages) != 1 || len(requests[1].Messages) != 3 {
		t.Fatalf("request conversation lengths = %d requests, %d then %d messages", len(requests), len(requests[0].Messages), len(requests[1].Messages))
	}

	_ = client.Close()
}

func TestBackendOmitsParallelToolCallsWithoutTools(t *testing.T) {
	var request completionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL + "/v1", Model: "test-model"})
	if err := client.Start(context.Background(), &captureBus{}); err != nil {
		t.Fatalf("Start(): %v", err)
	}

	if err := client.Submit(context.Background(), "hello"); err != nil {
		t.Fatalf("Submit(): %v", err)
	}

	_ = client.Close()
	if request.ParallelToolCalls != nil {
		t.Fatalf("parallel_tool_calls = %v, want omitted without tools", *request.ParallelToolCalls)
	}

	if len(request.Messages) != 1 || request.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v, want no system instruction without tools", request.Messages)
	}
}

func TestBackendPublishesUsageFromStream(t *testing.T) {
	var request completionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":120,\"completion_tokens\":30,\"total_tokens\":150}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL + "/v1", Model: "test-model"})
	events := &captureBus{}
	if err := client.Start(context.Background(), events); err != nil {
		t.Fatalf("Start(): %v", err)
	}

	if err := client.Submit(context.Background(), "hi"); err != nil {
		t.Fatalf("Submit(): %v", err)
	}

	_ = client.Close()
	if request.StreamOptions == nil || !request.StreamOptions.IncludeUsage {
		t.Fatalf("stream_options = %+v, want include_usage", request.StreamOptions)
	}

	var usageEvents []event.Event
	for _, ev := range events.snapshot() {
		if ev.Kind == "usage.updated" {
			usageEvents = append(usageEvents, ev)
		}
	}

	if len(usageEvents) != 1 {
		t.Fatalf("usage events = %+v, want exactly one usage.updated", usageEvents)
	}

	got := usageEvents[0]
	if got.Summary != "120 in · 30 out · 150 total tokens" {
		t.Fatalf("usage summary = %q", got.Summary)
	}

	if got.Data["input_tokens"] != 120 || got.Data["output_tokens"] != 30 || got.Data["total_tokens"] != 150 {
		t.Fatalf("usage data = %+v", got.Data)
	}

	if got.TurnID == "" || got.Backend != "openai" {
		t.Fatalf("usage event metadata = %+v", got)
	}
}

func TestBackendDerivesUsageTotalWhenAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":5}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL + "/v1", Model: "test-model"})
	events := &captureBus{}
	if err := client.Start(context.Background(), events); err != nil {
		t.Fatalf("Start(): %v", err)
	}

	if err := client.Submit(context.Background(), "hi"); err != nil {
		t.Fatalf("Submit(): %v", err)
	}

	_ = client.Close()
	var usageEvents []event.Event
	for _, ev := range events.snapshot() {
		if ev.Kind == "usage.updated" {
			usageEvents = append(usageEvents, ev)
		}
	}

	if len(usageEvents) != 1 || usageEvents[0].Data["total_tokens"] != 12 {
		t.Fatalf("derived usage = %+v", usageEvents)
	}
}

func TestBackendOmitsUsageWhenServerReportsNone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL + "/v1", Model: "test-model"})
	events := &captureBus{}
	if err := client.Start(context.Background(), events); err != nil {
		t.Fatalf("Start(): %v", err)
	}

	if err := client.Submit(context.Background(), "hi"); err != nil {
		t.Fatalf("Submit(): %v", err)
	}

	_ = client.Close()
	for _, ev := range events.snapshot() {
		if ev.Kind == "usage.updated" {
			t.Fatalf("unexpected usage.updated without server usage: %+v", ev)
		}
	}
}

func TestBackendCompactsContextAndSurvivesResume(t *testing.T) {
	var requests []completionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}

		requests = append(requests, request)
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]string{"content": "ok"}}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	store := session.NewStore(t.TempDir())

	// Seed a long transcript: five completed turns whose size comfortably
	// crosses the compaction threshold for the configured window.
	state := session.New("openai", "/repo")
	state.Model = "test-model"
	padding := strings.Repeat("x", 200)
	for i := 0; i < 5; i++ {
		state.Messages = append(state.Messages,
			session.Message{Role: "user", Content: fmt.Sprintf("request %d %s", i, padding)},
			session.Message{Role: "assistant", Content: fmt.Sprintf("answer %d %s", i, padding)},
		)
	}

	if err := store.Save(state); err != nil {
		t.Fatalf("save seeded session: %v", err)
	}

	client := New(Config{BaseURL: server.URL + "/v1", Model: "test-model", ContextLimit: 400})
	client.ConfigureSession(state, false, store.Save)
	events := &captureBus{}
	if err := client.Start(context.Background(), events); err != nil {
		t.Fatalf("Start(): %v", err)
	}

	if err := client.Submit(context.Background(), "next request"); err != nil {
		t.Fatalf("Submit(): %v", err)
	}

	_ = client.Close()

	// The request carried a compacted prefix: one summary plus the kept turns.
	if len(requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(requests))
	}

	first := requests[0].Messages
	if first[0].Role != "system" || !strings.Contains(first[0].Content, "[compacted history]") {
		t.Fatalf("request did not start with a compaction summary: %+v", first)
	}

	var compacted bool
	for _, ev := range events.snapshot() {
		if ev.Kind == "context.compacted" {
			compacted = true
		}
	}

	if !compacted {
		t.Fatalf("no context.compacted event published: %+v", events.snapshot())
	}

	// The compaction is durable: resume reconstructs the reduced transcript.
	reloaded, err := store.Load(state.ID)
	if err != nil {
		t.Fatalf("load saved session: %v", err)
	}

	if reloaded.Summary == "" || reloaded.CompactedAt.IsZero() {
		t.Fatalf("session did not persist compaction state: %+v", reloaded)
	}

	if reloaded.Messages[0].Role != "system" || !strings.Contains(reloaded.Messages[0].Content, "[compacted history]") {
		t.Fatalf("persisted history was not compacted: %+v", reloaded.Messages)
	}

	if len(reloaded.Transcript) != 12 || !strings.HasPrefix(reloaded.Transcript[0].Content, "request 0") {
		t.Fatalf("compacted session lost its display transcript: %+v", reloaded.Transcript)
	}

	resumed := New(Config{BaseURL: server.URL + "/v1", Model: "test-model", ContextLimit: 400})
	resumed.ConfigureSession(reloaded, true, store.Save)
	resumedEvents := &captureBus{}
	if err := resumed.Start(context.Background(), resumedEvents); err != nil {
		t.Fatalf("resumed Start(): %v", err)
	}

	if got := resumedEvents.snapshot(); len(got) < 12 || got[1].Kind != "user.message" || got[1].Summary != reloaded.Transcript[0].Content {
		t.Fatalf("resume did not restore the full display transcript: %+v", got)
	}

	if err := resumed.Submit(context.Background(), "after resume"); err != nil {
		t.Fatalf("resumed Submit(): %v", err)
	}

	_ = resumed.Close()
	last := requests[len(requests)-1].Messages
	if last[0].Role != "system" || !strings.Contains(last[0].Content, "[compacted history]") {
		t.Fatalf("resume re-inflated the transcript: %+v", last)
	}
}

func TestModelCompactionUsesModelSummary(t *testing.T) {
	var requests []completionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}

		requests = append(requests, request)
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			chunk, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{"delta": map[string]string{"content": "ok"}}},
				"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
			})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}

		// The summariser request is non-streaming.
		if len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[1].Role != "user" {
			t.Errorf("summariser messages = %+v", request.Messages)
		}

		if request.Messages[1].Content == "" {
			t.Errorf("summariser transcript is empty")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"User asked to compact the transcript."}}]}`)
	}))
	defer server.Close()
	store := session.NewStore(t.TempDir())
	state := session.New("openai", "/repo")
	state.Model = "test-model"
	padding := strings.Repeat("x", 200)
	for i := 0; i < 5; i++ {
		state.Messages = append(state.Messages,
			session.Message{Role: "user", Content: fmt.Sprintf("request %d %s", i, padding)},
			session.Message{Role: "assistant", Content: fmt.Sprintf("answer %d %s", i, padding)},
		)
	}

	client := New(Config{BaseURL: server.URL + "/v1", Model: "test-model", ContextLimit: 400, Compaction: CompactionModel})
	client.ConfigureSession(state, false, store.Save)
	events := &captureBus{}
	if err := client.Start(context.Background(), events); err != nil {
		t.Fatalf("Start(): %v", err)
	}

	if err := client.Submit(context.Background(), "next"); err != nil {
		t.Fatalf("Submit(): %v", err)
	}

	_ = client.Close()

	// The first request must be the non-streaming summariser, followed by the
	// streamed turn that carries the model-produced summary.
	if len(requests) < 2 || requests[0].Stream {
		t.Fatalf("first request was not the summariser: %+v", requests)
	}

	var streamedReq *completionRequest
	for i := range requests {
		if requests[i].Stream {
			streamedReq = &requests[i]
			break
		}
	}

	if streamedReq == nil {
		t.Fatalf("no streamed request followed compaction: %+v", requests)
	}

	if streamedReq.Messages[0].Role != "system" || !strings.Contains(streamedReq.Messages[0].Content, "User asked to compact the transcript.") {
		t.Fatalf("model summary was not used as the prefix: %+v", streamedReq.Messages[0])
	}

	var strategy string
	for _, ev := range events.snapshot() {
		if ev.Kind == "context.compacted" {
			strategy, _ = ev.Data["strategy"].(string)
		}
	}

	if strategy != string(CompactionModel) {
		t.Fatalf("context.compacted strategy = %q, want %q", strategy, CompactionModel)
	}
}

func TestModelCompactionFallsBackWhenSummariserFails(t *testing.T) {
	var summariserCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}

		if !request.Stream {
			summariserCalls++
			http.Error(w, `{"error":"summariser unavailable"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]string{"content": "ok"}}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	store := session.NewStore(t.TempDir())
	state := session.New("openai", "/repo")
	state.Model = "test-model"
	padding := strings.Repeat("x", 200)
	for i := 0; i < 5; i++ {
		state.Messages = append(state.Messages,
			session.Message{Role: "user", Content: fmt.Sprintf("request %d %s", i, padding)},
			session.Message{Role: "assistant", Content: fmt.Sprintf("answer %d %s", i, padding)},
		)
	}

	client := New(Config{BaseURL: server.URL + "/v1", Model: "test-model", ContextLimit: 400, Compaction: CompactionModel})
	client.ConfigureSession(state, false, store.Save)
	events := &captureBus{}
	if err := client.Start(context.Background(), events); err != nil {
		t.Fatalf("Start(): %v", err)
	}

	if err := client.Submit(context.Background(), "next"); err != nil {
		t.Fatalf("Submit() failed despite fallback: %v", err)
	}

	_ = client.Close()
	if summariserCalls == 0 {
		t.Fatalf("summariser was never attempted")
	}

	var compacted bool
	var strategy string
	for _, ev := range events.snapshot() {
		if ev.Kind == "context.compacted" {
			compacted = true
			strategy, _ = ev.Data["strategy"].(string)
		}

		if ev.Kind == "error" {
			t.Fatalf("summariser failure surfaced as a turn error: %+v", ev)
		}
	}

	if !compacted {
		t.Fatalf("compaction did not fall back: %+v", events.snapshot())
	}

	if strategy != string(CompactionDeterministic) {
		t.Fatalf("fallback strategy = %q, want %q", strategy, CompactionDeterministic)
	}
}

func TestDeterministicCompactionDefaultUnchanged(t *testing.T) {
	reduced, summary, dropped := compactMessages([]session.Message{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "a"},
		{Role: "user", Content: "two"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "three"},
		{Role: "assistant", Content: "c"},
	}, 1, maxSummaryBytes)
	if dropped != 4 || !strings.Contains(summary, compactionMarker) || !strings.Contains(reduced[0].Content, "one") {
		t.Fatalf("deterministic compaction = summary %q dropped %d", summary, dropped)
	}
}

func TestBackendPublishesHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"invalid model"}`, http.StatusBadRequest)
	}))
	defer server.Close()
	backendClient := New(Config{BaseURL: server.URL, Model: "bad-model"})
	events := &captureBus{}
	if err := backendClient.Start(context.Background(), events); err != nil {
		t.Fatalf("Start(): %v", err)
	}

	err := backendClient.Submit(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "400 Bad Request") || !strings.Contains(err.Error(), "invalid model") {
		t.Fatalf("Submit() error = %v, want HTTP status and response body", err)
	}

	got := events.snapshot()
	if got[len(got)-1].Kind != "error" || !strings.Contains(got[len(got)-1].Summary, "invalid model") {
		t.Fatalf("last event = %+v, want normalized error", got[len(got)-1])
	}
}

func TestBackendPublishesMalformedStreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: not-json\n\n")
	}))
	defer server.Close()
	backendClient := New(Config{BaseURL: server.URL, Model: "test-model"})
	events := &captureBus{}
	if err := backendClient.Start(context.Background(), events); err != nil {
		t.Fatalf("Start(): %v", err)
	}

	if err := backendClient.Submit(context.Background(), "hello"); err == nil || !strings.Contains(err.Error(), "decode OpenAI-compatible stream") {
		t.Fatalf("Submit() error = %v, want stream decoding error", err)
	}

	got := events.snapshot()
	if got[len(got)-1].Kind != "error" {
		t.Fatalf("last event kind = %q, want error", got[len(got)-1].Kind)
	}
}

func TestBackendResumesPersistedConversation(t *testing.T) {
	var requests []completionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}

		requests = append(requests, request)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"continued\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	store := session.NewStore(t.TempDir())
	state := session.New("openai", "/repo")
	state.Model = "test-model"
	if err := store.Save(state); err != nil {
		t.Fatalf("save initial session: %v", err)
	}

	first := New(Config{BaseURL: server.URL + "/v1", Model: "test-model"})
	first.ConfigureSession(state, false, store.Save)
	if err := first.Start(context.Background(), &captureBus{}); err != nil {
		t.Fatalf("first Start(): %v", err)
	}

	if err := first.Submit(context.Background(), "my code is amber-47"); err != nil {
		t.Fatalf("first Submit(): %v", err)
	}

	_ = first.Close()

	saved, err := store.Load(state.ID)
	if err != nil {
		t.Fatalf("load saved session: %v", err)
	}

	if len(saved.Messages) != 2 || saved.Messages[1].Content != "continued" {
		t.Fatalf("persisted messages = %+v", saved.Messages)
	}

	if len(saved.Transcript) != 2 || saved.Transcript[0].Content != "my code is amber-47" || saved.Transcript[1].Content != "continued" {
		t.Fatalf("persisted display transcript = %+v", saved.Transcript)
	}

	resumedBus := &captureBus{}
	resumed := New(Config{BaseURL: server.URL + "/v1", Model: "test-model"})
	resumed.ConfigureSession(saved, true, store.Save)
	if err := resumed.Start(context.Background(), resumedBus); err != nil {
		t.Fatalf("resumed Start(): %v", err)
	}

	if err := resumed.Submit(context.Background(), "what code did I give you?"); err != nil {
		t.Fatalf("resumed Submit(): %v", err)
	}

	_ = resumed.Close()
	if got := requests[1].Messages; len(got) != 3 || got[0].Content != "my code is amber-47" || got[1].Content != "continued" || got[2].Content != "what code did I give you?" {
		t.Fatalf("resumed request messages = %+v", got)
	}

	if requests[0].Model != "test-model" || requests[1].Model != "test-model" {
		t.Fatalf("request models = %q, %q", requests[0].Model, requests[1].Model)
	}

	gotEvents := resumedBus.snapshot()
	if gotEvents[0].ThreadID != state.ID || gotEvents[1].Kind != "user.message" || gotEvents[1].Summary != "my code is amber-47" {
		t.Fatalf("resume events do not preserve session identity: %+v", gotEvents[:2])
	}

	if gotEvents[2].Kind != "message.completed" || gotEvents[2].Summary != "continued" {
		t.Fatalf("resumed assistant history = %+v", gotEvents[:4])
	}
}

func TestPersistMessagesBackfillsTranscriptForOlderSessions(t *testing.T) {
	store := session.NewStore(t.TempDir())
	state := session.New("openai", "/repo")
	state.Messages = []session.Message{
		{Role: "user", Content: "earlier prompt"},
		{Role: "assistant", Content: "earlier answer"},
	}
	backendClient := New(Config{Model: "test-model"})
	backendClient.ConfigureSession(state, false, store.Save)

	if err := backendClient.persistMessages(session.Message{Role: "user", Content: "new prompt"}); err != nil {
		t.Fatalf("persistMessages(): %v", err)
	}

	saved, err := store.Load(state.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}

	if len(saved.Transcript) != 3 || saved.Transcript[0].Content != "earlier prompt" || saved.Transcript[1].Content != "earlier answer" || saved.Transcript[2].Content != "new prompt" {
		t.Fatalf("backfilled transcript = %+v", saved.Transcript)
	}
}

func TestBackendStreamsExecutesAndPersistsMultipleToolCalls(t *testing.T) {
	var requests []completionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}

		requests = append(requests, request)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 1 {
			if len(request.Tools) != 1 || request.Tools[0].Function.Name != "echo" {
				t.Errorf("tool definitions = %+v, want echo", request.Tools)
			}

			_, _ = fmt.Fprint(
				w,
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-A\",\"type\":\"function\",\"function\":{\"name\":\"echo\",\"arguments\":\"{\\\"te\"}}]}}]}\n\n",
			)
			_, _ = fmt.Fprint(
				w,
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"call-B\",\"type\":\"function\",\"function\":{\"name\":\"echo\",\"arguments\":\"{\\\"text\\\":\\\"two\\\"}\"}},{\"index\":0,\"function\":{\"arguments\":\"xt\\\":\\\"one\\\"}\"}}]}}]}\n\n",
			)
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}

		if request.ParallelToolCalls == nil || !*request.ParallelToolCalls {
			t.Errorf("parallel_tool_calls = %v, want true when tools are offered", request.ParallelToolCalls)
		}

		if len(request.Messages) == 0 || request.Messages[0].Role != "system" || !strings.Contains(request.Messages[0].Content, "tool calls") {
			t.Errorf("system instruction = %+v, want tool batching guidance", request.Messages)
		}

		if len(requests) == 2 && len(request.Messages) != 5 {
			t.Errorf("follow-up messages = %+v, want system + user + assistant calls + 2 results", request.Messages)
		} else if len(requests) == 2 {
			assistant := request.Messages[2]
			if len(assistant.ToolCalls) != 2 || assistant.ToolCalls[0].ID != "call-A" || assistant.ToolCalls[1].ID != "call-B" {
				t.Errorf("assistant tool calls = %+v", assistant.ToolCalls)
			}

			if request.Messages[3].Role != "tool" || request.Messages[3].ToolCallID != "call-A" || request.Messages[3].Content != "one" {
				t.Errorf("first tool result = %+v", request.Messages[3])
			}

			if request.Messages[4].Role != "tool" || request.Messages[4].ToolCallID != "call-B" || request.Messages[4].Content != "two" {
				t.Errorf("second tool result = %+v", request.Messages[4])
			}
		} else if len(request.Messages) != 7 {
			t.Errorf("resumed message count = %d, want 7: %+v", len(request.Messages), request.Messages)
		} else if len(request.Messages[2].ToolCalls) != 2 || request.Messages[2].ToolCalls[0].ID != "call-A" || request.Messages[3].ToolCallID != "call-A" || request.Messages[4].ToolCallID != "call-B" || request.Messages[5].Content != "done" || request.Messages[6].Content != "continue after restart" {
			t.Errorf("resumed messages did not reconstruct the tool transcript: %+v", request.Messages)
		}

		answer := "done"
		if len(requests) == 3 {
			answer = "resumed"
		}

		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": answer}}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	store := session.NewStore(t.TempDir())
	state := session.New("openai", "/repo")
	registry, err := tool.NewRegistry(tool.Echo{})
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}

	client := New(Config{BaseURL: server.URL + "/v1", Model: "test-model"})
	client.ConfigureSession(state, false, store.Save)
	client.ConfigureTools(registry)
	events := &captureBus{}
	if err := client.Start(context.Background(), events); err != nil {
		t.Fatalf("Start(): %v", err)
	}

	if err := client.Submit(context.Background(), "echo both values"); err != nil {
		t.Fatalf("Submit(): %v", err)
	}

	_ = client.Close()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(requests))
	}

	saved, err := store.Load(state.ID)
	if err != nil {
		t.Fatalf("load saved session: %v", err)
	}

	if len(saved.Messages) != 5 || len(saved.Messages[1].ToolCalls) != 2 || saved.Messages[1].ToolCalls[0].ID != "call-A" || saved.Messages[2].ToolCallID != "call-A" ||
		saved.Messages[3].ToolCallID != "call-B" ||
		saved.Messages[4].Content != "done" {

		t.Fatalf("persisted tool conversation = %+v", saved.Messages)
	}

	var toolEvents []event.Event
	for _, ev := range events.snapshot() {
		if ev.Kind == "tool.started" || ev.Kind == "tool.completed" {
			toolEvents = append(toolEvents, ev)
		}
	}

	if len(toolEvents) != 4 || toolEvents[0].ItemID != "call-A" || toolEvents[1].ItemID != "call-B" || toolEvents[2].ItemID != "call-A" || toolEvents[3].ItemID != "call-B" {
		t.Fatalf("normalized tool events = %+v", toolEvents)
	}

	resumed := New(Config{BaseURL: server.URL + "/v1", Model: "test-model"})
	resumed.ConfigureSession(saved, true, store.Save)
	resumed.ConfigureTools(registry)
	resumedEvents := &captureBus{}
	if err := resumed.Start(context.Background(), resumedEvents); err != nil {
		t.Fatalf("resumed Start(): %v", err)
	}

	if err := resumed.Submit(context.Background(), "continue after restart"); err != nil {
		t.Fatalf("resumed Submit(): %v", err)
	}

	_ = resumed.Close()
	if len(requests) != 3 {
		t.Fatalf("model requests after resume = %d, want 3", len(requests))
	}

	if got := resumedEvents.snapshot(); len(got) < 4 || got[3].Kind != "session.resumed" {
		t.Fatalf("resumed session event missing after conversation history: %+v", got)
	}
}

func TestBackendRequiresModel(t *testing.T) {
	if err := New(Config{}).Start(context.Background(), &captureBus{}); err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("Start() error = %v, want required model", err)
	}
}

func kinds(events []event.Event) []string {
	result := make([]string, len(events))
	for i, ev := range events {
		result[i] = ev.Kind
	}

	return result
}

type captureBus struct {
	mu     sync.Mutex
	events []event.Event
}

func (b *captureBus) Publish(_ context.Context, ev event.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, ev)
	return nil
}

func (*captureBus) Subscribe(context.Context) (<-chan event.Event, error) {
	return nil, fmt.Errorf("not implemented")
}

func (b *captureBus) snapshot() []event.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]event.Event(nil), b.events...)
}

var _ bus.Bus = (*captureBus)(nil)
