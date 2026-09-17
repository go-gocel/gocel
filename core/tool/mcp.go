package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-gocel/gocel/core/kernel"
)

const mcpProtocolVersion = "2025-03-26"

// ── State machine ──────────────────────────────────────────────────────────

type mcpConnState int

const (
	mcpStateNew mcpConnState = iota
	mcpStateInitializing
	mcpStateReady
	mcpStateClosed
	// mcpStateBroken: the subprocess died or the pipe failed — the next
	// ensureRunning restarts it (the old code bound the process to the
	// first caller's ctx via exec.CommandContext and never recovered).
	mcpStateBroken
)

// ── Core protocol types ────────────────────────────────────────────────────

// ── Response types ────────────────────────────────────────────────────────

// MCPRequest is a JSON-RPC request in the MCP protocol.
//
// MCPRequest 是 MCP 协议中的 JSON-RPC 请求结构体。
type MCPRequest struct {
	JsonRPC string      `json:"jsonrpc"`
	ID      any         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// MCPResponse is a JSON-RPC response in the MCP protocol.
//
// MCPResponse 是 MCP 协议中的 JSON-RPC 响应结构体。
type MCPResponse struct {
	JsonRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *MCPError       `json:"error,omitempty"`
}

// MCPError 是 MCP 协议的错误结构体，实现了 error 接口。
//
// MCPError represents a protocol-level error in the MCP specification.
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error returns the formatted MCP error string.
// Error 返回格式化的 MCP 错误字符串。
func (e *MCPError) Error() string {
	return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message)
}

// ── Initialize types ───────────────────────────────────────────────────────

// MCPInitializeParams is the parameter payload of the MCP initialize
// request.
// MCPInitializeParams 是 MCP initialize 请求的参数载荷。
type MCPInitializeParams struct {
	ProtocolVersion string                `json:"protocolVersion"`
	Capabilities    MCPClientCapabilities `json:"capabilities"`
	ClientInfo      MCPImplementation     `json:"clientInfo"`
}

// MCPClientCapabilities 声明客户端支持的 MCP 能力。
//
// MCPClientCapabilities declares the capabilities supported by the client.
type MCPClientCapabilities struct {
	Roots    *struct{} `json:"roots,omitempty"`
	Sampling *struct{} `json:"sampling,omitempty"`
}

// MCPImplementation 标识 MCP 端的实现信息（名称 + 版本）。
//
// MCPImplementation identifies an MCP endpoint with name and version.
type MCPImplementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// MCPInitializeResult 是 MCP initialize 请求的响应结果。
//
// MCPInitializeResult is the response to an MCP initialize request.
type MCPInitializeResult struct {
	ProtocolVersion string                `json:"protocolVersion"`
	Capabilities    MCPServerCapabilities `json:"capabilities"`
	ServerInfo      MCPImplementation     `json:"serverInfo"`
}

// MCPServerCapabilities 声明服务端支持的 MCP 能力。
//
// MCPServerCapabilities declares the capabilities supported by the server.
type MCPServerCapabilities struct {
	Prompts   *struct{} `json:"prompts,omitempty"`
	Resources *struct{} `json:"resources,omitempty"`
	Tools     *struct{} `json:"tools,omitempty"`
	Logging   *struct{} `json:"logging,omitempty"`
}

// ── Tool list types ────────────────────────────────────────────────────────

// MCPToolDefinition describes one tool exposed by the server in a
// tools/list response.
// MCPToolDefinition 描述 tools/list 响应中服务端暴露的一个工具。
type MCPToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// MCPListToolsResult 是 tools/list 请求的响应，包含工具列表和下一页游标。
//
// MCPListToolsResult is the response to a tools/list request.
type MCPListToolsResult struct {
	Tools      []MCPToolDefinition `json:"tools"`
	NextCursor string              `json:"nextCursor,omitempty"`
}

// MCPToolListResult is an alias for MCPListToolsResult kept for backward
// compatibility.
// MCPToolListResult 是 MCPListToolsResult 的别名，为向后兼容保留。
type MCPToolListResult = MCPListToolsResult

// ── Tool call types (spec-compliant content) ───────────────────────────────

// MCPToolCallResult is the parsed result of a tools/call response.
// MCPToolCallResult 是 tools/call 响应解析后的结果。
type MCPToolCallResult struct {
	Content []MCPContent `json:"content"`
	IsError bool         `json:"isError"`
}

// MCPContent 是工具调用的响应内容单元，支持 text / image / audio / resource 四种类型。
//
// MCPContent represents a content unit in a tool-call response.
type MCPContent struct {
	Type     string       `json:"type"`
	Text     string       `json:"text,omitempty"`
	Data     string       `json:"data,omitempty"`
	MIMEType string       `json:"mimeType,omitempty"`
	Resource *MCPResource `json:"resource,omitempty"`
}

// MCPResource 表示 MCP 协议中的资源引用，包含 URI 和内容（text 或 blob）。
//
// MCPResource represents a resource reference in the MCP protocol.
type MCPResource struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// MCPContentItem is a backward-compatible alias for MCPContent.
// MCPContentItem 是 MCPContent 的向后兼容别名。
type MCPContentItem = MCPContent

// ── State error ────────────────────────────────────────────────────────────

// MCPStateError reports an operation attempted in an invalid connection
// state.
// MCPStateError 报告在非法连接状态下执行的操作。
type MCPStateError struct {
	State mcpConnState
	Op    string
}

// Error returns the state-error message.
// Error 返回状态错误信息。
func (e *MCPStateError) Error() string {
	stateName := map[mcpConnState]string{
		mcpStateNew: "new", mcpStateInitializing: "initializing",
		mcpStateReady: "ready", mcpStateClosed: "closed",
		mcpStateBroken: "broken",
	}[e.State]
	return fmt.Sprintf("mcp source %s: cannot %s in state %q", e.Op, e.Op, stateName)
}

// ── MCPSource ──────────────────────────────────────────────────────────────

// MCPSource 是 MCP 协议的工具源，通过外部子进程的 stdio JSON-RPC 通信发现和调用工具。
//
// MCPSource manages the full MCP subprocess lifecycle: start → initialize → tools/list → tools/call → close.
//
// MCPSource is an MCP tool source that communicates with a subprocess via stdio JSON-RPC.
//
//	src := NewMCPSource("calc", "npx", nil, "-y", "@modelcontextprotocol/server-calculator")
//	tools, _ := src.ListTools(ctx)
//	result, _ := src.CallTool(ctx, "add", map[string]any{"a": 1, "b": 2})
type MCPSource struct {
	name       string
	cmdStr     string
	args       []string
	env        map[string]string
	mu         sync.Mutex
	cmd        *exec.Cmd
	stdin      *bufio.Writer
	stdout     *bufio.Scanner
	stdoutPipe io.ReadCloser
	reqCounter int64
	toolsCache []kernel.Tool
	cached     bool

	state           mcpConnState
	protocolVersion string
}

// NewMCPSource 创建一个由外部子进程驱动的 MCP 工具源。
//
// 子进程通过标准输入输出（stdio）以 JSON-RPC 格式与 MCPSource 通信。
//
// NewMCPSource creates an MCP tool source driven by an external subprocess.
//
//	src := NewMCPSource("calculator", "npx", nil, "-y", "@anthropic/server-calculator")
func NewMCPSource(name, command string, env map[string]string, args ...string) *MCPSource {
	return &MCPSource{
		name:   name,
		cmdStr: command,
		args:   args,
		env:    env,
		state:  mcpStateNew,
	}
}

// Name returns the source name prefixed with "mcp:".
// Name 返回以 "mcp:" 前缀的来源名称。
func (s *MCPSource) Name() string { return "mcp:" + s.name }

// ── Lifecycle ──────────────────────────────────────────────────────────────

// Close kills the subprocess and seals the source; further operations
// fail with MCPStateError. It never blocks on an in-flight request.
// Close 杀死子进程并封存来源；后续操作以 MCPStateError 失败。它不会
// 因在途请求而阻塞。
func (s *MCPSource) Close() error {
	// Kill the process FIRST, without waiting for the request lock — a
	// hung server must be torn down even while a call is in flight (the
	// old Close blocked on s.mu held across the whole round-trip).
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == mcpStateClosed {
		return nil
	}
	s.state = mcpStateClosed

	var err error
	if s.cmd != nil && s.cmd.Process != nil {
		err = s.cmd.Process.Kill()
	}
	s.cleanupProcess()
	return err
}

func (s *MCPSource) cleanupProcess() {
	s.stdin = nil
	s.stdout = nil
	if s.stdoutPipe != nil {
		s.stdoutPipe.Close()
		s.stdoutPipe = nil
	}
	s.cmd = nil
	s.cached = false
	s.toolsCache = nil
	s.state = mcpStateClosed
}

// ── ensureRunning: start process + initialize handshake ────────────────────

func (s *MCPSource) ensureRunning(ctx context.Context) error {
	if s.cmd != nil && s.cmd.Process != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if s.state == mcpStateReady {
				return nil
			}
			if s.state == mcpStateInitializing {
				return fmt.Errorf("mcp source %q: already initializing", s.name)
			}
		}
	}

	if s.state == mcpStateClosed {
		return &MCPStateError{State: s.state, Op: "start"}
	}
	// mcpStateBroken (process died / pipe failed) restarts here; the
	// broken state replaces the old behavior of serving a dead process
	// forever as if healthy.
	s.state = mcpStateInitializing

	// The process is NOT bound to the caller's ctx: a request-scoped or
	// deadline ctx used to kill the server mid-life via CommandContext
	// (verified defect). Cancellation of individual calls still works
	// through readResponse's ctx.Done branch.
	cmd := exec.Command(s.cmdStr, s.args...)
	if s.env != nil {
		cmd.Env = os.Environ()
		for k, v := range s.env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return fmt.Errorf("start MCP server: %w", err)
	}

	s.cmd = cmd
	s.stdin = bufio.NewWriter(stdin)
	s.stdoutPipe = stdout
	s.stdout = bufio.NewScanner(stdout)
	s.stdout.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	s.reqCounter = 0

	if err := s.doInitialize(ctx); err != nil {
		s.cleanupProcess()
		return fmt.Errorf("initialize %q: %w", s.name, err)
	}
	return nil
}

func (s *MCPSource) doInitialize(ctx context.Context) error {
	raw, err := s.sendRequestRaw(ctx, "initialize", MCPInitializeParams{
		ProtocolVersion: mcpProtocolVersion,
		Capabilities:    MCPClientCapabilities{},
		ClientInfo:      MCPImplementation{Name: "gocel", Version: "1.0.0"},
	})
	if err != nil {
		return fmt.Errorf("initialize request: %w", err)
	}

	var result MCPInitializeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("parse initialize result: %w", err)
	}
	if result.ProtocolVersion == "" {
		return fmt.Errorf("server did not return protocolVersion")
	}
	s.protocolVersion = result.ProtocolVersion

	if err := s.sendNotification(ctx, "notifications/initialized", nil); err != nil {
		return fmt.Errorf("send initialized notification: %w", err)
	}

	s.state = mcpStateReady
	s.cached = false
	return nil
}

// ── ListTools ──────────────────────────────────────────────────────────────

// ListTools discovers tools from the MCP server (paging through
// tools/list), caches the result, and returns them as kernel.Tool
// wrappers.
// ListTools 从 MCP 服务器发现工具（通过 tools/list 分页获取），缓存结果，
// 并以 kernel.Tool 包装返回。
func (s *MCPSource) ListTools(ctx context.Context) ([]kernel.Tool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == mcpStateClosed {
		return nil, &MCPStateError{State: s.state, Op: "listTools"}
	}
	if s.cached {
		return s.toolsCache, nil
	}

	if err := s.ensureRunning(ctx); err != nil {
		return nil, fmt.Errorf("mcp source %q: %w", s.name, err)
	}

	var allDefs []MCPToolDefinition
	cursor := ""
	for {
		defs, next, err := s.listToolsPage(ctx, cursor)
		if err != nil {
			return nil, err
		}
		allDefs = append(allDefs, defs...)
		if next == "" {
			break
		}
		cursor = next
	}

	tools := make([]kernel.Tool, 0, len(allDefs))
	for _, d := range allDefs {
		schema := make(map[string]any)
		if len(d.InputSchema) > 0 {
			if err := json.Unmarshal(d.InputSchema, &schema); err != nil {
				_ = err
			}
		}

		name := d.Name
		desc := d.Description
		if desc == "" {
			desc = fmt.Sprintf("MCP tool: %s", name)
		}
		tool := &mcpTool{
			source: s,
			name:   name,
			desc:   desc,
			schema: schema,
		}
		tools = append(tools, tool)
	}

	s.toolsCache = tools
	s.cached = true
	return tools, nil
}

func (s *MCPSource) listToolsPage(ctx context.Context, cursor string) ([]MCPToolDefinition, string, error) {
	var params map[string]string
	if cursor != "" {
		params = map[string]string{"cursor": cursor}
	}
	raw, err := s.sendRequestDirect(ctx, "tools/list", params)
	if err != nil {
		return nil, "", fmt.Errorf("tools/list: %w", err)
	}
	var result MCPListToolsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, "", fmt.Errorf("parse tools/list result: %w", err)
	}
	return result.Tools, result.NextCursor, nil
}

// ── callTool ───────────────────────────────────────────────────────────────

func (s *MCPSource) callTool(ctx context.Context, name string, args map[string]any) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == mcpStateClosed {
		return "", &MCPStateError{State: s.state, Op: "callTool"}
	}
	if s.cmd == nil {
		return "", fmt.Errorf("mcp source %q: not running, call ListTools first", s.name)
	}

	params := map[string]any{
		"name":      name,
		"arguments": args,
	}
	raw, err := s.sendRequestDirect(ctx, "tools/call", params)
	if err != nil {
		return "", fmt.Errorf("tools/call %q: %w", name, err)
	}
	var result MCPToolCallResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("parse tools/call result: %w", err)
	}
	if result.IsError {
		msg := extractContentSummary(result.Content)
		return "", fmt.Errorf("MCP tool %q: %s", name, msg)
	}

	return formatContent(result.Content), nil
}

// ── Content helpers (spec-compliant) ───────────────────────────────────────

func extractContentSummary(content []MCPContent) string {
	for _, c := range content {
		if c.Type == "text" && c.Text != "" {
			return c.Text
		}
	}
	return "tool returned error"
}

func formatContent(content []MCPContent) string {
	var parts []string
	for _, c := range content {
		switch c.Type {
		case "text":
			if c.Text != "" {
				parts = append(parts, c.Text)
			}
		case "image":
			mime := c.MIMEType
			if mime == "" {
				mime = "image/png"
			}
			parts = append(parts, fmt.Sprintf("[Image: %s (%d bytes)]", mime, len(c.Data)))
		case "audio":
			mime := c.MIMEType
			if mime == "" {
				mime = "audio/mpeg"
			}
			parts = append(parts, fmt.Sprintf("[Audio: %s (%d bytes)]", mime, len(c.Data)))
		case "resource":
			if c.Resource != nil {
				summary := c.Resource.URI
				if c.Resource.Text != "" {
					summary = c.Resource.Text
				}
				parts = append(parts, fmt.Sprintf("[Resource: %s]", summary))
			}
		}
	}
	return strings.Join(parts, "\n")
}

// ── JSON-RPC helpers ───────────────────────────────────────────────────────

func (s *MCPSource) nextID() string {
	return fmt.Sprintf("gocel_%d", atomic.AddInt64(&s.reqCounter, 1))
}

func (s *MCPSource) sendRequestRaw(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	req := MCPRequest{
		JsonRPC: "2.0",
		ID:      s.nextID(),
		Method:  method,
		Params:  params,
	}
	return s.writeAndRead(ctx, req)
}

// markBroken records a dead subprocess / broken pipe so the next
// ensureRunning restarts it. Caller must hold s.mu.
func (s *MCPSource) markBroken() {
	if s.state != mcpStateReady && s.state != mcpStateBroken && s.state != mcpStateInitializing {
		return
	}
	s.cleanupProcess()
	s.state = mcpStateBroken
}

func (s *MCPSource) sendRequestDirect(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	req := MCPRequest{
		JsonRPC: "2.0",
		ID:      s.nextID(),
		Method:  method,
		Params:  params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	if _, err := s.stdin.Write(data); err != nil {
		s.markBroken()
		return nil, fmt.Errorf("write request: %w", err)
	}
	if err := s.stdin.WriteByte('\n'); err != nil {
		s.markBroken()
		return nil, err
	}
	if err := s.stdin.Flush(); err != nil {
		s.markBroken()
		return nil, fmt.Errorf("flush: %w", err)
	}
	resp, err := s.readResponse(ctx)
	if err != nil {
		// A read failure (EOF on a dead process, broken pipe) marks the
		// source broken so the NEXT call restarts it.
		s.markBroken()
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

func (s *MCPSource) sendNotification(ctx context.Context, method string, params interface{}) error {
	req := MCPRequest{
		JsonRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	_, err := s.writeAndRead(ctx, req)
	return err
}

func (s *MCPSource) writeAndRead(ctx context.Context, req MCPRequest) (json.RawMessage, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	if _, err := s.stdin.Write(data); err != nil {
		s.markBroken()
		return nil, fmt.Errorf("write request: %w", err)
	}
	if err := s.stdin.WriteByte('\n'); err != nil {
		s.markBroken()
		return nil, err
	}
	if err := s.stdin.Flush(); err != nil {
		s.markBroken()
		return nil, fmt.Errorf("flush: %w", err)
	}

	if req.ID == nil {
		return nil, nil
	}

	resp, err := s.readResponse(ctx)
	if err != nil {
		s.markBroken()
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

func (s *MCPSource) readResponse(ctx context.Context) (*MCPResponse, error) {
	// Capture stdout into a local variable once. A concurrent Close() may set
	// s.stdout = nil, but the local stays valid for the lifetime of this call.
	stdout := s.stdout
	if stdout == nil {
		return nil, io.ErrClosedPipe
	}

	type scanLine struct {
		line string
		err  error
	}
	ch := make(chan scanLine, 1)

	go func() {
		defer close(ch)
		scanDone := make(chan struct{})
		var result scanLine
		go func() {
			defer close(scanDone)
			if stdout.Scan() {
				result = scanLine{line: stdout.Text()}
			} else {
				if err := stdout.Err(); err != nil {
					result = scanLine{err: err}
				} else {
					result = scanLine{err: io.EOF}
				}
			}
		}()
		select {
		case <-scanDone:
			ch <- result
		case <-ctx.Done():
		}
	}()

	select {
	case result := <-ch:
		if result.err != nil {
			return nil, fmt.Errorf("mcp read response: %w", result.err)
		}
		var resp MCPResponse
		if err := json.Unmarshal([]byte(result.line), &resp); err != nil {
			return nil, fmt.Errorf("unmarshal response: %w", err)
		}
		return &resp, nil

	case <-ctx.Done():
		if s.cmd != nil && s.cmd.Process != nil {
			s.cmd.Process.Kill()
		}
		return nil, ctx.Err()
	}
}

// CallTool is a public wrapper for callTool, useful for testing.
// CallTool 是 callTool 的公开包装，便于测试使用。
func (s *MCPSource) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	return s.callTool(ctx, name, args)
}

// MarshalMCPRequest serializes an MCPRequest to JSON bytes.
// MarshalMCPRequest 将 MCPRequest 序列化为 JSON 字节。
func MarshalMCPRequest(req *MCPRequest) ([]byte, error) {
	return json.Marshal(req)
}

// UnmarshalMCPResponse deserializes JSON bytes into an MCPResponse.
// UnmarshalMCPResponse 将 JSON 字节反序列化为 MCPResponse。
func UnmarshalMCPResponse(data []byte) (*MCPResponse, error) {
	var resp MCPResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ── mcpTool (kernel.Tool wrapper) ──────────────────────────────────────────

type mcpTool struct {
	source *MCPSource
	name   string
	desc   string
	schema map[string]any
}

// Name returns the MCP tool's name.
// Name 返回 MCP 工具名称。
func (t *mcpTool) Name() string           { return t.name }
// Description returns the MCP tool's description.
// Description 返回 MCP 工具描述。
func (t *mcpTool) Description() string    { return t.desc }
// Schema returns a copy of the tool's input schema.
// Schema 返回工具输入 schema 的副本。
func (t *mcpTool) Schema() map[string]any { return cloneMap(t.schema) }
// ToolMeta returns the tool metadata with kind ToolKindMCP.
// ToolMeta 返回工具元数据，类型为 ToolKindMCP。
func (t *mcpTool) ToolMeta() kernel.ToolMeta {
	return kernel.ToolMeta{Kind: kernel.ToolKindMCP, Source: "mcp:" + t.source.name}
}

// Run forwards the JSON arguments to the MCP server and returns the
// formatted content result.
// Run 将 JSON 参数转发给 MCP 服务器，并返回格式化后的内容结果。
func (t *mcpTool) Run(ctx context.Context, argsJSON string) (string, error) {
	args := make(map[string]any)
	if argsJSON != "" && argsJSON != "{}" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("mcp tool %q: parse args: %w", t.name, err)
		}
	}
	return t.source.callTool(ctx, t.name, args)
}
