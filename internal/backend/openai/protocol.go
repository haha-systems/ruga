package openai

import "github.com/haha-systems/ruga/internal/tool"

// These types describe the OpenAI-compatible Chat Completions wire format.
// They stay inside this provider package and never cross the backend boundary.
type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
}

type completionRequest struct {
	Model             string           `json:"model"`
	Messages          []message        `json:"messages"`
	Stream            bool             `json:"stream"`
	Tools             []toolDefinition `json:"tools,omitempty"`
	ParallelToolCalls *bool            `json:"parallel_tool_calls,omitempty"`
}

type toolCall struct {
	ID           string           `json:"id"`
	Type         string           `json:"type"`
	FunctionCall toolFunctionCall `json:"function"`
}

type toolFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolDefinition struct {
	Type     string             `json:"type"`
	Function toolFunctionSchema `json:"function"`
}

type toolFunctionSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  tool.ToolSchema `json:"parameters"`
}

type completionChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content   string          `json:"content"`
			ToolCalls []toolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type toolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
