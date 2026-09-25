package event

import (
	"encoding/json"
	"time"
)

// Event is the application-owned record passed between the backend, bus, and UI.
type Event struct {
	ID        string           `json:"id"`
	Timestamp time.Time        `json:"timestamp"`
	Backend   string           `json:"backend"`
	Kind      string           `json:"kind"`
	ThreadID  string           `json:"thread_id,omitempty"`
	TurnID    string           `json:"turn_id,omitempty"`
	ItemID    string           `json:"item_id,omitempty"`
	Source    string           `json:"source,omitempty"`
	Summary   string           `json:"summary,omitempty"`
	Approval  *ApprovalRequest `json:"approval,omitempty"`
	Decision  ApprovalDecision `json:"approval_decision,omitempty"`
	Data      map[string]any   `json:"data,omitempty"`
	Raw       json.RawMessage  `json:"raw,omitempty"`
}

// ApprovalRequest contains application-owned details needed to make a
// provider action understandable and actionable in the UI.
type ApprovalRequest struct {
	RequestID   string   `json:"request_id"`
	Kind        string   `json:"kind"`
	ThreadID    string   `json:"thread_id,omitempty"`
	TurnID      string   `json:"turn_id,omitempty"`
	ItemID      string   `json:"item_id,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	Command     string   `json:"command,omitempty"`
	CWD         string   `json:"cwd,omitempty"`
	GrantRoot   string   `json:"grant_root,omitempty"`
	Tool        string   `json:"tool,omitempty"`
	Details     string   `json:"details,omitempty"`
	FilePaths   []string `json:"file_paths,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	Scope       string   `json:"scope,omitempty"`
}

type ApprovalDecision string

const (
	ApprovalAccept ApprovalDecision = "accept"
	ApprovalReject ApprovalDecision = "reject"
)
