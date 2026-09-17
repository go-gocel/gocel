// Package timecontext injects the current time into each step's message
// stream (DSH time-context): the model learns the wall clock of the
// machine running the harness without guessing. The injection is a
// lightweight system message at OnMessagesBuilt — a durable, replayable
// fact in the conversation, not an ephemeral side channel.
//
// Package timecontext 在每步消息流中注入当前时间（DSH time-context）：
// 模型获知运行 harness 的机器墙钟而无需猜测。注入是 OnMessagesBuilt 处
// 的轻量系统消息——对话中持久、可重放的事实，而非临时旁路。
package timecontext

import (
	"context"
	"fmt"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// Option configures the module.
// Option 配置模块。
type Option func(*Module)

// WithLocation pins the time zone the injected time renders in. The
// default is the machine's local zone. An explicit location makes the
// output deterministic across hosts.
//
// WithLocation 固定注入时间的渲染时区。默认使用机器本地时区。
// 显式指定位置可使输出跨主机确定。
func WithLocation(loc *time.Location) Option {
	return func(m *Module) { m.loc = loc }
}

// WithFormat overrides the time rendering format (time.RFC3339 default).
// WithFormat 覆盖时间渲染格式（默认 time.RFC3339）。
func WithFormat(f string) Option {
	return func(m *Module) { m.format = f }
}

// Module injects the current time into the message stream before each run.
// Module 在每次运行前把当前时间注入消息流。
type Module struct {
	loc    *time.Location
	format string
}

// New creates the module with the machine's local zone.
// New 以机器本地时区创建模块。
func New(opts ...Option) *Module {
	m := &Module{loc: time.Local, format: time.RFC3339}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Register implements kernel.Module.
//
// Register 实现 kernel.Module：注册 OnMessagesBuilt 钩子，把当前时间作为
// 系统消息追加到消息流。
func (m *Module) Register(rt kernel.HookRegistrar) {
	rt.OnMessagesBuilt(func(ctx context.Context, msgs []*types.Message) (context.Context, []*types.Message, error) {
		now := time.Now().In(m.loc).Format(m.format)
		// 追加系统消息，由 FireMessagesBuilt 统一合并进 system。
		return ctx, append(msgs, types.NewSystemMessage(fmt.Sprintf("Current time: %s", now))), nil
	})
}
