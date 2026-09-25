package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"
	"github.com/haha-systems/ruga/internal/event"
)

// Watermill serializes publication on a worker. GoChannel waits for subscriber
// acknowledgements, so event order is retained while the backend reader only
// appends to this queue and never waits for terminal rendering.
type Watermill struct {
	pub    *gochannel.GoChannel
	mu     sync.Mutex
	cond   *sync.Cond
	queue  []event.Event
	closed bool
	done   chan struct{}
}

func New() *Watermill {
	pub := gochannel.NewGoChannel(gochannel.Config{
		OutputChannelBuffer:            256,
		BlockPublishUntilSubscriberAck: true,
	}, watermill.NewSlogLogger(slog.Default()))
	b := &Watermill{pub: pub, done: make(chan struct{})}
	b.cond = sync.NewCond(&b.mu)
	go b.dispatch()
	return b
}

func (b *Watermill) Publish(ctx context.Context, ev event.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return fmt.Errorf("event bus is closed")
	}
	b.queue = append(b.queue, ev)
	b.cond.Signal()
	return nil
}

func (b *Watermill) Subscribe(ctx context.Context) (<-chan event.Event, error) {
	messages, err := b.pub.Subscribe(ctx, Topic)
	if err != nil {
		return nil, err
	}
	events := make(chan event.Event, 256)
	go func() {
		defer close(events)
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-messages:
				if !ok {
					return
				}
				var ev event.Event
				if err := json.Unmarshal(msg.Payload, &ev); err != nil {
					slog.Error("decode application event", "error", err)
					msg.Ack()
					continue
				}
				select {
				case events <- ev:
					msg.Ack()
				case <-ctx.Done():
					msg.Ack()
					return
				}
			}
		}
	}()
	return events, nil
}

func (b *Watermill) Close() error {
	b.mu.Lock()
	b.closed = true
	b.cond.Broadcast()
	b.mu.Unlock()
	<-b.done
	return b.pub.Close()
}

func (b *Watermill) dispatch() {
	defer close(b.done)
	for {
		b.mu.Lock()
		for len(b.queue) == 0 && !b.closed {
			b.cond.Wait()
		}
		if len(b.queue) == 0 && b.closed {
			b.mu.Unlock()
			return
		}
		ev := b.queue[0]
		b.queue[0] = event.Event{}
		b.queue = b.queue[1:]
		b.mu.Unlock()

		payload, err := json.Marshal(ev)
		if err == nil {
			err = b.pub.Publish(Topic, message.NewMessage(ev.ID, payload))
		}
		if err != nil {
			slog.Error("publish application event", "error", err, "event_id", ev.ID)
		}
	}
}

var _ Bus = (*Watermill)(nil)
