package tool

import (
	"context"
	"encoding/json"
)

// Echo is a small read-only tool used to exercise the tool loop.
type Echo struct{}

func (Echo) Name() string { return "echo" }

func (Echo) Schema() ToolSchema {
	return ToolSchema{
		Description:          "Return the supplied text unchanged.",
		Type:                 "object",
		Properties:           map[string]Property{"text": {Type: "string", Description: "Text to return."}},
		Required:             []string{"text"},
		AdditionalProperties: false,
	}
}

func (Echo) IsReadOnly() bool { return true }

func (Echo) Execute(_ context.Context, arguments json.RawMessage) ToolResult {
	var input struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return ToolResult{Content: "invalid echo arguments: " + err.Error(), IsError: true}
	}

	return ToolResult{Content: input.Text}
}
