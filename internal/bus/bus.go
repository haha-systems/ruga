package bus

import (
	"context"

	"github.com/haha-systems/ruga/internal/event"
)

const Topic = "application.events"

// Bus is the small application-facing surface of the Watermill event bus.
type Bus interface {
	Publish(context.Context, event.Event) error
	Subscribe(context.Context) (<-chan event.Event, error)
}
