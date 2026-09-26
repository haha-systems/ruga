// Package tool defines provider-neutral tools and their execution registry.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxResultBytes = 16 * 1024

type ToolSchema struct {
	Description          string              `json:"description,omitempty"`
	Type                 string              `json:"type"`
	Properties           map[string]Property `json:"properties,omitempty"`
	Required             []string            `json:"required,omitempty"`
	AdditionalProperties bool                `json:"additionalProperties"`
}

type Property struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

type ToolResult struct {
	Content string
	Summary string
	IsError bool
}

type Tool interface {
	Name() string
	Schema() ToolSchema
	Execute(context.Context, json.RawMessage) ToolResult
}

// ReadOnly is an opt-in concurrency capability. Tools without it run
// sequentially with other calls so their ordering remains predictable.
type ReadOnly interface{ IsReadOnly() bool }

type Definition struct {
	Name   string
	Schema ToolSchema
}

type Call struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

type Execution struct {
	Call     Call
	Result   ToolResult
	Duration time.Duration
}

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry(tools ...Tool) (*Registry, error) {
	registry := &Registry{tools: make(map[string]Tool, len(tools))}
	for _, registered := range tools {
		if err := registry.Register(registered); err != nil {
			return nil, err
		}
	}

	return registry, nil
}

func toolError(format string, args ...any) ToolResult {
	return ToolResult{Content: fmt.Sprintf(format, args...), IsError: true}
}

func (r *Registry) Register(registered Tool) error {
	if registered == nil {
		return fmt.Errorf("tool is required")
	}

	name := strings.TrimSpace(registered.Name())
	if name == "" {
		return fmt.Errorf("tool name is required")
	}

	schema := registered.Schema()
	if schema.Type != "object" {
		return fmt.Errorf("tool %q schema type must be object", name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tools == nil {
		r.tools = make(map[string]Tool)
	}

	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool %q is already registered", name)
	}

	r.tools[name] = registered
	return nil
}

func (r *Registry) Definitions() []Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	definitions := make([]Definition, 0, len(r.tools))
	for name, registered := range r.tools {
		definitions = append(definitions, Definition{Name: name, Schema: registered.Schema()})
	}

	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	return definitions
}

// Execute runs a batch concurrently only when every resolved tool explicitly
// declares itself read-only. Results always match the original call order.
func (r *Registry) Execute(ctx context.Context, calls []Call) []Execution {
	results := make([]Execution, len(calls))
	registered := make([]Tool, len(calls))
	parallel := len(calls) > 1
	r.mu.RLock()
	for i, call := range calls {
		registered[i] = r.tools[call.Name]
		readOnly, ok := registered[i].(ReadOnly)
		if !ok || !readOnly.IsReadOnly() {
			parallel = false
		}
	}

	r.mu.RUnlock()
	for i, call := range calls {
		results[i].Call = call
		if registered[i] == nil {
			results[i].Result = ToolResult{Content: "unknown tool: " + call.Name, IsError: true}
		}
	}

	if parallel {
		var wait sync.WaitGroup
		for i := range calls {
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				results[index].Result, results[index].Duration = executeOne(ctx, registered[index], calls[index])
			}(i)
		}

		wait.Wait()
		return bound(results)
	}

	for i := range calls {
		if registered[i] != nil {
			results[i].Result, results[i].Duration = executeOne(ctx, registered[i], calls[i])
		}
	}

	return bound(results)
}

func executeOne(ctx context.Context, registered Tool, call Call) (ToolResult, time.Duration) {
	started := time.Now()
	result := executeSafely(ctx, registered, call)
	return result, time.Since(started)
}

func executeSafely(ctx context.Context, registered Tool, call Call) (result ToolResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = ToolResult{Content: fmt.Sprintf("tool %s failed: %v", call.Name, recovered), IsError: true}
		}
	}()
	if err := ctx.Err(); err != nil {
		return ToolResult{Content: "tool canceled: " + err.Error(), IsError: true}
	}

	return registered.Execute(ctx, call.Arguments)
}

func bound(executions []Execution) []Execution {
	for i := range executions {
		content := executions[i].Result.Content
		if len(content) <= maxResultBytes {
			continue
		}

		omitted := len(content) - maxResultBytes
		limit := 0
		suffix := ""
		for {
			suffix = fmt.Sprintf("\n… [%d bytes truncated]", omitted)
			limit = maxResultBytes - len(suffix)
			nextOmitted := len(content) - limit
			if nextOmitted == omitted {
				break
			}

			omitted = nextOmitted
		}

		content = content[:limit]
		for !utf8.ValidString(content) {
			content = content[:len(content)-1]
		}

		executions[i].Result.Content = content + suffix
	}

	return executions
}
