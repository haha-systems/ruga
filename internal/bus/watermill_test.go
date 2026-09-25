package bus

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/xiy/ruga/internal/event"
)

func TestWatermillPreservesOrderWithoutBlockingPublisher(t *testing.T) {
	b := New()
	t.Cleanup(func() { _ = b.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, err := b.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	const count = 300
	for i := range count {
		ev := event.Event{ID: fmt.Sprint(i), Kind: "test", Summary: fmt.Sprint(i)}
		if err := b.Publish(ctx, ev); err != nil {
			t.Fatalf("Publish(%d): %v", i, err)
		}
	}

	for i := range count {
		select {
		case ev := <-events:
			if ev.ID != fmt.Sprint(i) {
				t.Fatalf("event %d has ID %q", i, ev.ID)
			}
		case <-ctx.Done():
			t.Fatalf("waiting for event %d: %v", i, ctx.Err())
		}
	}
}
