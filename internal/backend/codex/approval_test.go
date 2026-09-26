package codex

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	codexgo "github.com/zealbase/codex-app-server-go"

	"github.com/haha-systems/ruga/internal/event"
)

type approvalTestBus struct {
	events chan event.Event
}

func (b *approvalTestBus) Publish(_ context.Context, ev event.Event) error {
	b.events <- ev
	return nil
}

func (*approvalTestBus) Subscribe(context.Context) (<-chan event.Event, error) {
	return nil, nil
}

func TestApprovalRequestsWaitForApplicationDecision(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		params     string
		decision   event.ApprovalDecision
		wantKind   string
		wantPart   string
		wantJSON   string
		wantThread bool
	}{
		{
			name:       "command accepted",
			method:     "item/commandExecution/requestApproval",
			params:     `{"itemId":"it-1","threadId":"th-1","turnId":"tu-1","command":"rm -i cache.tmp","cwd":"/repo","reason":"outside workspace","commandActions":[{"type":"delete","path":"cache.tmp"}]}`,
			decision:   event.ApprovalAccept,
			wantKind:   "command",
			wantPart:   "rm -i cache.tmp",
			wantJSON:   `{"decision":"accept"}`,
			wantThread: true,
		},
		{
			name: "command rejected", method: "item/commandExecution/requestApproval",
			params:   `{"command":"curl example.test"}`,
			decision: event.ApprovalReject, wantKind: "command", wantPart: "curl example.test",
			wantJSON: `{"decision":"decline"}`,
		},
		{
			name: "file change accepted", method: "item/fileChange/requestApproval",
			params:   `{"itemId":"it-2","filePaths":["a.go","b.go"],"grantRoot":"/repo"}`,
			decision: event.ApprovalAccept, wantKind: "file_change", wantPart: "a.go",
			wantJSON: `{"decision":"accept"}`,
		},
		{
			name: "permissions rejected", method: "item/permissions/requestApproval",
			params:   `{"permissions":["network"],"scope":"turn","reason":"network access"}`,
			decision: event.ApprovalReject, wantKind: "permissions", wantPart: "network",
			wantJSON: `{"scope":"turn"}`,
		},
		{
			name: "MCP tool accepted", method: "item/mcp/requestApproval",
			params:   `{"toolName":"search","serverName":"docs","input":{"query":"Go"}}`,
			decision: event.ApprovalAccept, wantKind: "mcp_tool", wantPart: "docs/search",
			wantJSON: `{"decision":"accept"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bus := &approvalTestBus{events: make(chan event.Event, 4)}
			backend := &Backend{eventBus: bus}
			handler := serverRequestHandler{backend: backend}
			response := make(chan struct {
				value codexgo.ServerResponse
				err   error
			}, 1)
			go func() {
				value, err := handler.HandleServerRequest(context.Background(), codexgo.ServerRequest{
					Method: tt.method, Params: json.RawMessage(tt.params),
				})
				response <- struct {
					value codexgo.ServerResponse
					err   error
				}{value, err}
			}()

			requested := receiveApprovalEvent(t, bus.events)
			if requested.Kind != "approval.requested" || requested.Approval == nil {
				t.Fatalf("request event = %+v", requested)
			}

			if requested.Approval.Kind != tt.wantKind || !containsApprovalDetail(*requested.Approval, tt.wantPart) {
				t.Fatalf("approval details = %+v", requested.Approval)
			}

			if tt.wantThread && requested.ThreadID != "th-1" {
				t.Fatalf("ThreadID = %q, want th-1", requested.ThreadID)
			}

			if err := backend.ResolveApproval(context.Background(), requested.Approval.RequestID, tt.decision); err != nil {
				t.Fatalf("ResolveApproval(): %v", err)
			}

			resolved := receiveApprovalEvent(t, bus.events)
			if resolved.Kind != "approval.resolved" || resolved.Decision != tt.decision || resolved.Approval.RequestID != requested.Approval.RequestID {
				t.Fatalf("resolution event = %+v", resolved)
			}

			select {
			case result := <-response:
				if result.err != nil {
					t.Fatalf("HandleServerRequest(): %v", result.err)
				}

				if string(result.value.Result) != tt.wantJSON {
					t.Fatalf("result = %s, want %s", result.value.Result, tt.wantJSON)
				}

			case <-time.After(time.Second):
				t.Fatal("server request did not finish after approval decision")
			}
		})
	}
}

func receiveApprovalEvent(t *testing.T, events <-chan event.Event) event.Event {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for approval event")
		return event.Event{}
	}
}

func containsApprovalDetail(request event.ApprovalRequest, value string) bool {
	if request.Command == value || request.Tool == value || request.Reason == value || request.GrantRoot == value || request.Scope == value {
		return true
	}

	for _, path := range request.FilePaths {
		if path == value {
			return true
		}
	}

	for _, permission := range request.Permissions {
		if permission == value {
			return true
		}
	}

	return false
}

type interruptTestTransport struct {
	mu      sync.Mutex
	calls   []string
	params  map[string]json.RawMessage
	handler codexgo.RequestHandler
}

func (t *interruptTestTransport) Call(_ context.Context, method string, params, _ any) error {
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}

	t.mu.Lock()
	t.calls = append(t.calls, method)
	if t.params == nil {
		t.params = make(map[string]json.RawMessage)
	}

	t.params[method] = data
	t.mu.Unlock()
	return nil
}

func (*interruptTestTransport) Notify(context.Context, string, any) error { return nil }
func (t *interruptTestTransport) SetRequestHandler(handler codexgo.RequestHandler) {
	t.handler = handler
}
func (*interruptTestTransport) Close() error { return nil }

func TestInterruptTargetsActiveTurn(t *testing.T) {
	transport := &interruptTestTransport{}
	client, err := codexgo.New(codexgo.WithTransport(transport))
	if err != nil {
		t.Fatalf("codexgo.New(): %v", err)
	}
	defer client.Close()
	bus := &approvalTestBus{events: make(chan event.Event, 4)}
	pending := make(chan event.ApprovalDecision, 1)
	backend := &Backend{
		client: client, threadID: "thread-1", turnID: "turn-2", busy: true,
		eventBus: bus, pendingApprovals: map[string]chan event.ApprovalDecision{"approval-1": pending},
	}
	if err := backend.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt(): %v", err)
	}

	transport.mu.Lock()
	params := append(json.RawMessage(nil), transport.params["turn/interrupt"]...)
	transport.mu.Unlock()
	var request struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		t.Fatal(err)
	}

	if request.ThreadID != "thread-1" || request.TurnID != "turn-2" {
		t.Fatalf("interrupt target = %+v", request)
	}

	select {
	case decision := <-pending:
		if decision != event.ApprovalReject {
			t.Fatalf("pending approval decision = %q, want reject", decision)
		}

	default:
		t.Fatal("interrupt did not reject a pending approval")
	}

	if resolved := receiveApprovalEvent(t, bus.events); resolved.Kind != "approval.resolved" || resolved.Decision != event.ApprovalReject {
		t.Fatalf("interrupt resolution event = %+v", resolved)
	}
}

func TestCanceledApprovalIsRemovedAndResolved(t *testing.T) {
	bus := &approvalTestBus{events: make(chan event.Event, 4)}
	backend := &Backend{eventBus: bus}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := backend.requestApproval(ctx, event.ApprovalRequest{Kind: "command", Command: "pwd"})
		result <- err
	}()
	requested := receiveApprovalEvent(t, bus.events)
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("canceled approval returned no error")
		}

	case <-time.After(time.Second):
		t.Fatal("canceled approval handler did not return")
	}

	resolved := receiveApprovalEvent(t, bus.events)
	if resolved.Kind != "approval.resolved" || resolved.Decision != event.ApprovalReject || resolved.Approval.RequestID != requested.Approval.RequestID {
		t.Fatalf("cancellation resolution event = %+v", resolved)
	}

	backend.mu.Lock()
	pending := len(backend.pendingApprovals)
	backend.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending approvals after cancellation = %d", pending)
	}
}
