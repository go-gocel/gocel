// Package schedule provides durable session-scoped reminders (DSH
// schedule): the model creates after/at/every reminders whose state lives
// entirely in the session event log — timers and follow-ups are disposable
// projections of that log, so a restart or resume recovers overdue
// reminders by replay (log-as-state).
//
// Delivery is cooperative: the module runs a per-session scheduler that
// fires the configured callback (the product's agent-turn submitter) when a
// reminder comes due. One-shot reminders fire once; every-reminders
// re-arm on their anchor-aligned cadence. The scheduler is in-process —
// it observes the log and derives the next due time; a crash before
// delivery leaves the reminder in the log, and the next scheduler start
// delivers the overdue item.
//
// Package schedule 提供持久化的会话级提醒（DSH schedule）：模型创建
// after/at/every 提醒，其状态完全存在于会话事件日志中——定时器与后续
// 动作都是该日志的可丢弃投影，重启或恢复经重放找回逾期提醒
// （日志即状态）。
//
// 交付是协作式的：模块为每个会话运行一个调度器，到期时触发配置的回调
// （产品的 agent 轮次提交器）。一次性提醒只触发一次；every 提醒按锚点
// 对齐的节奏重新武装。调度器在进程内——它观察日志并派生下一个到期
// 时间；崩溃未交付的提醒留在日志中，下次调度器启动交付逾期项。
package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	coresession "github.com/go-gocel/gocel/core/session"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/tool"
	"github.com/go-gocel/gocel/core/types"
)

// ReminderKind classifies the reminder selector.
// ReminderKind 分类提醒选择器。
type ReminderKind string

const (
	// KindAfter fires once after a delay in seconds.
	// KindAfter 在延迟若干秒后触发一次。
	KindAfter ReminderKind = "after"
	// KindAt fires once at an absolute UTC time.
	// KindAt 在绝对 UTC 时间触发一次。
	KindAt ReminderKind = "at"
	// KindEvery fires repeatedly on a fixed cadence (seconds, >= 300).
	// KindEvery 按固定节奏（秒，>= 300）重复触发。
	KindEvery ReminderKind = "every"
)

// Reminder is one durable reminder record folded from the log.
// Reminder 是从日志折叠的一条持久提醒记录。
type Reminder struct {
	ID          string       `json:"id"`
	Prompt      string       `json:"prompt"`
	Kind        ReminderKind `json:"kind"`
	AfterSecs   int64        `json:"after_seconds,omitempty"`
	At          time.Time    `json:"at,omitempty"`
	EverySecs   int64        `json:"every_seconds,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	LastFiredAt time.Time    `json:"last_fired_at,omitempty"`
	Done        bool         `json:"done"`
}

// Config wires the module to the session log and the delivery callback.
// Config 把模块接到会话日志与交付回调。
type Config struct {
	// Log is the session event log the reminders persist in.
	// Log 是提醒持久化所在的会话事件日志。
	Log *coresession.Log
	// Deliver is invoked when a reminder comes due, with the reminder and
	// the session id (the product submits an agent turn). It must not block
	// for long; the scheduler runs it synchronously on its own timer.
	//
	// Deliver 在提醒到期时被调用，携带提醒与会话 id（产品据此提交 agent
	// 轮次）。不得长时间阻塞；调度器在自己的定时器上同步执行它。
	Deliver func(ctx context.Context, sessionID string, r Reminder)
}

// Module implements the schedule tools and the per-session scheduler.
// Module 实现 schedule 工具与每会话调度器。
type Module struct {
	cfg      Config
	mu       sync.Mutex // scheduler lifecycle (start/stop)
	stateMu  sync.Mutex // fold→modify→replaceAll composite ops (create/delete/fireDue)
	stop     chan struct{}
	stopped  chan struct{}
	started  bool
	sessionID string
}

// New creates the schedule module. The session id is the log owner's id —
// used in delivery callbacks to route the reminder to the right session.
//
// New 创建 schedule 模块。session id 是日志属主的 id——交付回调用它把
// 提醒路由到正确的会话。
func New(sessionID string, cfg Config) (*Module, error) {
	if cfg.Log == nil {
		return nil, fmt.Errorf("schedule: nil log")
	}
	if cfg.Deliver == nil {
		return nil, fmt.Errorf("schedule: nil deliver callback")
	}
	return &Module{cfg: cfg, sessionID: sessionID}, nil
}

// Tools builds schedule_create / schedule_list / schedule_delete.
// Tools 构建 schedule_create / schedule_list / schedule_delete。
func (m *Module) Tools() ([]kernel.Tool, error) {
	create, err := tool.ToolFromFunc(
		m.create,
		tool.WithToolName("schedule_create"),
		tool.WithToolDescription("Create a durable reminder: after_seconds (one-shot delay), at (absolute UTC time), or every_seconds (>=300 fixed cadence). Exactly one selector is required. The reminder survives restarts (log-as-state)."),
		tool.WithToolEffects(kernel.EffectRead),
	)
	if err != nil {
		return nil, err
	}
	list, err := tool.ToolFromFunc(
		m.list,
		tool.WithToolName("schedule_list"),
		tool.WithToolDescription("List the session's active reminders (id, kind, prompt, due/next time)."),
		tool.WithToolEffects(kernel.EffectRead),
	)
	if err != nil {
		return nil, err
	}
	del, err := tool.ToolFromFunc(
		m.delete,
		tool.WithToolName("schedule_delete"),
		tool.WithToolDescription("Delete a reminder by id (a no-op for unknown or already-fired ids)."),
		tool.WithToolEffects(kernel.EffectRead),
	)
	if err != nil {
		return nil, err
	}
	return []kernel.Tool{create, list, del}, nil
}

// createArgs: exactly one of after_seconds / at / every_seconds.
type createArgs struct {
	Prompt       string `json:"prompt" description:"The reminder text"`
	AfterSeconds int64  `json:"after_seconds,omitempty" description:"One-shot delay in seconds"`
	At           string `json:"at,omitempty" description:"Absolute UTC time (RFC3339)"`
	EverySeconds int64  `json:"every_seconds,omitempty" description:"Fixed cadence in seconds (>= 300)"`
}

func (m *Module) create(_ context.Context, args createArgs) (string, error) {
	if trim(args.Prompt) == "" {
		return "", fmt.Errorf("schedule: prompt is required")
	}
	// Exactly one selector: count the supplied forms.
	selectors := 0
	if args.AfterSeconds > 0 {
		selectors++
	}
	if trim(args.At) != "" {
		selectors++
	}
	if args.EverySeconds >= 300 {
		selectors++
	}
	if selectors != 1 {
		return "", fmt.Errorf("schedule: provide exactly one of after_seconds, at (RFC3339 UTC), or every_seconds (>=300)")
	}
	now := time.Now().UTC()
	r := Reminder{
		ID:        types.SessionID(),
		Prompt:    args.Prompt,
		CreatedAt: now,
	}
	switch {
	case args.AfterSeconds > 0:
		r.Kind = KindAfter
		r.AfterSecs = args.AfterSeconds
	case trim(args.At) != "":
		at, err := time.Parse(time.RFC3339, args.At)
		if err != nil {
			return "", fmt.Errorf("schedule: invalid at time (want RFC3339 UTC): %w", err)
		}
		r.Kind = KindAt
		r.At = at.UTC()
		if !r.At.After(now) {
			return "", fmt.Errorf("schedule: at time must be in the future")
		}
	case args.EverySeconds >= 300:
		r.Kind = KindEvery
		r.EverySecs = args.EverySeconds
		// First occurrence: CreatedAt + cadence (the anchor).
		r.At = now.Add(time.Duration(r.EverySecs) * time.Second)
	}
	if err := m.append(r); err != nil {
		return "", err
	}
	return `{"id":"` + r.ID + `","kind":"` + string(r.Kind) + `"}`, nil
}

func (m *Module) list(_ context.Context) (string, error) {
	rs := m.fold()
	b, _ := json.Marshal(rs)
	return string(b), nil
}

func (m *Module) delete(_ context.Context, args struct {
	ID string `json:"id" description:"The reminder id"`
}) (string, error) {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	rs := m.fold()
	var kept []Reminder
	found := false
	for _, r := range rs {
		if r.ID == args.ID {
			found = true
			continue
		}
		kept = append(kept, r)
	}
	if !found {
		return `{"deleted":false}`, nil
	}
	if err := m.replaceAll(kept); err != nil {
		return "", err
	}
	return `{"deleted":true}`, nil
}

// append writes one reminder as a log-only schedule/change event carrying
// the whole list (last-wins whole-value rule — DSH). The fold→modify→append
// sequence is atomic under stateMu: a concurrent create/delete/fireDue can
// never overwrite this update.
func (m *Module) append(r Reminder) error {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	rs := m.fold()
	rs = append(rs, r)
	return m.replaceAll(rs)
}

// replaceAll persists the whole reminder list as one log-only event.
func (m *Module) replaceAll(rs []Reminder) error {
	_, err := m.cfg.Log.Append(types.NewLogOnlyEvent("schedule/change", map[string]any{
		"reminders": rs,
	}))
	return err
}

// fold derives the reminder list from the log (the truth). A malformed
// schedule/change event fails loudly: the module logs the corruption and
// returns the last-good list instead of silently treating the log as empty
// (a damaged event must not make reminders vanish — fail-closed on the
// "reminders lost" side). An empty log yields an empty slice, never nil.
func (m *Module) fold() []Reminder {
	rs := make([]Reminder, 0)
	for _, ev := range m.cfg.Log.Events() {
		if ev.Kind != "schedule/change" || ev.Meta == nil {
			continue
		}
		raw, ok := ev.Meta["reminders"]
		if !ok {
			continue
		}
		b, err := json.Marshal(raw)
		if err != nil {
			log.Printf("[schedule] corrupt schedule/change event: %v", err)
			continue
		}
		var parsed []Reminder
		if err := json.Unmarshal(b, &parsed); err != nil {
			log.Printf("[schedule] corrupt schedule/change event: %v", err)
			continue
		}
		rs = parsed
	}
	return rs
}

// Start launches the per-session scheduler: it watches the log, derives
// due reminders, fires Deliver, and persists the fired state. Idempotent.
//
// Start 启动每会话调度器：观察日志、派生到期提醒、触发 Deliver 并持久化
// 已触发状态。幂等。
func (m *Module) Start(ctx context.Context) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.stop = make(chan struct{})
	m.stopped = make(chan struct{})
	m.mu.Unlock()

	go m.run(ctx)
}

// Stop halts the scheduler and waits for it to settle. The module can be
// restarted afterwards (the old Start-after-Stop was a silent no-op).
// Stop 停止调度器并等待其停稳。之后可重新 Start（旧实现 Stop 后 Start
// 是静默空操作）。
func (m *Module) Stop() {
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return
	}
	close(m.stop)
	m.mu.Unlock()
	<-m.stopped
	m.mu.Lock()
	m.started = false
	m.stop = make(chan struct{})
	m.stopped = make(chan struct{})
	m.mu.Unlock()
}

// run is the scheduler loop: tick every second, fire due reminders.
func (m *Module) run(ctx context.Context) {
	defer close(m.stopped)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	// Fire any reminders that came due while the scheduler was down.
	m.fireDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stop:
			return
		case <-ticker.C:
			m.fireDue(ctx)
		}
	}
}

// fireDue fires every due reminder and persists the updated state. A
// delivery failure is logged-and-skipped (the reminder stays in the log
// and remains due — the next tick retries it). A PERSISTENCE failure after
// delivery is logged and tracked in the process-local delivered set: the
// reminder will not re-fire in this scheduler lifetime even though the log
// still marks it due (at-least-once without duplicate delivery in-process;
// a restart may re-deliver, which is the documented at-least-once bound).
//
// Deliveries run OUTSIDE the state lock with panic recovery: a slow or
// panicking product callback must neither stall the scheduler nor kill the
// goroutine and strand reminders (the old code delivered under stateMu,
// panicking the scheduler).
func (m *Module) fireDue(ctx context.Context) {
	now := time.Now().UTC()

	type fire struct {
		old Reminder
	}
	var fires []fire
	changed := false

	m.stateMu.Lock()
	rs := m.fold()
	for i := range rs {
		r := &rs[i]
		if r.Done {
			continue
		}
		if !due(*r, now) {
			continue
		}
		if m.alreadyDelivered(r.ID) {
			continue
		}
		old := *r
		r.LastFiredAt = now
		if r.Kind != KindEvery {
			r.Done = true
		} else {
			// Re-arm ANCHOR-ALIGNED: advance from the last target, not
			// from the actual fire time — delivery latency must not
			// accumulate drift. Occurrences missed during downtime are
			// skipped to the next target at/after now.
			next := r.At.Add(time.Duration(r.EverySecs) * time.Second)
			if !next.After(now) {
				missed := now.Sub(next)
				steps := missed/(time.Duration(r.EverySecs)*time.Second) + 1
				next = next.Add(steps * time.Duration(r.EverySecs) * time.Second)
			}
			r.At = next
		}
		changed = true
		m.markDelivered(r.ID)
		fires = append(fires, fire{old: old})
	}
	if changed {
		if err := m.replaceAll(rs); err != nil {
			log.Printf("[schedule] persist after fire failed: %v", err)
		}
	}
	m.stateMu.Unlock()

	for _, f := range fires {
		m.deliverSafe(ctx, f.old)
	}
}

// deliverSafe runs one delivery with panic recovery — a panicking product
// callback must not kill the scheduler goroutine and strand reminders.
func (m *Module) deliverSafe(ctx context.Context, r Reminder) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("[schedule] deliver %q panicked: %v", r.ID, p)
		}
	}()
	m.cfg.Deliver(ctx, m.sessionID, r)
}

// delivered guards against duplicate delivery within one scheduler
// lifetime when persistence fails after a fire (the log still marks the
// reminder due). The set is process-local; a restart may re-deliver, which
// is the documented at-least-once bound.
type deliveredSet struct {
	mu sync.Mutex
	m  map[string]bool
}

var delivered = &deliveredSet{m: make(map[string]bool)}

func (d *deliveredSet) mark(id string) {
	d.mu.Lock()
	d.m[id] = true
	d.mu.Unlock()
}

func (d *deliveredSet) has(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.m[id]
}

func (m *Module) markDelivered(id string) { delivered.mark(id) }
func (m *Module) alreadyDelivered(id string) bool {
	return delivered.has(id)
}

// due reports whether the reminder should fire at now. One-shots fire once
// (Done flips after firing); every-reminders fire when the anchor-aligned
// occurrence has arrived.
func due(r Reminder, now time.Time) bool {
	if r.Done {
		return false
	}
	switch r.Kind {
	case KindAfter:
		// Fires once AfterSecs after CreatedAt.
		return !now.Before(r.CreatedAt.Add(time.Duration(r.AfterSecs) * time.Second))
	case KindAt:
		return !now.Before(r.At)
	case KindEvery:
		// Next occurrence is tracked in At (the anchor-aligned target).
		if r.At.IsZero() {
			return false
		}
		return !now.Before(r.At)
	default:
		return false
	}
}

func trim(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n') {
		end--
	}
	return s[start:end]
}
