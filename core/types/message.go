// ❄️ FROZEN — Stable data contract. Types, fields and semantics must not change.
//
// Package types defines the core data types used throughout gocel.
// This package has zero external dependencies.
package types

import (
	"crypto/rand"
	"fmt"
	mathrand "math/rand"
	"unicode/utf8"
)

// Role represents the role of a message participant (system/user/assistant/tool).
//
// Role 表示消息参与者的角色（系统/用户/助手/工具）。
type Role string

const (
	// RoleSystem is the role of system or instruction messages.
	// RoleSystem 系统或指令消息的角色。
	RoleSystem    Role = "system"
	// RoleUser is the role of user messages.
	// RoleUser 用户消息的角色。
	RoleUser      Role = "user"
	// RoleAssistant is the role of assistant messages.
	// RoleAssistant 助手消息的角色。
	RoleAssistant Role = "assistant"
	// RoleTool is the role of tool result messages.
	// RoleTool 工具结果消息的角色。
	RoleTool      Role = "tool"
)

// ToolCallFunction represents a function call requested by the model.
//
// ToolCallFunction 表示模型请求的函数调用。
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// ToolCall represents a tool call requested by the model.
//
// ToolCall 表示模型请求的工具调用。
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // "function"
	Function ToolCallFunction `json:"function"`
	Index    *int             `json:"index,omitempty"` // for streaming chunks
}

// Message is the fundamental communication unit in a conversation.
//
// Message 是对话中的基本通信单元。
type Message struct {
	Role             Role          `json:"role"`
	Content          string        `json:"content"`
	ContentParts     []ContentPart `json:"content_parts,omitempty"`
	ReasoningContent string        `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID       string        `json:"tool_call_id,omitempty"`
	ToolName         string        `json:"tool_name,omitempty"`

	// Meta carries optional extension data (not serialized to LLM).
	Meta map[string]any `json:"-"`
}

// NewSystemMessage creates a system message.
// NewSystemMessage 创建系统消息。
func NewSystemMessage(content string) *Message {
	return &Message{Role: RoleSystem, Content: content}
}

// NewUserMessage creates a user message.
// NewUserMessage 创建用户消息。
func NewUserMessage(content string) *Message {
	return &Message{Role: RoleUser, Content: content}
}

// NewAssistantMessage creates an assistant message.
// NewAssistantMessage 创建助手消息。
func NewAssistantMessage(content string) *Message {
	return &Message{Role: RoleAssistant, Content: content}
}

// NewToolMessage creates a tool result message.
// NewToolMessage 创建工具结果消息。
func NewToolMessage(content, toolCallID, toolName string) *Message {
	return &Message{
		Role:       RoleTool,
		Content:    content,
		ToolCallID: toolCallID,
		ToolName:   toolName,
	}
}

// TokenUsage tracks token consumption for an API call.
// TokenUsage 记录 API 调用的 token 用量。
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// SessionIDKey is the metadata key for looking up an existing session.
// SessionIDKey 是用于查找现有会话的元数据键。
const SessionIDKey = "session_id"

// SessionID generates a unique session identifier using crypto/rand.
// Falls back to math/rand uint64 on read failure.
//
// SessionID 使用 crypto/rand 生成唯一会话标识符。
func SessionID() string {
	b := make([]byte, 8)
	_, err := rand.Read(b)
	if err != nil {
		return fmt.Sprintf("sess_%x", mathrand.Uint64())
	}
	return fmt.Sprintf("sess_%x", b)
}

// CloneMessage creates a deep copy of a Message.
// CloneMessage 创建 Message 的深拷贝。
func CloneMessage(m *Message) *Message {
	if m == nil {
		return nil
	}
	tcs := make([]ToolCall, len(m.ToolCalls))
	for i, tc := range m.ToolCalls {
		tcs[i] = tc
		if tc.Index != nil {
			idx := *tc.Index
			tcs[i].Index = &idx
		}
	}
	meta := make(map[string]any, len(m.Meta))
	for k, v := range m.Meta {
		meta[k] = v
	}
	return &Message{
		Role:             m.Role,
		Content:          m.Content,
		ContentParts:     cloneContentParts(m.ContentParts),
		ReasoningContent: m.ReasoningContent,
		ToolCalls:        tcs,
		ToolCallID:       m.ToolCallID,
		ToolName:         m.ToolName,
		Meta:             meta,
	}
}

// CloneMessages creates a deep copy of a message slice.
// CloneMessages 创建消息切片的深拷贝。
func CloneMessages(msgs []*Message) []*Message {
	if msgs == nil {
		return nil
	}
	result := make([]*Message, len(msgs))
	for i, m := range msgs {
		result[i] = CloneMessage(m)
	}
	return result
}

// EstimateTokens provides a rough token estimation for messages.
// Uses ~4 chars per token for ASCII, ~1 char per token for CJK as a conservative estimate.
// EstimateTokens 对消息做粗略的 token 估算：保守估计 ASCII 约 4 字符/token、
// CJK 约 1 字符/token。
func EstimateTokens(msgs []*Message) int {
	var total int
	for _, m := range msgs {
		if m == nil {
			continue
		}
		if len(m.ContentParts) > 0 {
			for _, p := range m.ContentParts {
				switch p.Type {
				case ContentTypeText:
					total += weightedRuneCount(p.Text)
				case ContentTypeImageURL:
					total += weightedRuneCount(p.ImageURL) + 50 // URL overhead
				case ContentTypeImageData:
					total += len(p.ImageData.Data) / 4 // rough base64 → token
				default:
					total += 50
				}
			}
		} else {
			total += weightedRuneCount(m.Content)
		}
		total += weightedRuneCount(m.ReasoningContent)
		for _, tc := range m.ToolCalls {
			total += weightedRuneCount(tc.Function.Name) + weightedRuneCount(tc.Function.Arguments)
		}
		total += 12 // per-message overhead
	}
	if total == 0 {
		return 0
	}
	return total / 4
}

// weightedRuneCount estimates effective characters accounting for CJK density.
// CJK characters (CJK Unified Ideographs U+4E00–U+9FFF, plus extensions)
// typically consume more token budget; we weight them at 4× to compensate.
func weightedRuneCount(s string) int {
	total := utf8.RuneCountInString(s)
	// For every CJK rune, add 3 extra to the numerator
	// (1 base + 3 extra = 4 weight, so each CJK char ≈ ~1 token after /4)
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			total += 3
		}
	}
	return total
}
