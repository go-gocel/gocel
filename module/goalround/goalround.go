// Package goalround is the round driver for durable goals: it claims
// rounds under the manager's CAS, injects the goal context into each round,
// marks host-initiated runs (host authority for goal tools), and settles
// the goal with a completion notice. The mechanics follow DSH's
// goal-round-driver:
//
//   - Activation ("armed") is process-local: a restart, resume, or forked
//     child never auto-continues — the consumer must arm explicitly.
//   - Round claiming is reserve-then-admit: StartRound increments under
//     CAS at OnAgentStart, and any claim failure disarms the goal with a
//     failure notice — fail-closed, never an implicit retry.
//   - Host turns: the first run of an armed goal is host-initiated
//     (host_turn state = true); continuation rounds are goal rounds
//     (host_turn = false). The goal tools read that state for their
//     authority lines.
//
// Package goalround 是持久化目标的轮次驱动器：在 manager 的 CAS 下认领
// 轮次、向每轮注入目标上下文、标记宿主发起运行（goal 工具的宿主权限）、
// 并以完成通知了结目标。机制照搬 DSH 的 goal-round-driver：
//
//   - 激活（armed）是进程内状态：重启、恢复或 fork 的子代理绝不自动
//     续跑——消费方必须显式 arm。
//   - 轮次认领是先预留后准入：OnAgentStart 时 StartRound 在 CAS 下递增，
//     认领失败即以失败通知 disarm——fail-closed，绝不隐式重试。
//   - 宿主轮次：目标的首个运行由宿主发起（host_turn=true）；续跑轮次是
//     目标轮次（host_turn=false）。goal 工具读取该状态执行双权限线。
package goalround

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/goal"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// HostTurnKey is the shared run-state key marking host-initiated runs;
// tools/goal reads the same key.
//
// HostTurnKey 是标记宿主发起运行的共享运行状态键；tools/goal 读取同一键。
const HostTurnKey = "goal:host_turn"

type armedState struct {
	goalID  string
	round   int  // claimed at OnAgentStart; 0 = not yet claimed
	hostRun bool // true for the first (host-initiated) run
}

// Module drives goal rounds for armed sessions.
//
// Module 驱动已激活（armed）会话的目标轮次。
type Module struct {
	mu    sync.Mutex
	mgr   *goal.Manager
	armed map[string]*armedState // sessionID → state
	// fixedSessionID pins the session identity for consumers that do not
	// populate RuntimeFacts.SessionID (examples, single-session products).
	fixedSessionID string
}

// Option configures the driver.
//
// Option 配置轮次驱动器。
type Option func(*Module)

// WithSessionID pins the session identity used for arming. The default
// reads RuntimeFacts.SessionID from the AgentContext.
//
// WithSessionID 固定用于激活（arming）的会话身份；默认从 AgentContext 读取
// RuntimeFacts.SessionID。
func WithSessionID(sessionID string) Option {
	return func(m *Module) { m.fixedSessionID = sessionID }
}

// New builds the driver over a goal manager.
//
// New 在目标管理器之上构建轮次驱动器。
func New(mgr *goal.Manager, opts ...Option) *Module {
	m := &Module{mgr: mgr, armed: make(map[string]*armedState)}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// sessionOf resolves the session identity: the pinned id wins, then the
// AgentContext facts.
func (m *Module) sessionOf(ac kernel.AgentContext) string {
	if ac == nil {
		return ""
	}
	if m.fixedSessionID != "" {
		return m.fixedSessionID
	}
	return ac.Facts().SessionID
}

// Arm marks goalID for auto-continuation in this session. The next run
// claims round 1 as a host turn. Arming is idempotent.
//
// Arm 将 goalID 标记为当前会话的自动续跑目标；下一次运行以宿主轮次认领
// 第 1 轮。重复调用是幂等的。
func (m *Module) Arm(sessionID, goalID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.armed[sessionID]; ok {
		return
	}
	m.armed[sessionID] = &armedState{goalID: goalID, hostRun: true}
}

// Disarm removes the armed goal for the session; a no-op when none is armed.
//
// Disarm 移除会话的已激活目标；未激活时为空操作。
func (m *Module) Disarm(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.armed, sessionID)
}

// ShouldContinue reports whether the session's armed goal wants another
// round: the goal is still active and the round cap allows one more claim.
// The consumer re-runs the agent; the next OnAgentStart claims the round.
//
// ShouldContinue 报告会话的已激活目标是否还需要一轮：目标仍处于激活状态且
// 轮次上限允许再认领一次。消费方据此重新运行代理，下一次 OnAgentStart 认领轮次。
func (m *Module) ShouldContinue(ctx context.Context, sessionID string) (goalID string, ok bool) {
	m.mu.Lock()
	st := m.armed[sessionID]
	m.mu.Unlock()
	if st == nil {
		return "", false
	}
	g, err := m.mgr.Get(ctx, st.goalID)
	if err != nil || g.Phase != goal.PhaseActive {
		return "", false
	}
	if g.MaxRounds > 0 && g.Rounds >= g.MaxRounds {
		return "", false
	}
	return st.goalID, true
}

// Register registers the goal-round hooks (OnAgentStart, OnMessagesBuilt,
// OnAgentEnd) on the runtime.
//
// Register 在运行时上注册目标轮次钩子（OnAgentStart / OnMessagesBuilt / OnAgentEnd）。
func (m *Module) Register(rt kernel.HookRegistrar) {
	rt.OnAgentStart(func(ctx context.Context, info *kernel.AgentRunInfo) (context.Context, *kernel.AgentRunInfo, error) {
		ac := kernel.GetAgentContext(ctx)
		if ac == nil || ac.State() == nil {
			return ctx, info, nil
		}
		sessionID := m.sessionOf(ac)
		if sessionID == "" {
			// Fail-closed: without a session identity the driver cannot
			// track arms, so it stays inert.
			return ctx, info, nil
		}
		m.mu.Lock()
		st := m.armed[sessionID]
		m.mu.Unlock()

		state := ac.State()
		if st == nil {
			// Ordinary run without an armed goal: host authority by default.
			state.Set(HostTurnKey, true)
			return ctx, info, nil
		}

		// Reserve-then-admit: claim under CAS; any failure disarms.
		g, err := m.mgr.StartRound(ctx, st.goalID)
		if err != nil {
			m.Disarm(sessionID)
			m.notify(ac, st.goalID, "failed", err.Error())
			state.Set(HostTurnKey, false)
			return ctx, info, nil
		}
		m.mu.Lock()
		st.round = g.Rounds
		host := st.hostRun
		st.hostRun = false
		m.mu.Unlock()
		state.Set(HostTurnKey, host)
		state.Set("goal:active_id", st.goalID)
		state.Set("goal:round", g.Rounds)
		return ctx, info, nil
	})

	rt.OnMessagesBuilt(func(ctx context.Context, msgs []*types.Message) (context.Context, []*types.Message, error) {
		ac := kernel.GetAgentContext(ctx)
		if ac == nil {
			return ctx, msgs, nil
		}
		sessionID := m.sessionOf(ac)
		m.mu.Lock()
		st := m.armed[sessionID]
		m.mu.Unlock()
		if st == nil || st.round == 0 {
			return ctx, msgs, nil
		}
		g, err := m.mgr.Get(ctx, st.goalID)
		if err != nil {
			return ctx, msgs, nil
		}
		block := goalBlock(g, st.round)
		// 追加系统消息，由 FireMessagesBuilt 统一合并进 system。
		return ctx, append(msgs, types.NewSystemMessage(block)), nil
	})

	rt.OnAgentEnd(func(ctx context.Context, info *kernel.RunInfo) (context.Context, *kernel.RunInfo, error) {
		ac := kernel.GetAgentContext(ctx)
		if ac == nil {
			return ctx, info, nil
		}
		sessionID := m.sessionOf(ac)
		m.mu.Lock()
		st := m.armed[sessionID]
		m.mu.Unlock()
		if st == nil {
			return ctx, info, nil
		}
		g, err := m.mgr.Get(ctx, st.goalID)
		if err != nil {
			m.Disarm(sessionID)
			m.notify(ac, st.goalID, "failed", err.Error())
			return ctx, info, nil
		}
		switch g.Phase {
		case goal.PhaseActive:
			if g.MaxRounds > 0 && g.Rounds >= g.MaxRounds {
				m.Disarm(sessionID)
				m.notify(ac, st.goalID, "rounds_exhausted", fmt.Sprintf("cap of %d rounds reached", g.MaxRounds))
			}
			// else: stay armed; the consumer consults ShouldContinue.
		case goal.PhaseComplete, goal.PhaseBlocked, goal.PhasePaused:
			m.Disarm(sessionID)
			m.notify(ac, st.goalID, string(g.Phase), g.BlockerReason)
		}
		return ctx, info, nil
	})
}

// notify sends a goal-round settlement notice through the run's event
// channel; a nil sender drops it.
func (m *Module) notify(ac kernel.AgentContext, goalID, status, detail string) {
	if ac == nil || ac.SendEvent() == nil {
		return
	}
	ac.SendEvent()(types.NoticeEvent(&types.Notice{
		Kind:   types.NoticeKindGoalRound,
		ID:     goalID,
		Status: status,
		Label:  detail,
		At:     time.Now(),
	}))
}

// goalBlock renders the injected goal context of one round.
func goalBlock(g *goal.Goal, round int) string {
	capText := "unlimited"
	if g.MaxRounds > 0 {
		capText = fmt.Sprintf("%d", g.MaxRounds)
	}
	return fmt.Sprintf(`## Goal (round %d of %s)
Objective: %s
Report completion with update_goal(action="complete"). Report a blocker only via update_goal(action="block", reason="...") and only when the same blocker already persisted — a fresh goal cannot be blocked by a round.`, round, capText, g.Objective)
}
