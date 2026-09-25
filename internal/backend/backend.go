package backend

import (
	"context"

	"github.com/xiy/ruga/internal/bus"
)

// Backend starts a provider session and publishes normalized activity.
type Backend interface {
	Start(context.Context, bus.Bus) error
	Submit(context.Context, string) error
	Close() error
}
