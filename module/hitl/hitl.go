// Package hitl provides Human-In-The-Loop interaction for agents.
//
// Three interaction modes:
//
//  1. Tool approval — intercept specific tools before execution, wait for human
//     approve/reject/modify. Supports conditional interception.
//
//  2. User dialogue — register an "ask_user" tool that the agent can call to ask
//     the user questions, present options, and get responses. Supports multi-turn
//     conversation — the agent can call ask_user multiple times within one execution.
//
//  3. Selection — when the agent presents options, the human selects one.
//     Integrated into the dialogue tool via the "options" parameter.
//
// Example:
//
//	hm := hitl.New(
//	    hitl.WithApproveTool("delete_file", types.HITLModeConfirm),
//	    hitl.WithConditionalApprove("rm", types.HITLModeConfirm, func(args string) bool {
//	        return !strings.Contains(args, "/tmp") // only approve non-tmp paths
//	    }),
//	    hitl.WithUserDialogue("ask_user", "Ask the user a question or present options"),
//	)
//	agent := agents.NewReactAgent(model,
//	    agents.WithTools(append(tools, hm.AsTools()...)),
//	    agents.WithModules([]kernel.Module{hm}),
//	)
//
// The module works transparently with Graph orchestration — when a graph node
// calls ask_user, the entire graph pauses until the human responds.
package hitl

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
	"github.com/go-gocel/gocel/jsonx"
)

// ── Module ──────────────────────────────────────────────────────────────

// Module provides HITL interaction through hook interception and a dialogue tool.
//
// Module 通过钩子拦截与对话工具提供人工介入（HITL）交互。
type Module struct {
	mu sync.RWMutex

	// Tool approval rules
	rules []approveRule

	// User dialogue tool
	dialogueName string
	dialogueDesc string

	timeout         time.Duration
	timeoutBehavior TimeoutBehavior
}

// approveRule defines a tool approval rule.
type approveRule struct {
	toolName  string
	mode      types.HITLMode
	condition func(args string) bool // optional: only intercept when true (nil = always)
}

// TimeoutBehavior defines what happens when the human does not respond
// before the timeout expires.
//
// TimeoutBehavior 定义人工超时未响应时的处理方式。
type TimeoutBehavior int

const (
	// TimeoutReject treats a timeout as a rejection: the tool call fails
	// with a "timeout rejected" error. Safe default — no approval means
	// no execution.
	// TimeoutReject 超时视为拒绝：工具调用失败（默认，安全优先）。
	TimeoutReject TimeoutBehavior = iota
	// TimeoutSkip silently skips approval and lets the tool call through.
	// TimeoutSkip 超时跳过审批，放行工具调用（慎用）。
	TimeoutSkip
	// TimeoutApprove auto-approves the tool call after the timeout.
	// TimeoutApprove 超时自动批准工具调用（慎用）。
	TimeoutApprove
	// TimeoutError returns a timeout error to the agent.
	// TimeoutError 超时向 Agent 返回错误。
	TimeoutError
)

// Option configures the HITL module.
//
// Option 配置 HITL 模块。
type Option func(*Module)

// WithApproveTool requires human approval before executing the named tool.
//
// WithApproveTool 要求在执行指定工具前获得人工审批。
func WithApproveTool(toolName string, mode types.HITLMode) Option {
	return func(m *Module) {
		m.rules = append(m.rules, approveRule{toolName: toolName, mode: mode})
	}
}

// WithConditionalApprove requires human approval only when condition(args) returns true.
// This allows fine-grained control: approve file deletion only for non-tmp paths, etc.
//
// WithConditionalApprove 仅当 condition(args) 返回 true 时才要求人工审批，
// 支持细粒度控制（例如只放行非 tmp 路径的文件删除）。
func WithConditionalApprove(toolName string, mode types.HITLMode, condition func(args string) bool) Option {
	return func(m *Module) {
		m.rules = append(m.rules, approveRule{toolName: toolName, mode: mode, condition: condition})
	}
}

// WithUserDialogue registers a tool for bidirectional conversation.
// The agent can call this tool with a "question" and optional "options" array.
// The human's text response (or selected option) is returned as the tool result.
//
// WithUserDialogue 注册双向对话工具：代理可用 "question" 与可选的 "options"
// 数组调用它，人工的文本回复（或所选选项）作为工具结果返回。
func WithUserDialogue(name, description string) Option {
	return func(m *Module) {
		m.dialogueName = name
		m.dialogueDesc = description
	}
}

// WithTimeout sets the max wait duration for human response (default 5min).
//
// WithTimeout 设置等待人工响应的最长时长（默认 5 分钟）。
func WithTimeout(d time.Duration) Option {
	return func(m *Module) { m.timeout = d }
}

// WithTimeoutBehavior sets the behavior when the human does not respond
// before the timeout (default TimeoutReject — no approval means no execution).
//
// WithTimeoutBehavior 设置人工在超时前未响应时的行为（默认 TimeoutReject——
// 不审批即不执行）。
func WithTimeoutBehavior(b TimeoutBehavior) Option {
	return func(m *Module) { m.timeoutBehavior = b }
}

// New creates a HITL module.
//
// New 创建一个 HITL 模块。
func New(opts ...Option) *Module {
	m := &Module{
		timeout:         5 * time.Minute,
		timeoutBehavior: TimeoutReject,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Register implements kernel.Module.
//
// Register 实现 kernel.Module 接口。
func (m *Module) Register(rt kernel.HookRegistrar) {
	rt.OnToolCall(m.onToolCall)
}

// AsTools returns the tools exposed by this module.
//
// AsTools 返回本模块暴露的工具列表。
func (m *Module) AsTools() []kernel.Tool {
	if m.dialogueName == "" {
		return nil
	}
	return []kernel.Tool{&dialogueTool{
		name: m.dialogueName,
		desc: m.dialogueDesc,
		mod:  m,
	}}
}

// ── Tool approval ───────────────────────────────────────────────────────

func (m *Module) onToolCall(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
	// Dialogue tool always passes through
	if m.dialogueName != "" && info.Name == m.dialogueName {
		return ctx, info, nil
	}

	rule := m.matchRule(info.Name, info.Args)
	if rule == nil {
		return ctx, info, nil
	}

	return m.requestApproval(ctx, info, rule.mode)
}

func (m *Module) matchRule(name, args string) *approveRule {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.rules {
		r := &m.rules[i]
		if r.toolName != name {
			continue
		}
		if r.condition == nil || r.condition(args) {
			return r
		}
	}
	return nil
}

func (m *Module) requestApproval(ctx context.Context, info *kernel.ToolCallInfo, mode types.HITLMode) (context.Context, *kernel.ToolCallInfo, error) {
	ac := kernel.GetAgentContext(ctx)
	if ac == nil {
		return ctx, nil, fmt.Errorf("hitl: tool %q requires AgentContext in context", info.Name)
	}

	hitlInfo := types.NewHITLInfo(mode, info.Name, info.Args)
	if se := ac.SendEvent(); se != nil {
		se(hitlInfo.ToInterruptEvent())
	}

	inputCh := ac.InterruptInput()
	if inputCh == nil {
		return ctx, nil, fmt.Errorf("hitl: tool %q requires InterruptInput channel", info.Name)
	}

	decision, timedOut, err := m.waitForDecision(ctx, inputCh)
	if err != nil {
		if timedOut {
			m.fireDecision(ctx, info, "timeout_error", err.Error())
		}
		return ctx, nil, fmt.Errorf("hitl: tool %q: %w", info.Name, err)
	}

	switch {
	case decision.Approved && decision.ModifiedArgs != "":
		info.Args = decision.ModifiedArgs
		m.fireDecision(ctx, info, decisionLabel("approved", timedOut), "modified")
		return ctx, info, nil
	case decision.Approved:
		m.fireDecision(ctx, info, decisionLabel("approved", timedOut), "")
		return ctx, info, nil
	case decision.Skip:
		m.fireDecision(ctx, info, decisionLabel("skipped", timedOut), decision.Feedback)
		return ctx, info, nil
	default:
		msg := decision.Feedback
		if msg == "" {
			msg = fmt.Sprintf("tool %q rejected (mode=%s)", info.Name, mode)
		}
		m.fireDecision(ctx, info, decisionLabel("rejected", timedOut), msg)
		return ctx, nil, fmt.Errorf("%s", msg)
	}
}

// fireDecision 把审批决策上报给框架决策钩子（audit/observability 订阅）。
// 上报失败只影响留痕，不改变决策结果。
func (m *Module) fireDecision(ctx context.Context, info *kernel.ToolCallInfo, decision, reason string) {
	if rt := kernel.RuntimeFromContext(ctx); rt != nil {
		_ = rt.FireDecision(ctx, &kernel.DecisionInfo{
			Tool:     info.Name,
			Args:     info.Args,
			Decision: decision,
			Reason:   reason,
		})
	}
}

// decisionLabel maps a base decision to its timeout variant.
func decisionLabel(base string, timedOut bool) string {
	if !timedOut {
		return base
	}
	return "timeout_" + base
}

// ── User dialogue tool ──────────────────────────────────────────────────

// dialogueTool implements kernel.Tool. It pauses execution to get human input.
// The human's response becomes the tool's return value, which the agent then
// processes as part of its ReAct loop. Multi-turn conversation is naturally
// supported — the agent can call this tool as many times as needed. A batch
// of questions (DSH user-questions batch) is asked in one interruption;
// presentation intent (DSH presentation intent, e.g. plan-review) rides on
// the interruption for UIs that recognise it.
type dialogueTool struct {
	name string
	desc string
	mod  *Module
}

// Name returns the dialogue tool's name.
//
// Name 返回对话工具的名称。
func (t *dialogueTool) Name() string        { return t.name }
// Description returns the dialogue tool's description.
//
// Description 返回对话工具的描述。
func (t *dialogueTool) Description() string { return t.desc }
// Schema returns the JSON schema describing the tool's arguments.
//
// Schema 返回描述工具参数的 JSON schema。
func (t *dialogueTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": map[string]any{
				"type":        "string",
				"description": "The question to ask the user (single-question form)",
			},
			"options": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional list of choices for the user to pick from",
			},
			"questions": map[string]any{
				"type":        "array",
				"description": "Batch of questions to ask in one interruption (each: id, question, optional detail/header/options/multi_select)",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":           map[string]any{"type": "string", "description": "Stable answer key"},
						"question":     map[string]any{"type": "string", "description": "Question text"},
						"detail":       map[string]any{"type": "string", "description": "Supporting text (optional)"},
						"header":       map[string]any{"type": "string", "description": "Short heading (optional)"},
						"options":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Choices (optional; empty = free text)"},
						"multi_select": map[string]any{"type": "boolean", "description": "Allow several selections (default false)"},
					},
					"required": []any{"id", "question"},
				},
			},
			"intent": map[string]any{
				"type":        "string",
				"description": "Presentation intent (e.g. \"plan-review\" presents the question as a plan under review); optional, presentation only",
			},
		},
	}
}
// ToolMeta returns the tool's metadata (kind and source).
//
// ToolMeta 返回工具的元数据（类型与来源）。
func (t *dialogueTool) ToolMeta() kernel.ToolMeta {
	return kernel.ToolMeta{Kind: kernel.ToolKindBuiltin, Source: "hitl"}
}

// Run sends an interrupt event and waits for the human's text response.
//
// Run 发送中断事件并等待人工的文本回复。
func (t *dialogueTool) Run(ctx context.Context, argsJSON string) (string, error) {
	ac := kernel.GetAgentContext(ctx)
	if ac == nil {
		return "", fmt.Errorf("hitl: AgentContext not found in context")
	}

	// Parse args
	var args struct {
		Question  string   `json:"question"`
		Options   []string `json:"options,omitempty"`
		Questions []struct {
			ID          string   `json:"id"`
			Question    string   `json:"question"`
			Detail      string   `json:"detail,omitempty"`
			Header      string   `json:"header,omitempty"`
			Options     []string `json:"options,omitempty"`
			MultiSelect bool     `json:"multi_select,omitempty"`
		} `json:"questions,omitempty"`
		Intent string `json:"intent,omitempty"`
	}
	if err := jsonx.Unmarshal([]byte(argsJSON), &args); err != nil {
		args.Question = strings.TrimSpace(argsJSON)
	}

	// Build interrupt event with question + options
	info := &types.HITLInfo{
		Mode:      "dialogue",
		Status:    types.HITLStatusPending,
		ToolName:  t.name,
		ToolArgs:  argsJSON,
		Timestamp: time.Now(),
		Prompt:    args.Question,
		Options:   args.Options,
		Intent:    args.Intent,
	}
	// Batch form: map the parsed batch into typed questions.
	if len(args.Questions) > 0 {
		info.Questions = make([]types.HITLQuestion, 0, len(args.Questions))
		for _, q := range args.Questions {
			info.Questions = append(info.Questions, types.HITLQuestion{
				ID:          q.ID,
				Question:    q.Question,
				Detail:      q.Detail,
				Header:      q.Header,
				Options:     q.Options,
				MultiSelect: q.MultiSelect,
			})
		}
	}
	if se := ac.SendEvent(); se != nil {
		se(info.ToInterruptEvent())
	}

	// Wait for human response
	inputCh := ac.InterruptInput()
	if inputCh == nil {
		return "", fmt.Errorf("hitl: InterruptInput channel not set")
	}

	decision, _, err := t.mod.waitForDecision(ctx, inputCh)
	if err != nil {
		return "", fmt.Errorf("hitl: wait for response: %w", err)
	}

	// Batch answers: render each answered question compactly.
	if len(info.Questions) > 0 && len(decision.Answers) > 0 {
		var parts []string
		for _, a := range decision.Answers {
			label := "answered"
			for _, q := range info.Questions {
				if q.ID == a.ID {
					label = q.Question
					break
				}
			}
			switch {
			case len(a.Selected) > 0:
				parts = append(parts, fmt.Sprintf("%s: %s", label, strings.Join(a.Selected, ", ")))
			case a.Custom != "":
				parts = append(parts, fmt.Sprintf("%s: %s", label, a.Custom))
			default:
				parts = append(parts, fmt.Sprintf("%s: (skipped)", label))
			}
		}
		return strings.Join(parts, "\n"), nil
	}

	// Build response:
	//   - If human selected an option: return the selected value
	//   - If human provided text: return the text
	//   - Otherwise: "User skipped"
	if decision.SelectedOption != "" {
		return fmt.Sprintf("用户选择了: %s", decision.SelectedOption), nil
	}
	if decision.Feedback != "" {
		return decision.Feedback, nil
	}
	if decision.Approved {
		return "用户已确认", nil
	}
	return "用户跳过了这个问题", nil
}

// ── Shared: wait for human response ─────────────────────────────────────

// waitForDecision blocks until a decision arrives, the context is done, or
// the timeout fires. The bool reports whether the timeout path produced the
// result (so callers can label timeout_* decisions distinctly).
func (m *Module) waitForDecision(ctx context.Context, inputCh <-chan string) (*types.HITLDecision, bool, error) {
	timeout := m.timeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}
	if timeout <= 0 {
		return nil, false, fmt.Errorf("timeout waiting for human response")
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-timer.C:
			d, err := m.onTimeout()
			return d, true, err
		case raw, ok := <-inputCh:
			if !ok {
				return nil, false, fmt.Errorf("interrupt channel closed")
			}
			var d types.HITLDecision
			if err := json.Unmarshal([]byte(raw), &d); err != nil {
				// Non-JSON → treat as plain text feedback
				return &types.HITLDecision{Feedback: raw}, false, nil
			}
			return &d, false, nil
		}
	}
}

// onTimeout maps the timeout to the configured behavior.
func (m *Module) onTimeout() (*types.HITLDecision, error) {
	switch m.timeoutBehavior {
	case TimeoutSkip:
		return &types.HITLDecision{Skip: true}, nil
	case TimeoutApprove:
		return &types.HITLDecision{Approved: true}, nil
	case TimeoutError:
		return nil, fmt.Errorf("timeout waiting for human response")
	default: // TimeoutReject
		return &types.HITLDecision{Approved: false, Feedback: "timeout rejected: no human approval"}, nil
	}
}
