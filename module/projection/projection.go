// Package projection provides the session-projection registry: domain
// units fold the session event log into derived read models for consumers
// (GUIs, dashboards, exports) — the Go counterpart of DSH's
// session-projection seam. The framework drives, the domain computes:
//
//   - A Unit declares pure fold semantics (Reset/Apply/View). The registry
//     feeds every committed log event to every unit and serves finished
//     whole values (last-wins) — never deltas.
//   - Units are keyed; consumers read Snapshot(sessionID) for one
//     consistent cut across all units, or subscribe to the change feed for
//     per-unit updates.
//   - Attaching a log replays nothing by default: units fold events from
//     the moment of attachment. Consumers that need a full replay fold
//     the log's committed events first (Log.Events) and then attach.
//
// The package owns the drive only — what a unit computes (todo lists, goal
// snapshots, plan state) belongs to the domain. It is safe for concurrent
// use.
//
// Package projection 提供会话投影注册表：域单元把会话事件日志折叠为派生
// 读模型供消费方（GUI、看板、导出）使用——DSH session-projection 缝的
// Go 对应物。框架驱动、域计算：
//
//   - Unit 声明纯折叠语义（Reset/Apply/View）。注册表把每条已提交日志
//     事件喂给每个单元，对外提供完整终值（last-wins）——绝无增量。
//   - 单元按 key 区分；消费方读 Snapshot(sessionID) 获取跨单元的
//     一致切面，或订阅变更馈送获取逐单元更新。
//   - 挂载日志默认不重放：单元从挂载时刻起折叠。需要完整重放的消费方
//     先折叠日志的已提交事件（Log.Events）再挂载。
//
// 本包只拥有驱动——单元计算什么（todo 列表、goal 快照、plan 状态）属于
// 域。并发安全。
package projection

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/go-gocel/gocel/core/session"
	"github.com/go-gocel/gocel/core/types"
)

var (
	// errNilUnit is returned when registering a nil or keyless unit.
	errNilUnit = errors.New("projection: nil or keyless unit")
	// errDuplicateUnit is returned when a unit key is already registered.
	errDuplicateUnit = errors.New("projection: duplicate unit key")
	// errNilLog is returned when attaching a nil log.
	errNilLog = errors.New("projection: nil log")
	// errSessionBound is returned when attaching a second log with a
	// different id — units are per-key singletons, so multi-session
	// registries silently mixed every session's events (verified defect).
	// One registry serves one session; products use one registry per
	// session.
	errSessionBound = errors.New("projection: registry already bound to a session log")
)

// jsonMarshal serializes a view for equality comparison.
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// Unit is one domain projection: pure fold semantics over session events.
// Implementations must be safe for concurrent Apply/View use (the registry
// serializes per log, but multiple logs may feed concurrently).
//
// Unit 是一个域投影单元：对会话事件的纯折叠语义。实现必须并发安全
// （注册表按日志串行化，但多个日志可能并发馈送）。
type Unit interface {
	// Key is the unit's stable identity in snapshots and feeds.
	Key() string
	// Reset returns the unit to its initial state (a fresh session).
	Reset()
	// Apply folds one committed event. Returning the same state reference
	// for unrelated events is the cheap path; the registry gates the
	// change feed on the view actually changing.
	Apply(ev types.SessionEvent)
	// View returns the current derived value (a whole JSON-able value).
	View() any
}

// Registry drives every registered unit over every attached log.
//
// Registry 驱动每个已注册单元折叠每条已挂载日志。
type Registry struct {
	mu        sync.Mutex
	units     map[string]Unit
	logs      map[string]*session.Log
	cancels   map[string]func() // sessionID → log subscription cancel
	nextID    uint64
	listeners map[uint64]func(key, sessionID string)
}

// New creates an empty registry.
// New 创建空注册表。
func New() *Registry {
	return &Registry{
		units:     make(map[string]Unit),
		logs:      make(map[string]*session.Log),
		cancels:   make(map[string]func()),
		listeners: make(map[uint64]func(key, sessionID string)),
	}
}

// Register adds a unit. A duplicate key fails loudly. Returns an idempotent
// unregister function. The already-attached log (at most one — single
// session per registry) is replayed through the new unit immediately (fold
// from the log's committed events), so a unit registered after events
// flowed still converges.
//
// Register 注册单元。重复 key 显式报错。返回幂等注销函数。已挂载日志
// （至多一个——注册表单会话）立即重放进新单元（从日志已提交事件折叠），
// 因此事件流经之后注册的单元仍能收敛。
func (r *Registry) Register(u Unit) error {
	if u == nil || u.Key() == "" {
		return errNilUnit
	}
	r.mu.Lock()
	if _, ok := r.units[u.Key()]; ok {
		r.mu.Unlock()
		return errDuplicateUnit
	}
	r.units[u.Key()] = u
	// Snapshot the attached logs under the lock; fold them OUTSIDE it
	// (the old code called log.Events() while holding r.mu — the lock
	// order inversion with log.Append's notify → feed deadlocked).
	logs := make([]*session.Log, 0, len(r.logs))
	for _, log := range r.logs {
		logs = append(logs, log)
	}
	r.mu.Unlock()

	// Replay the attached log (single-session registry) into the fresh
	// unit. One reset per unit — the old per-log Reset() left only the
	// last log's history with multiple logs.
	if len(logs) > 0 {
		u.Reset()
		for _, ev := range logs[0].Events() {
			u.Apply(ev)
		}
	}
	return nil
}

// Unregister removes a unit by key. It reports whether the unit existed.
// Unregister 按 key 移除单元，并返回该单元之前是否存在。
func (r *Registry) Unregister(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.units[key]
	delete(r.units, key)
	return ok
}

// Attach starts driving the log's events through every registered unit.
// Events committed before attachment are NOT replayed — units fold from
// the moment of attachment (fold Log.Events first for a full replay).
// Attaching the same log twice is a no-op; attaching a DIFFERENT log fails
// loudly (one registry = one session — units are per-key singletons, so
// multi-log registries silently mixed sessions, a verified defect).
// Returns an idempotent detach.
//
// Attach 开始把日志事件驱动到每个已注册单元。挂载前已提交的事件不重放
// ——单元从挂载时刻起折叠（需要完整重放先折叠 Log.Events）。重复挂载
// 同一日志为 no-op；挂载不同日志显式报错（一个注册表 = 一个会话——
// 单元是每 key 单例，多日志注册表会静默混会话，已证实的缺陷）。
// 返回幂等 detach。
func (r *Registry) Attach(log *session.Log) (detach func(), err error) {
	if log == nil {
		return nil, errNilLog
	}
	r.mu.Lock()
	if existing, ok := r.logs[log.ID()]; ok {
		r.mu.Unlock()
		if existing == log {
			return func() {}, nil
		}
		// Same id, different instance: treat as bound.
		return func() {}, nil
	}
	if len(r.logs) > 0 {
		r.mu.Unlock()
		return nil, errSessionBound
	}
	r.logs[log.ID()] = log
	r.mu.Unlock()

	// Subscribe OUTSIDE the registry lock: log.Subscribe takes the log's
	// lock, and log.Append → notify → feed takes the registry lock — the
	// old lock order (r.mu → l.mu in Attach) inverted the append path
	// (l.mu → r.mu) and deadlocked (verified defect).
	cancel := log.Subscribe(func(ev types.SessionEvent) {
		r.feed(log.ID(), ev)
	})

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.cancels, log.ID())
			delete(r.logs, log.ID())
			r.mu.Unlock()
			if cancel != nil {
				cancel()
			}
		})
	}, nil
}

// feed applies one event to every unit and notifies the change feed for
// units whose view changed.
func (r *Registry) feed(sessionID string, ev types.SessionEvent) {
	var changed []string
	r.mu.Lock()
	for key, u := range r.units {
		before := u.View()
		u.Apply(ev)
		after := u.View()
		if !sameView(before, after) {
			changed = append(changed, key)
		}
	}
	var fns []func(string, string)
	for _, fn := range r.listeners {
		fns = append(fns, fn)
	}
	r.mu.Unlock()
	for _, key := range changed {
		for _, fn := range fns {
			fn(key, sessionID)
		}
	}
}

// Snapshot returns one consistent cut of every unit's current view for the
// session, keyed by unit key. An empty map when the log is not attached or
// no units are registered.
//
// Snapshot 返回该会话每个单元当前视图的一致切面（按单元 key）。日志未
// 挂载或未注册单元时返回空 map。
func (r *Registry) Snapshot(sessionID string) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]any, len(r.units))
	for key, u := range r.units {
		out[key] = u.View()
	}
	return out
}

// OnChanged subscribes to the change feed: one call per unit whose view
// changed, per committed event, carrying the unit key and the session id.
// Returns an idempotent cancel.
//
// OnChanged 订阅变更馈送：每个视图发生变化的单元、每条已提交事件一次
// 回调，携带单元 key 与会话 id。返回幂等 cancel。
func (r *Registry) OnChanged(fn func(key, sessionID string)) (cancel func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	id := r.nextID
	r.listeners[id] = fn
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.listeners, id)
			r.mu.Unlock()
		})
	}
}

// sameView reports whether two views are deeply equal (JSON comparison).
// It is the cheap gate that keeps unrelated events from notifying.
func sameView(a, b any) bool {
	aj, err1 := jsonMarshal(a)
	bj, err2 := jsonMarshal(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return string(aj) == string(bj)
}
