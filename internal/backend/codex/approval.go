package codex

import (
	"context"
	"encoding/json"
	"fmt"

	codexgo "github.com/zealbase/codex-app-server-go"

	"github.com/haha-systems/ruga/internal/event"
)

type serverRequestHandler struct {
	backend *Backend
}

type commandExecutionApprovalRequest struct {
	ItemID                 string          `json:"itemId,omitempty"`
	ThreadID               string          `json:"threadId,omitempty"`
	TurnID                 string          `json:"turnId,omitempty"`
	Reason                 string          `json:"reason,omitempty"`
	Command                string          `json:"command,omitempty"`
	CWD                    string          `json:"cwd,omitempty"`
	CommandActions         json.RawMessage `json:"commandActions,omitempty"`
	NetworkApprovalContext json.RawMessage `json:"networkApprovalContext,omitempty"`
}

func (h serverRequestHandler) HandleServerRequest(ctx context.Context, request codexgo.ServerRequest) (codexgo.ServerResponse, error) {
	switch request.Method {
	case "item/commandExecution/requestApproval":
		var input commandExecutionApprovalRequest
		if err := json.Unmarshal(request.Params, &input); err != nil {
			return codexgo.ServerResponse{}, err
		}

		decision, err := h.backend.requestApproval(ctx, event.ApprovalRequest{
			Kind: "command", ThreadID: input.ThreadID, TurnID: input.TurnID, ItemID: input.ItemID,
			Reason: input.Reason, Command: input.Command, CWD: input.CWD,
			Details: compactJSON(input.CommandActions, 240),
		})
		if err != nil {
			return codexgo.ServerResponse{}, err
		}

		return response(codexgo.CommandExecutionApprovalResult{Decision: codexDecision(decision)})

	case "item/fileChange/requestApproval":
		var input codexgo.FileChangeApprovalRequest
		if err := json.Unmarshal(request.Params, &input); err != nil {
			return codexgo.ServerResponse{}, err
		}

		decision, err := h.backend.requestApproval(ctx, event.ApprovalRequest{
			Kind: "file_change", ThreadID: input.ThreadID, TurnID: input.TurnID, ItemID: input.ItemID,
			Reason: input.Reason, GrantRoot: input.GrantRoot, FilePaths: input.FilePaths,
		})
		if err != nil {
			return codexgo.ServerResponse{}, err
		}

		if decision == event.ApprovalAccept {
			return response(codexgo.FileChangeApprovalResult{Decision: codexgo.FileChangeApprovalDecisionAccept})
		}

		return response(codexgo.FileChangeApprovalResult{Decision: codexgo.FileChangeApprovalDecisionDecline})

	case "item/permissions/requestApproval":
		var input codexgo.PermissionsApprovalRequest
		if err := json.Unmarshal(request.Params, &input); err != nil {
			return codexgo.ServerResponse{}, err
		}

		decision, err := h.backend.requestApproval(ctx, event.ApprovalRequest{
			Kind: "permissions", ThreadID: input.ThreadID, TurnID: input.TurnID, ItemID: input.ItemID,
			Reason: input.Reason, Permissions: input.Permissions, Scope: string(input.Scope),
		})
		if err != nil {
			return codexgo.ServerResponse{}, err
		}

		permissions := []string{}
		if decision == event.ApprovalAccept {
			permissions = input.Permissions
		}

		return response(codexgo.PermissionsApprovalResult{Permissions: permissions, Scope: input.Scope})

	case "item/mcp/requestApproval":
		var input codexgo.MCPToolCallApprovalRequest
		if err := json.Unmarshal(request.Params, &input); err != nil {
			return codexgo.ServerResponse{}, err
		}

		tool := input.ToolName
		if input.ServerName != "" {
			tool = input.ServerName + "/" + tool
		}

		decision, err := h.backend.requestApproval(ctx, event.ApprovalRequest{
			Kind: "mcp_tool", ThreadID: input.ThreadID, TurnID: input.TurnID,
			Tool: tool, Details: compactJSON(input.Input, 240),
		})
		if err != nil {
			return codexgo.ServerResponse{}, err
		}

		return response(codexgo.MCPToolCallApprovalResponse{Decision: string(decision)})

	default:
		return codexgo.ServerResponse{}, fmt.Errorf("unsupported Codex server request %q", request.Method)
	}
}

func codexDecision(decision event.ApprovalDecision) codexgo.ApprovalDecision {
	if decision == event.ApprovalAccept {
		return codexgo.ApprovalDecisionAccept
	}

	return codexgo.ApprovalDecisionDecline
}

func response(value any) (codexgo.ServerResponse, error) {
	result, err := json.Marshal(value)
	if err != nil {
		return codexgo.ServerResponse{}, err
	}

	return codexgo.ServerResponse{Result: result}, nil
}
