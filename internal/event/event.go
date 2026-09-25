package event

import (
	"encoding/json"
	"time"
)

// Event is the application-owned record passed between the backend, bus, and UI.
type Event struct {
	ID        string          `json:"id"`
	Timestamp time.Time       `json:"timestamp"`
	Backend   string          `json:"backend"`
	Kind      string          `json:"kind"`
	ThreadID  string          `json:"thread_id,omitempty"`
	TurnID    string          `json:"turn_id,omitempty"`
	ItemID    string          `json:"item_id,omitempty"`
	Source    string          `json:"source,omitempty"`
	Summary   string          `json:"summary,omitempty"`
	Data      map[string]any  `json:"data,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}
