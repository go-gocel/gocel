// Package tool_test tests the MCP source functionality using a self-contained
// test binary pattern: TestMain re-executes this binary as a subprocess when
// MCP_MOCK_MODE=1 is set, acting as an MCP server over stdio.
package tool_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/tool"
)

// TestMain is the entry point. When MCP_MOCK_MODE=1, run the mock MCP server
// as a subprocess; otherwise run tests normally.
func TestMain(m *testing.M) {
	if os.Getenv("MCP_MOCK_MODE") == "1" {
		runMCPMock()
		return
	}
	m.Run()
}

// mcpTestTimeout bounds each MCP round-trip in tests. The deadline exists to
// catch hangs, not to measure performance: spawning the mock subprocess and
// initializing it can take several seconds under parallel full-suite load,
// so a 5s budget flakes (backlog D6). 30s still catches real hangs.
const mcpTestTimeout = 30 * time.Second

// ── Self-contained MCP mock ────────────────────────────────────────────────

// runMCPMock reads JSON-RPC requests from stdin and writes responses to stdout.
func runMCPMock() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Split(bufio.ScanLines)

	writeResponse := func(id any, result any, errCode int, errMsg string) {
		resp := map[string]any{"jsonrpc": "2.0", "id": id}
		if errCode != 0 {
			resp["error"] = map[string]any{"code": errCode, "message": errMsg}
		} else {
			resp["result"] = result
		}
		data, _ := json.Marshal(resp)
		fmt.Fprintln(os.Stdout, string(data))
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var req struct {
			JsonRPC string          `json:"jsonrpc"`
			ID      any             `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}

		switch req.Method {
		case "initialize":
			writeResponse(req.ID, map[string]any{
				"protocolVersion": "2025-03-26",
				"capabilities":    map[string]any{"tools": struct{}{}},
				"serverInfo":      map[string]any{"name": "mcp_mock", "version": "1.0"},
			}, 0, "")

		case "notifications/initialized":
			// Notification — no response expected

		case "tools/list":
			writeResponse(req.ID, map[string]any{
				"tools": []map[string]any{
					{
						"name":        "mcp_echo",
						"description": "Echo back a message",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"message": map[string]any{"type": "string"},
							},
							"required": []string{"message"},
						},
					},
					{
						"name":        "add",
						"description": "Add two numbers",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"a": map[string]any{"type": "number"},
								"b": map[string]any{"type": "number"},
							},
							"required": []string{"a", "b"},
						},
					},
				},
			}, 0, "")

		case "tools/call":
			var params struct {
				Name string          `json:"name"`
				Args json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeResponse(req.ID, nil, -32602, "Invalid params")
				continue
			}

			switch params.Name {
			case "mcp_echo":
				var args struct {
					Message string `json:"message"`
				}
				json.Unmarshal(params.Args, &args)
				writeResponse(req.ID, map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": "Echo: " + args.Message},
					},
				}, 0, "")

			case "add":
				var args struct {
					A float64 `json:"a"`
					B float64 `json:"b"`
				}
				json.Unmarshal(params.Args, &args)
				result := fmt.Sprintf("%v", args.A+args.B)
				writeResponse(req.ID, map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": result},
					},
				}, 0, "")

			default:
				writeResponse(req.ID, nil, -32602, fmt.Sprintf("Unknown tool: %s", params.Name))
			}

		default:
			writeResponse(req.ID, nil, -32601, fmt.Sprintf("Method not found: %s", req.Method))
		}
	}
}

// buildMCPSource compiles this test binary and returns its path.
// The binary is invoked with MCP_MOCK_MODE=1 to act as an MCP server.
func buildMCPSource(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "mcp_mock_test.exe")

	cmd := exec.Command("go", "test", "-c", "-o", outPath,
		"github.com/go-gocel/gocel/core/tool")
	cmd.Dir = filepath.Join("..", "..") // module root
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("build mcp mock binary: %v", err)
	}
	return outPath
}

// ── Types & Content formatting ─────────────────────────────────────────────

func TestMCPContent_FormatText(t *testing.T) {
	content := []tool.MCPContent{
		{Type: "text", Text: "hello world"},
		{Type: "text", Text: "second line"},
	}
	if len(content) != 2 {
		t.Fatalf("expected 2 content items")
	}
	if content[0].Type != "text" || content[0].Text != "hello world" {
		t.Errorf("first item: type=%q text=%q", content[0].Type, content[0].Text)
	}
}

func TestMCPContent_FormatImage(t *testing.T) {
	content := tool.MCPContent{
		Type:     "image",
		Data:     "base64data",
		MIMEType: "image/png",
	}
	if content.Type != "image" {
		t.Errorf("type = %q, want %q", content.Type, "image")
	}
	if content.MIMEType != "image/png" {
		t.Errorf("mimeType = %q, want %q", content.MIMEType, "image/png")
	}
	if content.Data != "base64data" {
		t.Errorf("data = %q", content.Data)
	}
}

func TestMCPContent_FormatAudio(t *testing.T) {
	content := tool.MCPContent{
		Type:     "audio",
		Data:     "base64audio",
		MIMEType: "audio/mpeg",
	}
	if content.Type != "audio" {
		t.Errorf("type = %q", content.Type)
	}
}

func TestMCPContent_FormatResource(t *testing.T) {
	content := tool.MCPContent{
		Type: "resource",
		Resource: &tool.MCPResource{
			URI:  "file:///tmp/test.txt",
			Text: "file contents",
		},
	}
	if content.Type != "resource" {
		t.Errorf("type = %q", content.Type)
	}
	if content.Resource == nil {
		t.Fatal("Resource is nil")
	}
	if content.Resource.URI != "file:///tmp/test.txt" {
		t.Errorf("URI = %q", content.Resource.URI)
	}
}

func TestMCPRequest_ID_Type(t *testing.T) {
	req := &tool.MCPRequest{
		JsonRPC: "2.0",
		ID:      "gocel_1",
		Method:  "ping",
	}
	data, err := tool.MarshalMCPRequest(req)
	if err != nil {
		t.Fatalf("MarshalMCPRequest: %v", err)
	}
	if !strings.Contains(string(data), `"gocel_1"`) {
		t.Errorf("ID should be string, got: %s", string(data))
	}
}

func TestMCPResponse_Parse(t *testing.T) {
	resp, err := tool.UnmarshalMCPResponse([]byte(`{"jsonrpc":"2.0","id":"gocel_1","result":{"tools":[]}}`))
	if err != nil {
		t.Fatalf("UnmarshalMCPResponse: %v", err)
	}
	if resp.ID != "gocel_1" {
		t.Errorf("ID = %v, want %v", resp.ID, "gocel_1")
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %v", resp.Error)
	}
}

func TestMCPResponse_ParseError(t *testing.T) {
	_, err := tool.UnmarshalMCPResponse([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"not found"}}`))
	if err != nil {
		t.Fatalf("UnmarshalMCPResponse: %v", err)
	}
}

func TestMCPContent_BackwardCompatAlias(t *testing.T) {
	var item tool.MCPContentItem
	item = tool.MCPContent{Type: "text", Text: "test"}
	if item.Text != "test" {
		t.Error("MCPContentItem alias should work")
	}
}

// ── MCPSource integration (via self-contained mock binary) ─────────────────

func TestMCPSource_InitializeAndListTools(t *testing.T) {
	mockPath := buildMCPSource(t)

	src := tool.NewMCPSource("test", mockPath, map[string]string{"MCP_MOCK_MODE": "1"})
	defer src.Close()

	ctx, cancel := context.WithTimeout(context.Background(), mcpTestTimeout)
	defer cancel()

	tools, err := src.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	if tools[0].Name() != "mcp_echo" {
		t.Errorf("tool[0].Name() = %q, want %q", tools[0].Name(), "mcp_echo")
	}
	if tools[0].Description() == "" {
		t.Error("tool[0].Description() is empty")
	}
	if tools[1].Name() != "add" {
		t.Errorf("tool[1].Name() = %q, want %q", tools[1].Name(), "add")
	}
}

func TestMCPSource_CallTool(t *testing.T) {
	mockPath := buildMCPSource(t)

	src := tool.NewMCPSource("test", mockPath, map[string]string{"MCP_MOCK_MODE": "1"})
	defer src.Close()

	ctx, cancel := context.WithTimeout(context.Background(), mcpTestTimeout)
	defer cancel()

	_, err := src.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	result, err := src.CallTool(ctx, "mcp_echo", map[string]any{"message": "hello"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result != "Echo: hello" {
		t.Errorf("result = %q, want %q", result, "Echo: hello")
	}
}

func TestMCPSource_CallToolAdd(t *testing.T) {
	mockPath := buildMCPSource(t)

	src := tool.NewMCPSource("test", mockPath, map[string]string{"MCP_MOCK_MODE": "1"})
	defer src.Close()

	ctx, cancel := context.WithTimeout(context.Background(), mcpTestTimeout)
	defer cancel()

	_, err := src.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	result, err := src.CallTool(ctx, "add", map[string]any{"a": 3.0, "b": 4.0})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result != "7" {
		t.Errorf("result = %q, want %q", result, "7")
	}
}

func TestMCPSource_CallToolNotFound(t *testing.T) {
	mockPath := buildMCPSource(t)

	src := tool.NewMCPSource("test", mockPath, map[string]string{"MCP_MOCK_MODE": "1"})
	defer src.Close()

	ctx, cancel := context.WithTimeout(context.Background(), mcpTestTimeout)
	defer cancel()

	_, err := src.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	_, err = src.CallTool(ctx, "nonexistent_tool", map[string]any{})
	if err == nil {
		t.Fatal("expected error for nonexistent tool")
	}
}

func TestMCPSource_ListToolsCached(t *testing.T) {
	mockPath := buildMCPSource(t)

	src := tool.NewMCPSource("test", mockPath, map[string]string{"MCP_MOCK_MODE": "1"})
	defer src.Close()

	ctx, cancel := context.WithTimeout(context.Background(), mcpTestTimeout)
	defer cancel()

	// First call starts the process
	tools1, err := src.ListTools(ctx)
	if err != nil {
		t.Fatalf("first ListTools: %v", err)
	}

	// Second call should return cached data without re-initializing
	tools2, err := src.ListTools(ctx)
	if err != nil {
		t.Fatalf("second ListTools: %v", err)
	}

	if len(tools1) != len(tools2) {
		t.Errorf("cached list length changed: %d vs %d", len(tools1), len(tools2))
	}
}

func TestMCPSource_Concurrency(t *testing.T) {
	mockPath := buildMCPSource(t)

	src := tool.NewMCPSource("test", mockPath, map[string]string{"MCP_MOCK_MODE": "1"})
	defer src.Close()

	ctx, cancel := context.WithTimeout(context.Background(), mcpTestTimeout)
	defer cancel()

	_, err := src.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	done := make(chan bool, 3)
	for i := 0; i < 3; i++ {
		go func() {
			_, err := src.CallTool(ctx, "mcp_echo", map[string]any{"message": "concurrent"})
			if err != nil {
				t.Errorf("concurrent CallTool: %v", err)
			}
			done <- true
		}()
	}

	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for concurrent calls")
		}
	}
}

func TestMCPSource_CloseIdempotent(t *testing.T) {
	mockPath := buildMCPSource(t)

	src := tool.NewMCPSource("test", mockPath, map[string]string{"MCP_MOCK_MODE": "1"})

	err1 := src.Close()
	err2 := src.Close()
	if err2 != nil {
		t.Errorf("second close: %v", err2)
	}
	_ = err1
}

func TestMCPSource_StateError(t *testing.T) {
	mockPath := buildMCPSource(t)

	src := tool.NewMCPSource("test", mockPath, map[string]string{"MCP_MOCK_MODE": "1"})
	defer src.Close()

	ctx, cancel := context.WithTimeout(context.Background(), mcpTestTimeout)
	defer cancel()

	// Close before list — state should error
	src.Close()

	_, err := src.ListTools(ctx)
	if err == nil {
		t.Error("expected error ListTools on closed source")
	}
}

func TestMCPTool_Run(t *testing.T) {
	mockPath := buildMCPSource(t)

	src := tool.NewMCPSource("test", mockPath, map[string]string{"MCP_MOCK_MODE": "1"})
	defer src.Close()

	ctx, cancel := context.WithTimeout(context.Background(), mcpTestTimeout)
	defer cancel()

	tools, err := src.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	if len(tools) < 2 {
		t.Fatalf("expected at least 2 tools, got %d", len(tools))
	}

	echoTool := tools[0]
	result, err := echoTool.Run(ctx, `{"message":"test run"}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result != "Echo: test run" {
		t.Errorf("Run result = %q, want %q", result, "Echo: test run")
	}
}

func TestMCPTool_RunAdd(t *testing.T) {
	mockPath := buildMCPSource(t)

	src := tool.NewMCPSource("test", mockPath, map[string]string{"MCP_MOCK_MODE": "1"})
	defer src.Close()

	ctx, cancel := context.WithTimeout(context.Background(), mcpTestTimeout)
	defer cancel()

	tools, err := src.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	addTool := tools[1]
	result, err := addTool.Run(ctx, `{"a":10,"b":20}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result != "30" {
		t.Errorf("Run result = %q, want %q", result, "30")
	}
}

// ── Tool Info conversion ───────────────────────────────────────────────────

func TestMCPTool_Info(t *testing.T) {
	mockPath := buildMCPSource(t)

	src := tool.NewMCPSource("test", mockPath, map[string]string{"MCP_MOCK_MODE": "1"})
	defer src.Close()

	ctx, cancel := context.WithTimeout(context.Background(), mcpTestTimeout)
	defer cancel()

	tools, err := src.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	info := kernel.ToolFromInfo(tools[0])
	if info.Name != "mcp_echo" {
		t.Errorf("info.Name = %q", info.Name)
	}
	if info.Kind != kernel.ToolKindMCP {
		t.Errorf("info.Kind = %v, want MCP kind", info.Kind)
	}
}

func TestMCPTool_ToolMeta(t *testing.T) {
	mockPath := buildMCPSource(t)

	src := tool.NewMCPSource("test", mockPath, map[string]string{"MCP_MOCK_MODE": "1"})
	defer src.Close()

	ctx, cancel := context.WithTimeout(context.Background(), mcpTestTimeout)
	defer cancel()

	tools, err := src.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	meta := tools[0].ToolMeta()
	if meta.Kind != kernel.ToolKindMCP {
		t.Errorf("ToolMeta.Kind = %v, want MCP", meta.Kind)
	}
}

// TestMCPSource_CloseRace reproduces the race condition where Close()
// sets s.stdout = nil concurrently with readResponse() accessing it.
// Before the fix, this would panic with nil pointer dereference.
func TestMCPSource_CloseRace(t *testing.T) {
	mockPath := buildMCPSource(t)

	src := tool.NewMCPSource("test", mockPath, map[string]string{"MCP_MOCK_MODE": "1"})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := src.ListTools(ctx)
	if err != nil {
		src.Close()
		t.Fatalf("ListTools: %v", err)
	}

	// Fire concurrent Close + CallTool to trigger the race.
	// Close() sets s.stdout = nil; CallTool() calls readResponse() which reads s.stdout.
	var wg sync.WaitGroup
	errCh := make(chan error, 10)

	// Goroutine 1: repeatedly close and reopen
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			src.Close()
			time.Sleep(10 * time.Millisecond)
		}
	}()

	// Goroutine 2-4: try to call tools during the chaos
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				_, err := src.CallTool(ctx, "mcp_echo", map[string]any{"message": "race"})
				if err != nil && !strings.Contains(err.Error(), "closed") &&
					!strings.Contains(err.Error(), "in state") {
					errCh <- err
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Logf("unexpected error during race: %v", err)
	}

	// Final cleanup
	src.Close()
}
