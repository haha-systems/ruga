package backend

import (
	"context"

	"github.com/haha-systems/ruga/internal/bus"
	"github.com/haha-systems/ruga/internal/event"
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
