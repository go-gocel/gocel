package tool

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/go-gocel/gocel/core/kernel"
)

// MapToolRegistry implements kernel.ToolRegistry with in-memory storage.
// It provides thread-safe CRUD operations with caching.
// Sources are flattened before entering the registry — see runtime.Register.
//
// MapToolRegistry 实现 kernel.ToolRegistry，内存存储，线程安全，带缓存。
// 来源在 runtime.Register 中已展平。
type MapToolRegistry struct {
	mu     sync.RWMutex
	tools  map[string]kernel.Tool
	cache  []kernel.Tool
	cached bool
}

// NewMapToolRegistry creates a tool registry from the given tool slice.
//
// NewMapToolRegistry 根据工具切片创建工具注册表。
func NewMapToolRegistry(tools []kernel.Tool) *MapToolRegistry {
	r := &MapToolRegistry{tools: make(map[string]kernel.Tool, len(tools))}
	for _, t := range tools {
		r.tools[t.Name()] = t
	}
	return r
}

// Add registers a tool under its name, failing if the name is already
// taken.
// Add 以工具名注册工具，名称已存在时返回错误。
func (r *MapToolRegistry) Add(ctx context.Context, tool kernel.Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[tool.Name()]; ok {
		return fmt.Errorf("tool %q already registered", tool.Name())
	}
	r.tools[tool.Name()] = tool
	r.cached = false
	return nil
}

// Remove unregisters the named tool, failing if it is unknown.
// Remove 注销指定名称的工具，未知时返回错误。
func (r *MapToolRegistry) Remove(ctx context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; !ok {
		return fmt.Errorf("tool %q not found", name)
	}
	delete(r.tools, name)
	r.cached = false
	return nil
}

// List returns all registered tools; the result is cached and copied on
// return.
// List 返回全部已注册工具；结果带缓存，返回时复制。
func (r *MapToolRegistry) List(ctx context.Context) []kernel.Tool {
	r.mu.RLock()
	if r.cached {
		result := make([]kernel.Tool, len(r.cache))
		copy(result, r.cache)
		r.mu.RUnlock()
		return result
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	// Double-check after acquiring write lock
	if r.cached {
		result := make([]kernel.Tool, len(r.cache))
		copy(result, r.cache)
		return result
	}

	result := make([]kernel.Tool, 0, len(r.tools))

	for _, t := range r.tools {
		result = append(result, t)
	}

	// Map iteration order is randomized by the Go runtime; sort by name so the
	// returned slice (and thus the tools array serialized into each model
	// request) stays byte-stable across calls, cache rebuilds, and process
	// restarts — a deterministic prompt-cache prefix.
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name() < result[j].Name()
	})

	r.cache = make([]kernel.Tool, len(result))
	copy(r.cache, result)
	r.cached = true

	return result
}

// Get returns the tool registered under name, or nil when absent.
// Get 返回 name 下注册的工具，不存在时返回 nil。
func (r *MapToolRegistry) Get(ctx context.Context, name string) kernel.Tool {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if ok {
		return t
	}
	return nil
}

// SimpleFuncTool is a lightweight tool built from a Go function with a schema.
//
// SimpleFuncTool 是从 Go 函数 + schema 构建的轻量级工具。
type SimpleFuncTool struct {
	name        string
	description string
	schema      map[string]any
	fn          func(ctx context.Context, argsJSON string) (string, error)
	kind        kernel.ToolKind
	source      string
}

// SimpleFuncToolOption configures a SimpleFuncTool.
// SimpleFuncToolOption 是 SimpleFuncTool 的配置选项函数。
type SimpleFuncToolOption func(*SimpleFuncTool)

// WithSimpleToolKind sets the tool kind.
// WithSimpleToolKind 设置工具类型。
func WithSimpleToolKind(kind kernel.ToolKind) SimpleFuncToolOption {
	return func(t *SimpleFuncTool) { t.kind = kind }
}

// WithSimpleToolSource sets the tool source identifier.
// WithSimpleToolSource 设置工具来源。
func WithSimpleToolSource(source string) SimpleFuncToolOption {
	return func(t *SimpleFuncTool) { t.source = source }
}

// NewSimpleFuncTool creates a SimpleFuncTool from a name, schema, and handler function.
//
// NewSimpleFuncTool 根据名称、schema 和处理函数创建 SimpleFuncTool。
func NewSimpleFuncTool(name, description string, schema map[string]any, fn func(ctx context.Context, argsJSON string) (string, error), opts ...SimpleFuncToolOption) *SimpleFuncTool {
	t := &SimpleFuncTool{
		name:        name,
		description: description,
		schema:      schema,
		fn:          fn,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Name returns the tool's name.
// Name 返回工具名称。
func (t *SimpleFuncTool) Name() string        { return t.name }
// Description returns the tool's description.
// Description 返回工具描述。
func (t *SimpleFuncTool) Description() string { return t.description }
// ListTools returns the tool itself as the only entry.
// ListTools 返回只包含工具自身的列表。
func (t *SimpleFuncTool) ListTools(_ context.Context) ([]kernel.Tool, error) {
	return []kernel.Tool{t}, nil
}
// Schema returns a copy of the tool's input schema.
// Schema 返回工具输入 schema 的副本。
func (t *SimpleFuncTool) Schema() map[string]any {
	clone := make(map[string]any, len(t.schema))
	for k, v := range t.schema {
		clone[k] = v
	}
	return clone
}
// Run invokes the wrapped handler with the JSON arguments.
// Run 以 JSON 参数调用被包装的处理函数。
func (t *SimpleFuncTool) Run(ctx context.Context, args string) (string, error) {
	return t.fn(ctx, args)
}
// ToolMeta returns the tool metadata (kind and source).
// ToolMeta 返回工具元数据（类型与来源）。
func (t *SimpleFuncTool) ToolMeta() kernel.ToolMeta {
	return kernel.ToolMeta{Kind: t.kind, Source: t.source}
}

// cloneMap returns a shallow copy of a map[string]any.
func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	clone := make(map[string]any, len(m))
	for k, v := range m {
		clone[k] = v
	}
	return clone
}
