package session

import (
	"context"
	"log"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// SessionObserver is a Module that persists agent run results to a Session.
//
// SessionObserver 是一个 Module，把 Agent 运行结果持久化到 Session。
type SessionObserver struct {
	svc SessionService
}

// NewSessionObserver creates a Module that persists agent run results to a Session.
//
// NewSessionObserver 创建将 Agent 运行结果持久化到 Session 的 Module。
func NewSessionObserver(svc SessionService) *SessionObserver {
	if svc == nil {
		log.Print("[session] NewSessionObserver called with nil service — persistence disabled")
	}
	return &SessionObserver{svc: svc}
}

// Register implements kernel.Module: hooks OnRunComplete to the agent-end
// hook.
//
// Register 实现 kernel.Module：把 OnRunComplete 挂到 Agent 结束钩子上。
func (o *SessionObserver) Register(rt kernel.HookRegistrar) {
	rt.OnAgentEnd(o.OnRunComplete)
}

// OnRunComplete persists a finished run: it loads or creates the run's
// session, records the status (completed or error), appends the new
// messages and token usage, then saves.
//
// OnRunComplete 持久化一次已完成的运行：加载或创建该运行的会话、记录状态
// （completed 或 error）、追加新增消息与 token 用量，最后保存。
func (o *SessionObserver) OnRunComplete(ctx context.Context, info *kernel.RunInfo) (context.Context, *kernel.RunInfo, error) {
	if o == nil || o.svc == nil {
		return ctx, info, nil
	}

	sessionID := ""
	if info.Input != nil && info.Input.Meta != nil {
		if sid, ok := info.Input.Meta[types.SessionIDKey].(string); ok && sid != "" {
			sessionID = sid
		}
	}

	var sess Session
	if sessionID != "" {
		if loaded, err := o.svc.Get(ctx, sessionID); err == nil && loaded != nil {
			sess = loaded
		}
	}
	if sess == nil {
		var createErr error
		sess, createErr = o.svc.Create(ctx, info.AgentName, "", nil)
		if createErr != nil || sess == nil {
			return ctx, info, nil
		}
	}

	if info.Err != nil {
		sess.SetStatus("error")
	} else {
		sess.SetStatus("completed")
	}

	inputMsgCount := 0
	if info.Input != nil {
		inputMsgCount = len(info.Input.Messages)
	}
	if len(info.AllMsgs) > inputMsgCount {
		sess.AddMessages(info.AllMsgs[inputMsgCount:])
	}
	if info.Result != nil && info.Result.TokenUsage != nil {
		sess.AddTokenUsage(info.Result.TokenUsage.TotalTokens)
	}

	_ = o.svc.Save(ctx, sess)
	return ctx, info, nil
}

var _ kernel.Module = (*SessionObserver)(nil)
