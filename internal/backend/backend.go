package backend

import (
	"context"

	"github.com/haha-systems/ruga/internal/bus"
	"github.com/haha-systems/ruga/internal/event"
	"github.com/haha-systems/ruga/internal/session"
	"github.com/haha-systems/ruga/internal/tool"
)

// Backend starts a provider session and publishes normalized activity.
type Backend interface {
	Start(context.Context, bus.Bus) error
	Submit(context.Context, string) error
	Close() error
}

// Interactive exposes optional user-driven capabilities supported by a
// backend without making them mandatory for providers that lack them.
type Interactive interface {
	ResolveApproval(context.Context, string, event.ApprovalDecision) error
	Interrupt(context.Context) error
}

// ModelDisplay exposes the effective model for providers that can report it.
// It is optional so providers without model metadata remain usable.
type ModelDisplay interface {
	ModelName() string
}

// SessionSupport is optional because backend continuation state differs by
// provider. The callback is the application's durable session store.
type SessionSupport interface {
	ConfigureSession(session.Session, bool, func(session.Session) error)
}

// ToolSupport exposes Ruga's registered tools to backends that can execute
// provider tool calls.
type ToolSupport interface {
	ConfigureTools(*tool.Registry)
}
