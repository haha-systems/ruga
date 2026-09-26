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
