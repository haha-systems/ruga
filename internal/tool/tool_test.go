package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type parallelTestTool struct {
	name    string
	started chan string
	release <-chan struct{}
}

func (t parallelTestTool) Name() string { return t.name }
func (parallelTestTool) Schema() ToolSchema {
	return ToolSchema{Type: "object", AdditionalProperties: false}
}
func (parallelTestTool) IsReadOnly() bool { return true }
func (t parallelTestTool) Execute(context.Context, json.RawMessage) ToolResult {
	t.started <- t.name
	<-t.release
	return ToolResult{Content: t.name}
}

type orderedTestTool struct {
	name    string
	started chan string
	first   bool
	release <-chan struct{}
}

func (t orderedTestTool) Name() string { return t.name }
func (orderedTestTool) Schema() ToolSchema {
	return ToolSchema{Type: "object", AdditionalProperties: false}
}

func (t orderedTestTool) Execute(context.Context, json.RawMessage) ToolResult {
	t.started <- t.name
	if t.first {
		<-t.release
	}

	return ToolResult{Content: t.name}
}

func TestRegistryExecutesReadOnlyBatchConcurrentlyAndPreservesOrder(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	registry, err := NewRegistry(
		parallelTestTool{name: "first", started: started, release: release},
		parallelTestTool{name: "second", started: started, release: release},
	)
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}

	done := make(chan []Execution, 1)
	go func() {
		done <- registry.Execute(context.Background(), []Call{{ID: "1", Name: "first"}, {ID: "2", Name: "second"}})
	}()
	seen := map[string]bool{}
	for range 2 {
		select {
		case name := <-started:
			seen[name] = true
		case <-time.After(time.Second):
			t.Fatal("read-only calls did not start concurrently")
		}
	}

	close(release)
	results := <-done
	if len(results) != 2 || results[0].Call.ID != "1" || results[0].Result.Content != "first" || results[1].Result.Content != "second" {
		t.Fatalf("results did not preserve call order: %+v", results)
	}
}

func TestRegistrySerializesToolsWithoutReadOnlyCapability(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	registry, err := NewRegistry(
		orderedTestTool{name: "first", started: started, first: true, release: release},
		orderedTestTool{name: "second", started: started, release: release},
	)
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}

	done := make(chan []Execution, 1)
	go func() {
		done <- registry.Execute(context.Background(), []Call{{Name: "first"}, {Name: "second"}})
	}()
	if got := <-started; got != "first" {
		t.Fatalf("first started tool = %q", got)
	}

	select {
	case got := <-started:
		t.Fatalf("second tool %q started before first completed", got)
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	if got := <-started; got != "second" {
		t.Fatalf("second started tool = %q", got)
	}

	if results := <-done; len(results) != 2 || results[1].Call.Name != "second" {
		t.Fatalf("serial results = %+v", results)
	}
}

func TestRegistryBoundsResultsAndReportsUnknownTools(t *testing.T) {
	registry, err := NewRegistry(Echo{})
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}

	large := strings.Repeat("x", maxResultBytes+20)
	longRegistry, err := NewRegistry(fixedResultTool{content: large})
	if err != nil {
		t.Fatalf("NewRegistry(fixed result): %v", err)
	}

	results := longRegistry.Execute(context.Background(), []Call{{Name: "fixed"}})
	if len(results[0].Result.Content) > maxResultBytes || !strings.Contains(results[0].Result.Content, "bytes truncated]") {
		t.Fatalf("bounded result = %d bytes, %q", len(results[0].Result.Content), results[0].Result.Content[len(results[0].Result.Content)-20:])
	}

	unknown := registry.Execute(context.Background(), []Call{{ID: "missing-id", Name: "missing"}})
	if len(unknown) != 1 || !unknown[0].Result.IsError || unknown[0].Call.ID != "missing-id" {
		t.Fatalf("unknown tool result = %+v", unknown)
	}
}

type blockingMutationTool struct {
	name    string
	started chan string
	release <-chan struct{}
}

func (t blockingMutationTool) Name() string { return t.name }
func (blockingMutationTool) Schema() ToolSchema {
	return ToolSchema{Type: "object", AdditionalProperties: false}
}

func (t blockingMutationTool) Execute(context.Context, json.RawMessage) ToolResult {
	t.started <- t.name
	<-t.release
	return ToolResult{Content: t.name}
}

func TestRegistrySegmentsMixedBatchesAroundMutations(t *testing.T) {
	started := make(chan string, 4)
	mutationRelease := make(chan struct{})
	readRelease := make(chan struct{})
	registry, err := NewRegistry(
		blockingMutationTool{name: "write", started: started, release: mutationRelease},
		parallelTestTool{name: "read-a", started: started, release: readRelease},
		parallelTestTool{name: "read-b", started: started, release: readRelease},
	)
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}

	done := make(chan []Execution, 1)
	go func() {
		done <- registry.Execute(context.Background(), []Call{
			{ID: "1", Name: "write"},
			{ID: "2", Name: "read-a"},
			{ID: "3", Name: "read-b"},
		})
	}()

	if got := <-started; got != "write" {
		t.Fatalf("first started tool = %q, want the mutation", got)
	}

	// The reads must not overtake the mutation ahead of them.
	select {
	case got := <-started:
		t.Fatalf("read %q started before the mutation completed", got)
	case <-time.After(20 * time.Millisecond):
	}

	close(mutationRelease)

	// Both trailing reads may now start concurrently.
	seen := map[string]bool{}
	for range 2 {
		select {
		case name := <-started:
			seen[name] = true
		case <-time.After(time.Second):
			t.Fatalf("trailing reads did not start concurrently: %v", seen)
		}
	}

	close(readRelease)
	results := <-done
	if len(results) != 3 || results[0].Call.Name != "write" || results[1].Result.Content != "read-a" || results[2].Result.Content != "read-b" {
		t.Fatalf("segmented results = %+v", results)
	}
}

type fixedResultTool struct{ content string }

func (fixedResultTool) Name() string { return "fixed" }
func (fixedResultTool) Schema() ToolSchema {
	return ToolSchema{Type: "object", AdditionalProperties: false}
}

func (t fixedResultTool) Execute(context.Context, json.RawMessage) ToolResult {
	return ToolResult{Content: t.content}
}
