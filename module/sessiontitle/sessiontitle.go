// Package sessiontitle derives a session title from the session log (DSH
// session-title): the title is a log-backed fact — session/title events
// fold last-wins, so rename, restart, and resume recover the same title.
//
// Two strategies:
//
//   - Deterministic fallback: the first qualified human message's leading
//     words (whitespace-normalized, bounded bytes, never split a rune).
//   - Optional LLM provider: a model-backed generator may refine the title;
//     a provider failure keeps the fallback (the run never fails because
//     titling failed).
//
// The title stays out of the model context entirely — it is a consumer
// read model (session lists, exports), never a prompt contribution.
//
// Package sessiontitle 从会话日志派生会话标题（DSH session-title）：标题是
// 日志背书的事实——session/title 事件 last-wins 折叠，重命名、重启、恢复
// 得到同一标题。
//
// 两种策略：
//
//   - 确定性回退：首个合格人类消息的前若干词（空白归一、字节有界、绝不
//     切断码点）。
//   - 可选 LLM 提供者：模型支持的生成器可精炼标题；提供者失败保留回退
//     （运行绝不因标题失败而失败）。
//
// 标题完全不进入模型上下文——它是消费方读模型（会话列表、导出），
// 绝非提示词贡献。
package sessiontitle

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	coresession "github.com/go-gocel/gocel/core/session"
	"github.com/go-gocel/gocel/core/types"
)

// TitleProvider refines a session title with a model. It receives the
// fallback title and the session's qualified messages; it returns the
// refined title. A failure keeps the fallback.
//
// TitleProvider 用模型精炼会话标题。它接收回退标题与会话的合格消息；
// 返回精炼标题。失败保留回退。
type TitleProvider interface {
	// Refine produces the refined title.
	Refine(ctx context.Context, fallback string, msgs []*types.Message) (string, error)
}

// Config wires the title service to the session log.
// Config 把标题服务与会话日志接通。
type Config struct {
	// Log is the session event log the title persists in.
	Log *coresession.Log
	// Provider optionally refines titles with a model. Nil = deterministic
	// fallback only.
	Provider TitleProvider
	// MaxFallbackBytes bounds the deterministic fallback (default 80).
	MaxFallbackBytes int
	// MaxFallbackWords bounds the fallback word count (default 8).
	MaxFallbackWords int
}

// Service derives and persists the session title.
// Service 派生并持久化会话标题。
type Service struct {
	cfg Config
}

// New creates the title service. A nil log fails loudly — the title is a
// log-backed fact.
//
// New 创建标题服务。nil 日志显式报错——标题是日志背书的事实。
func New(cfg Config) (*Service, error) {
	if cfg.Log == nil {
		return nil, fmt.Errorf("sessiontitle: nil log")
	}
	if cfg.MaxFallbackBytes <= 0 {
		cfg.MaxFallbackBytes = 80
	}
	if cfg.MaxFallbackWords <= 0 {
		cfg.MaxFallbackWords = 8
	}
	return &Service{cfg: cfg}, nil
}

// Get returns the current title: the log-backed one when set, otherwise
// the deterministic fallback derived from the log's qualified messages.
//
// Get 返回当前标题：已设置时返回日志背书标题，否则返回从日志合格消息
// 派生的确定性回退。
func (s *Service) Get() string {
	if t := s.fold(); t != "" {
		return t
	}
	return s.fallback()
}

// Rename sets a user-pinned title durably in the log (it survives restarts
// and is never auto-refined again).
//
// Rename 在日志中持久化用户钉住的标题（重启后仍存在，且不再自动精炼）。
func (s *Service) Rename(title string) error {
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("sessiontitle: empty title")
	}
	return s.set(title)
}

// Auto derives and persists the title: the fallback first, then the
// provider's refinement when configured. A provider failure keeps the
// fallback. A title already pinned by Rename is left untouched.
//
// Auto 派生并持久化标题：先回退，再（配置时）用提供者精炼。提供者失败
// 保留回退。Rename 钉住的标题不被改动。
func (s *Service) Auto(ctx context.Context) (string, error) {
	if t := s.fold(); t != "" {
		return t, nil // already set (pinned or previously auto)
	}
	fallback := s.fallback()
	title := fallback
	if s.cfg.Provider != nil {
		if refined, err := s.cfg.Provider.Refine(ctx, fallback, s.qualifiedMessages()); err == nil && strings.TrimSpace(refined) != "" {
			title = strings.TrimSpace(refined)
		}
	}
	if err := s.set(title); err != nil {
		return "", err
	}
	return title, nil
}

// set appends a session/title log-only event carrying the whole title
// (last-wins whole-value).
func (s *Service) set(title string) error {
	_, err := s.cfg.Log.Append(types.NewLogOnlyEvent("session/title", map[string]any{"title": title}))
	return err
}

// fold derives the log-backed title (last-wins).
func (s *Service) fold() string {
	var title string
	for _, ev := range s.cfg.Log.Events() {
		if ev.Kind != "session/title" || ev.Meta == nil {
			continue
		}
		if v, ok := ev.Meta["title"].(string); ok {
			title = v
		}
	}
	return title
}

// fallback derives the deterministic title from the first qualified human
// message: whitespace-normalized, bounded by words and bytes, never
// splitting a rune.
func (s *Service) fallback() string {
	msgs := s.qualifiedMessages()
	if len(msgs) == 0 {
		return ""
	}
	text := normalize(msgs[0].Content)
	if text == "" {
		return ""
	}
	words := strings.Fields(text)
	if len(words) > s.cfg.MaxFallbackWords {
		words = words[:s.cfg.MaxFallbackWords]
	}
	out := strings.Join(words, " ")
	// Byte-bounded, never split a rune: step back from the bound until the
	// byte at the cut is a rune start (or the string is empty).
	if len(out) > s.cfg.MaxFallbackBytes {
		cut := s.cfg.MaxFallbackBytes
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut]
	}
	return out
}

// qualifiedMessages returns the log's user messages in order (the fallback
// and provider input surface — DSH direct-user projection).
func (s *Service) qualifiedMessages() []*types.Message {
	var out []*types.Message
	for _, ev := range s.cfg.Log.Events() {
		if ev.Kind == types.SessionEventUserMessage && ev.Message != nil {
			out = append(out, ev.Message)
		}
	}
	return out
}

// normalize collapses whitespace and trims the message text.
func normalize(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}
