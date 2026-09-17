package kernel

import (
	"context"
	"errors"
	"fmt"
)

// ❄️ FROZEN — Stable sentinel errors. Names and semantics must not change.
// ❄️ FROZEN 稳定哨兵错误：名称与语义不得变更。
var (
	ErrAgentNotFound      = errors.New("agent not found")
	ErrToolNotFound       = errors.New("tool not found")
	ErrModelNotFound      = errors.New("model not found")
	ErrSessionNotFound    = errors.New("session not found")
	ErrCheckpointNotFound = errors.New("checkpoint not found")
	ErrRegistryNotFound   = errors.New("registry item not found")
	ErrInvalidInput       = errors.New("invalid input")
	ErrNilModel           = errors.New("nil model")
	ErrMaxStepsReached    = errors.New("max steps reached")
	ErrMaxCyclesReached   = errors.New("max cycles reached")
	ErrStreamError        = errors.New("stream error")
	ErrMiddlewareError    = errors.New("middleware error")
	ErrNilTools           = errors.New("nil tool registry")
	ErrCircuitOpen        = errors.New("circuit breaker is open")
	ErrRateLimited        = errors.New("rate limited")
	ErrProtocolError      = errors.New("protocol error")
	ErrMCPError           = errors.New("mcp protocol error")
)

// NotFoundError wraps a not-found error with context about what was looked up.
//
// NotFoundError 封装"未找到"错误，附带了查找的上下文信息。
type NotFoundError struct {
	Kind    string
	Key     string
	Cause   error
	Details map[string]any
}

// NewNotFoundError creates a NotFoundError for the given kind and key.
// NewNotFoundError 为指定的 kind 和 key 创建 NotFoundError。
func NewNotFoundError(kind, key string) *NotFoundError {
	return &NotFoundError{
		Kind:    kind,
		Key:     key,
		Cause:   resolveNotFoundCause(kind),
		Details: make(map[string]any),
	}
}

// Error returns the formatted "not found" message.
// Error 返回格式化后的"未找到"错误消息。
func (e *NotFoundError) Error() string {
	if e.Key != "" {
		return fmt.Sprintf("%s not found: %q", e.Kind, e.Key)
	}
	return fmt.Sprintf("%s not found", e.Kind)
}

// Unwrap returns the underlying cause error.
// Unwrap 返回底层原因错误。
func (e *NotFoundError) Unwrap() error {
	return e.Cause
}

// WithDetail attaches a context key/value to the error and returns itself for chaining.
// WithDetail 向错误附加一条上下文键值，并返回自身以支持链式调用。
func (e *NotFoundError) WithDetail(key string, value any) *NotFoundError {
	if e.Details == nil {
		e.Details = make(map[string]any)
	}
	e.Details[key] = value
	return e
}

func resolveNotFoundCause(kind string) error {
	switch kind {
	case "agent":
		return ErrAgentNotFound
	case "tool":
		return ErrToolNotFound
	case "model":
		return ErrModelNotFound
	case "session":
		return ErrSessionNotFound
	case "checkpoint":
		return ErrCheckpointNotFound
	case "registry":
		return ErrRegistryNotFound
	default:
		return fmt.Errorf("unknown not-found kind: %s", kind)
	}
}

// InputError wraps an input validation error with field context.
// InputError 封装输入校验错误，附带字段上下文。
type InputError struct {
	Field   string
	Value   any
	Cause   error
	Message string
}

// NewInputError creates an InputError with the given field, value, cause,
// and a formatted message.
// NewInputError 创建 InputError，包含字段、值、根因与格式化消息。
func NewInputError(field string, value any, cause error, format string, args ...any) *InputError {
	msg := fmt.Sprintf(format, args...)
	if cause == nil {
		cause = ErrInvalidInput
	}
	return &InputError{
		Field:   field,
		Value:   value,
		Cause:   cause,
		Message: msg,
	}
}

// Error returns the formatted input error message.
// Error 返回格式化后的输入错误消息。
func (e *InputError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("input error: field %q: %s", e.Field, e.Message)
	}
	return fmt.Sprintf("input error: %s", e.Message)
}

// Unwrap returns the underlying cause error.
// Unwrap 返回底层原因错误。
func (e *InputError) Unwrap() error {
	return e.Cause
}

// StepError wraps step/cycle limit errors with actual count context.
// StepError 封装步数/循环上限错误，附带实际计数上下文。
type StepError struct {
	Kind      string
	Limit     int
	Actual    int
	Cause     error
	AgentName string
}

// NewStepError creates a StepError for the given kind (e.g. "step" or "cycle"),
// with limit and actual counts.
// NewStepError 创建 StepError，指定种类（如 step/cycle）、上限与实际计数。
func NewStepError(kind string, limit, actual int, agentName string) *StepError {
	var cause error
	switch kind {
	case "cycle":
		cause = ErrMaxCyclesReached
	default:
		cause = ErrMaxStepsReached
	}
	return &StepError{
		Kind:      kind,
		Limit:     limit,
		Actual:    actual,
		Cause:     cause,
		AgentName: agentName,
	}
}

// Error returns the formatted step limit message.
// Error 返回格式化后的步数上限消息。
func (e *StepError) Error() string {
	if e.AgentName != "" {
		return fmt.Sprintf("agent %q: max %s reached: %d/%d", e.AgentName, e.Kind, e.Actual, e.Limit)
	}
	return fmt.Sprintf("max %s reached: %d/%d", e.Kind, e.Actual, e.Limit)
}

// Unwrap returns the underlying cause error.
// Unwrap 返回底层原因错误。
func (e *StepError) Unwrap() error {
	return e.Cause
}

// StreamError wraps stream errors with reader context.
// StreamError 封装流式错误，附带读取上下文。
type StreamError struct {
	Op      string
	Cause   error
	Message string
}

// NewStreamError creates a StreamError for the given operation with a formatted message.
// NewStreamError 创建 StreamError，指定操作与格式化消息。
func NewStreamError(op string, cause error, format string, args ...any) *StreamError {
	if cause == nil {
		cause = ErrStreamError
	}
	return &StreamError{
		Op:      op,
		Cause:   cause,
		Message: fmt.Sprintf(format, args...),
	}
}

// Error returns the formatted stream error message.
// Error 返回格式化后的流式错误消息。
func (e *StreamError) Error() string {
	return fmt.Sprintf("stream error during %s: %s", e.Op, e.Message)
}

// Unwrap returns the underlying cause error.
// Unwrap 返回底层原因错误。
func (e *StreamError) Unwrap() error {
	return e.Cause
}

// ProtocolError wraps protocol-level errors (e.g. MCP) with operation context.
//
// ProtocolError 封装协议层错误（如 MCP），附带操作上下文。
type ProtocolError struct {
	Protocol string
	Op       string
	Cause    error
	Message  string
}

// NewProtocolError creates a ProtocolError for the given protocol and operation
// with a formatted message.
// NewProtocolError 创建 ProtocolError，指定协议、操作与格式化消息。
func NewProtocolError(protocol, op string, cause error, format string, args ...any) *ProtocolError {
	var baseErr error
	switch protocol {
	case "mcp":
		baseErr = ErrMCPError
	default:
		baseErr = ErrProtocolError
	}
	if cause == nil {
		cause = baseErr
	}
	return &ProtocolError{
		Protocol: protocol,
		Op:       op,
		Cause:    cause,
		Message:  fmt.Sprintf(format, args...),
	}
}

// Error returns the formatted protocol error message.
// Error 返回格式化后的协议错误消息。
func (e *ProtocolError) Error() string {
	return fmt.Sprintf("%s protocol error during %s: %s", e.Protocol, e.Op, e.Message)
}

// Unwrap returns the underlying cause error.
// Unwrap 返回底层原因错误。
func (e *ProtocolError) Unwrap() error {
	return e.Cause
}

// MiddlewareError wraps middleware errors with middleware name context.
// MiddlewareError 封装中间件错误，附带中间件名称上下文。
type MiddlewareError struct {
	Middleware string
	Op         string
	Cause      error
	Message    string
}

// NewMiddlewareError creates a MiddlewareError for the given middleware and
// operation with a formatted message.
// NewMiddlewareError 创建 MiddlewareError，指定中间件、操作与格式化消息。
func NewMiddlewareError(middleware, op string, cause error, format string, args ...any) *MiddlewareError {
	if cause == nil {
		cause = ErrMiddlewareError
	}
	return &MiddlewareError{
		Middleware: middleware,
		Op:         op,
		Cause:      cause,
		Message:    fmt.Sprintf(format, args...),
	}
}

// Error returns the formatted middleware error message.
// Error 返回格式化后的中间件错误消息。
func (e *MiddlewareError) Error() string {
	return fmt.Sprintf("middleware %q error during %s: %s", e.Middleware, e.Op, e.Message)
}

// Unwrap returns the underlying cause error.
// Unwrap 返回底层原因错误。
func (e *MiddlewareError) Unwrap() error {
	return e.Cause
}

// IsNotFoundError reports whether err is a NotFoundError (or wraps one).
// IsNotFoundError 判断 err 是否为 NotFoundError（或包装了它）。
func IsNotFoundError(err error) bool {
	var nfe *NotFoundError
	return errors.As(err, &nfe)
}

// IsInputError reports whether err is an InputError (or wraps one).
// IsInputError 判断 err 是否为 InputError（或包装了它）。
func IsInputError(err error) bool {
	var ie *InputError
	return errors.As(err, &ie)
}

// IsStepError reports whether err is a StepError (or wraps one).
// IsStepError 判断 err 是否为 StepError（或包装了它）。
func IsStepError(err error) bool {
	var se *StepError
	return errors.As(err, &se)
}

// IsStreamError reports whether err is a StreamError (or wraps one).
// IsStreamError 判断 err 是否为 StreamError（或包装了它）。
func IsStreamError(err error) bool {
	var se *StreamError
	return errors.As(err, &se)
}

// IsProtocolError reports whether err is a ProtocolError (or wraps one).
// IsProtocolError 判断 err 是否为 ProtocolError（或包装了它）。
func IsProtocolError(err error) bool {
	var pe *ProtocolError
	return errors.As(err, &pe)
}

// IsMiddlewareError reports whether err is a MiddlewareError (or wraps one).
// IsMiddlewareError 判断 err 是否为 MiddlewareError（或包装了它）。
func IsMiddlewareError(err error) bool {
	var me *MiddlewareError
	return errors.As(err, &me)
}

// ErrorCode 定义 Agent 错误分类码，用于快速判断错误类别。
//
// ErrorCode defines the agent error classification code for quick error category matching.
type ErrorCode string

const (
	// CodeAgentError is the error code for general errors during agent execution.
	// CodeAgentError Agent 执行过程中的通用错误。
	CodeAgentError ErrorCode = "AGENT_ERROR"
	// CodeToolError is the error code for tool call errors.
	// CodeToolError 工具调用相关的错误。
	CodeToolError ErrorCode = "TOOL_ERROR"
	// CodeModelError is the error code for model call errors (e.g. API failures, timeouts).
	// CodeModelError 模型调用相关的错误（如 API 异常、超时）。
	CodeModelError ErrorCode = "MODEL_ERROR"
	// CodeValidationError is the error code for input validation errors.
	// CodeValidationError 输入校验相关的错误。
	CodeValidationError ErrorCode = "VALIDATION_ERROR"
	// CodeRuntimeError is the error code for runtime system-level errors.
	// CodeRuntimeError 运行时系统级错误。
	CodeRuntimeError ErrorCode = "RUNTIME_ERROR"
	// CodeProtocolError is the error code for protocol layer errors (e.g. MCP).
	// CodeProtocolError 协议层错误（MCP 等）。
	CodeProtocolError ErrorCode = "PROTOCOL_ERROR"
	// CodeMiddlewareError is the error code for middleware processing errors.
	// CodeMiddlewareError Middleware 处理中的错误。
	CodeMiddlewareError ErrorCode = "MIDDLEWARE_ERROR"
	// CodeCircuitOpen is the error code for errors raised while the circuit breaker is open.
	// CodeCircuitOpen 熔断器打开状态下的错误。
	CodeCircuitOpen ErrorCode = "CIRCUIT_OPEN"
	// CodeRateLimited is the error code for rate-limited requests.
	// CodeRateLimited 请求被限流时的错误。
	CodeRateLimited ErrorCode = "RATE_LIMITED"
	// CodeTimeout is the error code for operation timeouts.
	// CodeTimeout 操作超时的错误。
	CodeTimeout ErrorCode = "TIMEOUT"
	// CodeNotFound is the error code for missing resources.
	// CodeNotFound 资源未找到的错误。
	CodeNotFound ErrorCode = "NOT_FOUND"
	// CodeCancelled is the error code for cancelled operations.
	// CodeCancelled 操作被取消的错误。
	CodeCancelled ErrorCode = "CANCELLED"
)

// AgentError 封装 Agent 执行过程中的错误，包含错误码、Agent 名称、步骤数和额外详情。
//
// AgentError encapsulates agent execution errors with code, agent name, step count, and details.
type AgentError struct {
	// Code 错误分类码。
	Code ErrorCode
	// Agent 发生错误的 Agent 名称。
	Agent string
	// Step 发生错误时的运行步数。
	Step int
	// Cause 原始错误链。
	Cause error
	// Message 人类可读的错误描述。
	Message string
	// Details 附加的上下文键值对。
	Details map[string]any
}

// NewAgentError 创建 Agent 执行错误，指定错误码、Agent 名称、根因和格式化消息。
//
// NewAgentError creates an agent execution error with code, agent name, cause, and formatted message.
func NewAgentError(code ErrorCode, agent string, cause error, format string, args ...any) *AgentError {
	return &AgentError{
		Code:    code,
		Agent:   agent,
		Cause:   cause,
		Message: fmt.Sprintf(format, args...),
		Details: make(map[string]any),
	}
}

// Error returns the formatted agent error message.
// Error 返回格式化后的 Agent 错误消息。
func (e *AgentError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] agent %q: %s: %v", e.Code, e.Agent, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] agent %q: %s", e.Code, e.Agent, e.Message)
}

// Unwrap returns the underlying cause error.
// Unwrap 返回底层原因错误。
func (e *AgentError) Unwrap() error {
	return e.Cause
}

// WithDetail 向 AgentError 添加一条上下文信息并返回自身（链式调用）。
//
// WithDetail adds a context detail to the AgentError and returns itself for chaining.
func (e *AgentError) WithDetail(key string, value any) *AgentError {
	if e.Details == nil {
		e.Details = make(map[string]any)
	}
	e.Details[key] = value
	return e
}

// IsAgentError 判断 error 是否为 AgentError（或包装了 AgentError）。
//
// IsAgentError reports whether err is an AgentError (or wraps one).
func IsAgentError(err error) bool {
	var ae *AgentError
	return errors.As(err, &ae)
}

// IsErrorCode 判断 error 是否包含指定的 ErrorCode。
//
// IsErrorCode reports whether err contains the specified ErrorCode.
func IsErrorCode(err error, code ErrorCode) bool {
	var ae *AgentError
	if errors.As(err, &ae) {
		return ae.Code == code
	}
	return false
}

// ToolError 封装工具调用中的错误，包含工具名称、参数和原始错误。
//
// ToolError encapsulates tool-call errors with tool name, args, and original cause.
type ToolError struct {
	Code    ErrorCode
	Tool    string
	Args    string
	Cause   error
	Message string
}

// NewToolError 创建工具错误，指定工具名称、根因和格式化消息。
//
// NewToolError creates a tool error with tool name, cause, and formatted message.
func NewToolError(tool string, cause error, format string, args ...any) *ToolError {
	return &ToolError{
		Code:    CodeToolError,
		Tool:    tool,
		Cause:   cause,
		Message: fmt.Sprintf(format, args...),
	}
}

// Error returns the formatted tool error message.
// Error 返回格式化后的工具错误消息。
func (e *ToolError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[TOOL_ERROR] tool %q: %s: %v", e.Tool, e.Message, e.Cause)
	}
	return fmt.Sprintf("[TOOL_ERROR] tool %q: %s", e.Tool, e.Message)
}

// Unwrap returns the underlying cause error.
// Unwrap 返回底层原因错误。
func (e *ToolError) Unwrap() error {
	return e.Cause
}

// IsToolError 判断 error 是否为 ToolError（或包装了 ToolError）。
//
// IsToolError reports whether err is a ToolError (or wraps one).
func IsToolError(err error) bool {
	var te *ToolError
	return errors.As(err, &te)
}

// ModelError 封装模型调用中的错误，包含模型名称、是否可重试和原始错误。
//
// ModelError encapsulates model-call errors with model name, retryable flag, and original cause.
type ModelError struct {
	Code      ErrorCode
	Model     string
	Cause     error
	Message   string
	Retryable bool
}

// NewModelError 创建模型错误，指定模型名称、根因、可重试标志和格式化消息。
//
// NewModelError creates a model error with model name, cause, retryable flag, and formatted message.
func NewModelError(model string, cause error, retryable bool, format string, args ...any) *ModelError {
	return &ModelError{
		Code:      CodeModelError,
		Model:     model,
		Cause:     cause,
		Message:   fmt.Sprintf(format, args...),
		Retryable: retryable,
	}
}

// Error returns the formatted model error message.
// Error 返回格式化后的模型错误消息。
func (e *ModelError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[MODEL_ERROR] model %q: %s (retryable=%v): %v", e.Model, e.Message, e.Retryable, e.Cause)
	}
	return fmt.Sprintf("[MODEL_ERROR] model %q: %s (retryable=%v)", e.Model, e.Message, e.Retryable)
}

// Unwrap returns the underlying cause error.
// Unwrap 返回底层原因错误。
func (e *ModelError) Unwrap() error {
	return e.Cause
}

// IsModelError 判断 error 是否为 ModelError（或包装了 ModelError）。
//
// IsModelError reports whether err is a ModelError (or wraps one).
func IsModelError(err error) bool {
	var me *ModelError
	return errors.As(err, &me)
}

// IsRetryable 判断 error 是否为可重试的模型错误。
//
// IsRetryable reports whether err is a retryable model error.
func IsRetryable(err error) bool {
	var me *ModelError
	if errors.As(err, &me) {
		return me.Retryable
	}
	return false
}

// ValidationError 表示单个字段的校验失败，包含字段名、值、校验规则和描述。
//
// ValidationError represents a single field validation failure.
type ValidationError struct {
	Field   string
	Value   any
	Rule    string
	Message string
}

// NewValidationError 创建字段校验错误，指定字段名、值、违反的规则和描述。
//
// NewValidationError creates a field validation error with field, value, rule, and message.
func NewValidationError(field string, value any, rule string, message string) *ValidationError {
	return &ValidationError{
		Field:   field,
		Value:   value,
		Rule:    rule,
		Message: message,
	}
}

// Error returns the formatted validation error message.
// Error 返回格式化后的校验错误消息。
func (e *ValidationError) Error() string {
	return fmt.Sprintf("[VALIDATION_ERROR] field %q (rule=%s): %s", e.Field, e.Rule, e.Message)
}

// IsValidationError 判断 error 是否为 ValidationError（或包装了 ValidationError）。
//
// IsValidationError reports whether err is a ValidationError (or wraps one).
func IsValidationError(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

// ValidationErrors 收集多个字段的校验失败，实现 error 接口。
//
// ValidationErrors collects multiple field validation failures and implements the error interface.
type ValidationErrors struct {
	Errors []*ValidationError
}

// NewValidationErrors 创建空的批量校验错误容器。
//
// NewValidationErrors creates an empty validation errors container.
func NewValidationErrors() *ValidationErrors {
	return &ValidationErrors{Errors: make([]*ValidationError, 0)}
}

// Add 向批量校验错误中添加一个字段错误。
//
// Add appends a field validation error to the collection.
func (ve *ValidationErrors) Add(err *ValidationError) {
	ve.Errors = append(ve.Errors, err)
}

// Error returns the joined validation failure messages, or "validation failed"
// when the collection is empty.
// Error 返回合并后的校验失败消息；集合为空时返回 "validation failed"。
func (ve *ValidationErrors) Error() string {
	if len(ve.Errors) == 0 {
		return "validation failed"
	}
	var msg string
	for i, e := range ve.Errors {
		if i > 0 {
			msg += "; "
		}
		msg += e.Error()
	}
	return msg
}

// HasErrors 判断批量校验错误中是否包含任何字段错误。
//
// HasErrors reports whether the collection contains any field errors.
func (ve *ValidationErrors) HasErrors() bool {
	return len(ve.Errors) > 0
}

// IsValidationErrors 判断 error 是否为 ValidationErrors（或包装了 ValidationErrors）。
//
// IsValidationErrors reports whether err is a ValidationErrors (or wraps one).
func IsValidationErrors(err error) bool {
	var ves *ValidationErrors
	return errors.As(err, &ves)
}

// CircuitError 表示熔断器开启状态下的错误，包含被熔断的路由名。
//
// CircuitError represents a circuit-breaker-open error with the affected route name.
type CircuitError struct {
	Route   string
	Message string
}

// NewCircuitError 创建熔断器错误，指定被熔断的路由名。
//
// NewCircuitError creates a circuit breaker error for the given route.
func NewCircuitError(route string) *CircuitError {
	return &CircuitError{
		Route:   route,
		Message: fmt.Sprintf("circuit breaker is open for route %q", route),
	}
}

// Error returns the formatted circuit-open error message.
// Error 返回格式化后的熔断错误消息。
func (e *CircuitError) Error() string {
	return fmt.Sprintf("[CIRCUIT_OPEN] %s", e.Message)
}

// Unwrap exposes the ErrCircuitOpen sentinel so errors.Is-based
// classification agrees with IsCircuitError.
// Unwrap 暴露 ErrCircuitOpen 哨兵错误，使基于 errors.Is 的分类
// 与 IsCircuitError 保持一致。
func (e *CircuitError) Unwrap() error { return ErrCircuitOpen }

// IsCircuitError 判断 error 是否为 CircuitError（或包装了 CircuitError）。
//
// IsCircuitError reports whether err is a CircuitError (or wraps one).
func IsCircuitError(err error) bool {
	var ce *CircuitError
	return errors.As(err, &ce)
}

// RateLimitError 表示请求被限流，包含路由名和建议等待时间。
//
// RateLimitError represents a rate-limited request with route name and suggested retry interval.
type RateLimitError struct {
	Route      string
	RetryAfter int
	Message    string
}

// NewRateLimitError 创建限流错误，指定路由名和建议等待秒数。
//
// NewRateLimitError creates a rate limit error with route name and suggested retry seconds.
func NewRateLimitError(route string, retryAfter int) *RateLimitError {
	return &RateLimitError{
		Route:      route,
		RetryAfter: retryAfter,
		Message:    fmt.Sprintf("rate limited for route %q, retry after %ds", route, retryAfter),
	}
}

// Error returns the formatted rate-limit error message.
// Error 返回格式化后的限流错误消息。
func (e *RateLimitError) Error() string {
	return fmt.Sprintf("[RATE_LIMITED] %s", e.Message)
}

// Unwrap exposes the ErrRateLimited sentinel so errors.Is-based
// classification agrees with IsRateLimitError.
// Unwrap 暴露 ErrRateLimited 哨兵错误，使基于 errors.Is 的分类
// 与 IsRateLimitError 保持一致。
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// IsRateLimitError 判断 error 是否为 RateLimitError（或包装了 RateLimitError）。
//
// IsRateLimitError reports whether err is a RateLimitError (or wraps one).
func IsRateLimitError(err error) bool {
	var re *RateLimitError
	return errors.As(err, &re)
}

// WrapError 将错误包装为带错误码格式的 error，如果 cause 不为 nil 则用 %w 包装。
//
// WrapError wraps an error with a code-formatted message; uses %w when cause is non-nil.
func WrapError(code ErrorCode, cause error, format string, args ...any) error {
	if cause == nil {
		return fmt.Errorf(format, args...)
	}
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), cause)
}

// IsNotFound 判断 error 是否为任意"未找到"类错误（Agent、Tool、Model、Session 等）。
//
// IsNotFound reports whether err is any kind of "not found" error.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrAgentNotFound) ||
		errors.Is(err, ErrToolNotFound) ||
		errors.Is(err, ErrModelNotFound) ||
		errors.Is(err, ErrSessionNotFound) ||
		errors.Is(err, ErrCheckpointNotFound) ||
		errors.Is(err, ErrRegistryNotFound)
}

// IsCancelled 判断 error 是否为取消类错误（context canceled / deadline exceeded）。
//
// IsCancelled reports whether err is a cancellation error (context canceled / deadline exceeded).
func IsCancelled(err error) bool {
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}
