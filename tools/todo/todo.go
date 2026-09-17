package todo

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	coresession "github.com/go-gocel/gocel/core/session"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
	"github.com/go-gocel/gocel/internal/toolutil"
)

// ─── Provider ────────────────────────────────────────────────────────────────

// Provider exposes the todo tool to the registry.
//
// Provider 向注册表暴露 todo 工具。
type Provider struct {
	mu   sync.Mutex
	tool *todoTool
}

// New creates a new todo tool provider. The tool instance retains state
// across calls within the same session.
//
// New 创建新的 todo 工具 provider。工具实例在同一会话内跨调用保留状态。
func New() *Provider {
	return &Provider{tool: &todoTool{
		tasks: make(map[string]*todoTask),
	}}
}

// WithLog attaches a session event log to the provider: every add/update
// writes the whole task list as a log-only "todo/write" event (last-wins
// whole-value, DSH todo_write), so projection units and GUIs can render
// the durable list. Without a log the tool keeps its in-memory behavior.
//
// WithLog 把会话事件日志挂到 provider：每次 add/update 把整个任务列表
// 写为 log-only "todo/write" 事件（last-wins 整值，DSH todo_write），
// 投影单元与 GUI 可渲染持久列表。无日志时工具保持内存态行为。
func (p *Provider) WithLog(log *coresession.Log) *Provider {
	p.mu.Lock()
	p.tool.log = log
	p.mu.Unlock()
	return p
}

// ListTools returns the provider's tools.
//
// ListTools 返回该 provider 提供的工具列表。
func (p *Provider) ListTools() []kernel.Tool {
	return []kernel.Tool{p.tool}
}

// ─── Task model ──────────────────────────────────────────────────────────────

type todoStatus string

const (
	statusPending    todoStatus = "pending"
	statusInProgress todoStatus = "in_progress"
	statusCompleted  todoStatus = "completed"
)

var validStatuses = map[todoStatus]bool{
	statusPending:    true,
	statusInProgress: true,
	statusCompleted:  true,
}

// Marker returns the checklist marker for the status: "[ ]" for pending,
// "[>]" for in progress, "[x]" for completed, "[?]" otherwise.
//
// Marker 返回状态对应的清单标记："[ ]" 待办、"[>]" 进行中、"[x]" 已完成、其余为 "[?]"。
func (s todoStatus) Marker() string {
	switch s {
	case statusPending:
		return "[ ]"
	case statusInProgress:
		return "[>]"
	case statusCompleted:
		return "[x]"
	default:
		return "[?]"
	}
}

type todoTask struct {
	ID      string     `json:"id"`
	Content string     `json:"content"`
	Status  todoStatus `json:"status"`
}

// ─── Tool ────────────────────────────────────────────────────────────────────

type todoTool struct {
	mu     sync.Mutex
	nextID atomic.Int64
	tasks  map[string]*todoTask
	// log, when set, receives the whole task list as a "todo/write" event
	// after every mutation (DSH todo_write whole-value last-wins).
	log *coresession.Log
}

// Name returns the tool name "todo".
//
// Name 返回工具名 "todo"。
func (t *todoTool) Name() string { return "todo" }

// Description describes the todo tool for the model.
//
// Description 向模型描述 todo 工具的用途与支持的操作。
func (t *todoTool) Description() string {
	return "Manage task checklists for complex multi-step workflows. Supports add (create a task), update (mark task as pending/in_progress/completed), and list (show all tasks with status)."
}

// Schema returns the JSON schema for the tool arguments.
//
// Schema 返回工具参数的 JSON schema。
func (t *todoTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Action to perform: add (create a task), update (change task status), list (show all tasks)",
				"enum":        []string{"add", "update", "list"},
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Task content/description (required for add action)",
			},
			"id": map[string]any{
				"type":        "string",
				"description": "Task ID (required for update action)",
			},
			"status": map[string]any{
				"type":        "string",
				"description": "New status for update action: pending, in_progress, or completed",
				"enum":        []string{"pending", "in_progress", "completed"},
			},
		},
		"required": []string{"action"},
	}
}

// ToolMeta returns the tool metadata.
//
// ToolMeta 返回工具元数据。
func (t *todoTool) ToolMeta() kernel.ToolMeta { return kernel.ToolMeta{Source: "builtin"} }

type todoArgs struct {
	Action  string `json:"action"`
	Content string `json:"content"`
	ID      string `json:"id"`
	Status  string `json:"status"`
}

// Run executes the todo action (add, update, or list) encoded in argsJSON.
//
// Run 执行 argsJSON 中编码的 todo 操作（add、update 或 list）。
func (t *todoTool) Run(ctx context.Context, argsJSON string) (string, error) {
	var args todoArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("todo: invalid args: %w", err)
	}

	switch args.Action {
	case "add":
		return t.add(ctx, args)
	case "update":
		return t.update(ctx, args)
	case "list":
		return t.list(ctx, args)
	default:
		return "", fmt.Errorf("todo: unknown action %q (valid: add, update, list)", args.Action)
	}
}

// ─── Actions ─────────────────────────────────────────────────────────────────

func (t *todoTool) add(_ context.Context, args todoArgs) (string, error) {
	if strings.TrimSpace(args.Content) == "" {
		return toolutil.FormatError(fmt.Errorf("content is required for add action")), nil
	}

	id := fmt.Sprintf("T%d", t.nextID.Add(1))

	t.mu.Lock()
	t.tasks[id] = &todoTask{
		ID:      id,
		Content: strings.TrimSpace(args.Content),
		Status:  statusPending,
	}
	t.logSnapshotLocked()
	t.mu.Unlock()

	return toolutil.FormatResult(fmt.Sprintf("Created task %s: %s", id, args.Content), map[string]any{
		"id":      id,
		"content": args.Content,
		"status":  string(statusPending),
	}), nil
}

func (t *todoTool) update(_ context.Context, args todoArgs) (string, error) {
	if args.ID == "" {
		return toolutil.FormatError(fmt.Errorf("id is required for update action")), nil
	}

	status := todoStatus(strings.TrimSpace(strings.ToLower(args.Status)))
	if !validStatuses[status] {
		return toolutil.FormatError(fmt.Errorf("invalid status %q (valid: pending, in_progress, completed)", args.Status)), nil
	}

	t.mu.Lock()
	task, ok := t.tasks[args.ID]
	if !ok {
		t.mu.Unlock()
		return toolutil.FormatError(fmt.Errorf("task %s not found", args.ID)), nil
	}
	task.Status = status
	t.logSnapshotLocked()
	t.mu.Unlock()

	return toolutil.FormatResult(fmt.Sprintf("Updated task %s to %s %s", args.ID, status.Marker(), task.Content), map[string]any{
		"id":      task.ID,
		"content": task.Content,
		"status":  string(status),
	}), nil
}

func (t *todoTool) list(_ context.Context, _ todoArgs) (string, error) {
	t.mu.Lock()
	tasks := make([]*todoTask, 0, len(t.tasks))
	for _, task := range t.tasks {
		tasks = append(tasks, task)
	}
	t.mu.Unlock()

	if len(tasks) == 0 {
		return toolutil.FormatResult("No tasks created yet.", map[string]any{
			"tasks": []map[string]any{},
			"total": 0,
		}), nil
	}

	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].ID < tasks[j].ID
	})

	lines := make([]string, 0, len(tasks)+2)
	lines = append(lines, "## Task Checklist\n")
	for _, task := range tasks {
		lines = append(lines, fmt.Sprintf("- %s **%s** %s", task.Status.Marker(), task.ID, task.Content))
	}
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("**Total: %d tasks** — %d pending, %d in progress, %d completed",
		len(tasks),
		countByStatus(tasks, statusPending),
		countByStatus(tasks, statusInProgress),
		countByStatus(tasks, statusCompleted),
	))

	formattedTasks := make([]map[string]any, 0, len(tasks))
	for _, task := range tasks {
		formattedTasks = append(formattedTasks, map[string]any{
			"id":      task.ID,
			"content": task.Content,
			"status":  string(task.Status),
		})
	}

	return toolutil.FormatResult(strings.Join(lines, "\n"), map[string]any{
		"tasks": formattedTasks,
		"total": len(tasks),
	}), nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// logSnapshotLocked persists the whole task list to the attached log as a
// "todo/write" whole-value event (last-wins). Called with t.mu held. A
// failure is dropped — the log is derived, the in-memory list is the tool's
// authoritative state (DSH: the log is the durable read model, never a
// decision input here).
func (t *todoTool) logSnapshotLocked() {
	if t.log == nil {
		return
	}
	items := make([]map[string]any, 0, len(t.tasks))
	for _, task := range t.tasks {
		items = append(items, map[string]any{
			"id":      task.ID,
			"content": task.Content,
			"status":  string(task.Status),
		})
	}
	_, _ = t.log.Append(types.NewLogOnlyEvent("todo/write", map[string]any{"items": items}))
}

func countByStatus(tasks []*todoTask, s todoStatus) int {
	n := 0
	for _, t := range tasks {
		if t.Status == s {
			n++
		}
	}
	return n
}

// AllTools returns all tools in this package.
//
// AllTools 返回本包的全部工具。
func AllTools() []kernel.Tool {
	return New().ListTools()
}
