// Package registry provides the scoped named-registry base shared by every
// capability registry in the harness: tools, skills, policies, modules,
// middlewares, and backends all register into one of these. It encodes the
// two scope laws borrowed from DSH's scoped composition:
//
//   - Reads inherit downward: a View resolves session, host, and global
//     layers, with the nearest scope winning.
//   - Events propagate upward only: a registration notifies observers of the
//     registering registry and every ancestor linked through LinkParent,
//     never any child.
//
// Registration stays at exactly one scope: duplicate names at the same
// scope fail loudly instead of silently shadowing.
//
// The registry is a routing and lifetime boundary, not a permission boundary
// — access control lives in FilePolicy/ApprovalPolicy, never here.
//
// Package registry 提供 harness 中所有能力注册表的共有基座（工具、技能、
// 策略、模块、中间件、后端都注册进其一）。它固化了借鉴自 DSH 作用域组装
// 的两条法则：
//
//   - 读视图向下继承：View 依次解析会话/宿主/全局层，最近作用域胜出。
//   - 事件只向上传播：注册事件只通知本注册表及经 LinkParent 链接的
//     全部祖先的观察者，绝不通知任何子层。
//
// 注册固定在唯一作用域：同作用域重名显式报错，绝不静默遮蔽。
//
// 注册表是路由与生命周期边界，不是权限边界——访问控制走
// FilePolicy/ApprovalPolicy，绝不混在此处。
package registry

import (
	"errors"
	"sort"
	"sync"
)

// ErrNameTaken is returned by Register when the name already exists at the
// registry's scope.
//
// ErrNameTaken 在名称已存在于注册表作用域时由 Register 返回。
var ErrNameTaken = errors.New("registry: name already registered at this scope")

// ErrParentBound is returned by LinkParent when the registry already has a
// parent (a scope binds its parent exactly once).
//
// ErrParentBound 在注册表已有父级时由 LinkParent 返回（作用域只绑定一次父级）。
var ErrParentBound = errors.New("registry: parent already bound")

// ErrInvalidParent is returned by LinkParent when the link would violate the
// scope laws: a parent must be strictly narrower than its child (events flow
// outward), and links must stay acyclic.
//
// ErrInvalidParent 在链接违反作用域法则时由 LinkParent 返回：父必须严格窄于
// 其子（事件向外流），且链接不得成环。
var ErrInvalidParent = errors.New("registry: invalid parent scope or cycle")

// Scope is the plane a registration belongs to.
// Scope 是注册项所属的平面。
type Scope uint8

const (
	// ScopeGlobal holds process-wide entries visible to every session.
	// ScopeGlobal 持有进程级条目，对每个会话可见。
	ScopeGlobal Scope = iota
	// ScopeHost holds entries shared by the sessions of one Host.
	// ScopeHost 持有同一 Host 的会话共享的条目。
	ScopeHost
	// ScopeSession holds entries private to one session.
	// ScopeSession 持有单个会话私有的条目。
	ScopeSession
)

// String returns the machine-readable name of the scope.
// String 返回作用域的机器可读名称。
func (s Scope) String() string {
	switch s {
	case ScopeGlobal:
		return "global"
	case ScopeHost:
		return "host"
	case ScopeSession:
		return "session"
	default:
		return "unknown"
	}
}

// Entry is a registered value together with its scope.
// Entry 是注册值及其所属作用域。
type Entry[T any] struct {
	Scope Scope
	Name  string
	Value T
}

// Registry holds registrations of T at a single scope. It is safe for
// concurrent use. Registration events notify this registry's observers and
// then bubble up the parent chain — events propagate upward only. Removal
// events follow the same upward-only law through ObserveRemovals.
//
// Registry 持有 T 在单一作用域的注册项，并发安全。注册事件先通知本注册表
// 的观察者，再沿父链向上冒泡——事件只向上传播。注销事件经
// ObserveRemovals 遵循同一"只向上"法则。
type Registry[T any] struct {
	mu       sync.RWMutex
	scope    Scope
	parent   *Registry[T]
	items    map[string]T
	watches  map[uint64]func(Entry[T])
	removals map[uint64]func(Entry[T])
	nextID   uint64
}

// New creates an empty registry bound to the given scope.
// New 创建绑定到给定作用域的空注册表。
func New[T any](scope Scope) *Registry[T] {
	return &Registry[T]{
		scope:    scope,
		items:    make(map[string]T),
		watches:  make(map[uint64]func(Entry[T])),
		removals: make(map[uint64]func(Entry[T])),
	}
}

// Scope returns the scope this registry registers into.
// Scope 返回本注册表注册时所属的作用域。
func (r *Registry[T]) Scope() Scope { return r.scope }

// LinkParent binds the registry's parent chain exactly once. Registration
// events then bubble from this registry to parent and its ancestors. The
// parent must be strictly narrower than this registry (parent.scope <
// r.scope — events flow outward) and the link must not create a cycle;
// violations return ErrInvalidParent. A second link returns ErrParentBound.
//
// LinkParent 一次性绑定父链。此后注册事件从本注册表冒泡到父及其祖先。
// 父必须严格窄于本注册表（parent.scope < r.scope——事件向外流），且不得
// 成环；违规返回 ErrInvalidParent。重复绑定返回 ErrParentBound。
func (r *Registry[T]) LinkParent(parent *Registry[T]) error {
	if parent == nil {
		return errors.New("registry: nil parent")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.parent != nil {
		return ErrParentBound
	}
	if parent.scope >= r.scope {
		return ErrInvalidParent
	}
	// Cycle check: walk the parent chain; re-entering r means a loop.
	for p := parent; p != nil; p = p.parent {
		if p == r {
			return ErrInvalidParent
		}
	}
	r.parent = parent
	return nil
}

// Parent returns the bound parent registry, or nil.
// Parent 返回已绑定的父注册表；未绑定时返回 nil。
func (r *Registry[T]) Parent() *Registry[T] {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.parent
}

// Register adds value under name at this registry's scope. A duplicate name
// returns ErrNameTaken — shadowing is a mistake, not a feature. Observers of
// this registry and of every linked ancestor are notified, in that order.
// Callbacks run OUTSIDE the registry lock — a callback that re-enters the
// registry (Get/Names/Register) cannot deadlock.
//
// Register 将 value 以 name 注册到本注册表的作用域。重名返回 ErrNameTaken——
// 遮蔽是错误而非特性。本注册表及所有链接祖先的观察者依次收到通知。
// 回调在注册表锁外执行——回调重入注册表（Get/Names/Register）不会死锁。
func (r *Registry[T]) Register(name string, value T) error {
	if name == "" {
		return errors.New("registry: empty name")
	}
	r.mu.Lock()
	if _, ok := r.items[name]; ok {
		r.mu.Unlock()
		return ErrNameTaken
	}
	r.items[name] = value
	entry := Entry[T]{Scope: r.scope, Name: name, Value: value}
	fns := collect(r.watches)
	r.mu.Unlock()

	for _, fn := range fns {
		if fn != nil {
			fn(entry)
		}
	}
	r.bubbleUp(entry)
	return nil
}

// bubbleUp notifies the linked ancestors' observers with the child entry.
// Events propagate upward only.
func (r *Registry[T]) bubbleUp(entry Entry[T]) {
	for p := r.parent; p != nil; p = p.parent {
		p.notify(entry)
	}
}

// notify delivers an entry to this registry's observers, outside the
// registry lock.
func (r *Registry[T]) notify(entry Entry[T]) {
	r.mu.Lock()
	fns := collect(r.watches)
	r.mu.Unlock()
	for _, fn := range fns {
		if fn != nil {
			fn(entry)
		}
	}
}

// collect snapshots a watch/removal map into a slice for lock-free
// dispatch. Nil entries (unregistered slots) are kept so the caller can
// skip them; the slice is a copy, never the live map.
func collect[F any](m map[uint64]F) []F {
	out := make([]F, 0, len(m))
	for _, fn := range m {
		out = append(out, fn)
	}
	return out
}

// Unregister removes the named entry. It reports whether an entry existed.
// Removal observers of this registry and of every linked ancestor are
// notified (upward-only), mirroring registration notification. Callbacks
// run OUTSIDE the registry lock — a callback that re-enters the registry
// (Get/Names/Register) cannot deadlock.
//
// Unregister 移除指定名称的条目并报告条目原先是否存在。本注册表及所有链接
// 祖先的注销观察者会收到通知（只向上），与注册通知一致。回调在注册表锁外
// 执行——回调重入注册表（Get/Names/Register）不会死锁。
func (r *Registry[T]) Unregister(name string) bool {
	r.mu.Lock()
	val, ok := r.items[name]
	if !ok {
		r.mu.Unlock()
		return false
	}
	delete(r.items, name)
	entry := Entry[T]{Scope: r.scope, Name: name, Value: val}
	fns := collect(r.removals)
	r.mu.Unlock()

	for _, fn := range fns {
		if fn != nil {
			fn(entry)
		}
	}
	r.bubbleUpRemoval(entry)
	return true
}

// bubbleUpRemoval notifies the linked ancestors' removal observers with the
// removed entry. Events propagate upward only.
func (r *Registry[T]) bubbleUpRemoval(entry Entry[T]) {
	for p := r.parent; p != nil; p = p.parent {
		p.notifyRemoval(entry)
	}
}

// notifyRemoval delivers a removal to this registry's removal observers,
// outside the registry lock.
func (r *Registry[T]) notifyRemoval(entry Entry[T]) {
	r.mu.Lock()
	fns := collect(r.removals)
	r.mu.Unlock()
	for _, fn := range fns {
		if fn != nil {
			fn(entry)
		}
	}
}

// Get resolves a name at this registry's own scope only. Use View for
// layered resolution across scopes.
//
// Get 仅在本注册表自身作用域内解析名称。跨作用域解析请用 View。
func (r *Registry[T]) Get(name string) (T, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.items[name]
	return v, ok
}

// Names returns the registered names at this scope, sorted.
// Names 返回本作用域已注册的名称，按序排列。
func (r *Registry[T]) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.items))
	for name := range r.items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Observe subscribes to registration events visible at this registry: its
// own registrations and, when linked, every descendant's (events flow
// upward). The returned cancel function removes the subscription; calling it
// twice is a no-op.
//
// Observe 订阅本注册表可见的注册事件：自身的注册，以及（链接时）所有
// 后代的注册（事件向上流）。返回的 cancel 移除订阅；重复调用无副作用。
func (r *Registry[T]) Observe(fn func(Entry[T])) (cancel func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	id := r.nextID
	r.watches[id] = fn
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.watches, id)
			r.mu.Unlock()
		})
	}
}

// ObserveRemovals subscribes to removal events visible at this registry:
// its own unregistrations and, when linked, every descendant's (events flow
// upward). The returned cancel function removes the subscription.
//
// ObserveRemovals 订阅本注册表可见的注销事件：自身的注销，以及（链接时）
// 所有后代的注销（事件向上流）。返回的 cancel 移除订阅。
func (r *Registry[T]) ObserveRemovals(fn func(Entry[T])) (cancel func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	id := r.nextID
	r.removals[id] = fn
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.removals, id)
			r.mu.Unlock()
		})
	}
}

// View stacks registries from innermost to outermost scope. Reads inherit
// downward: the nearest registration wins, and entries from outer scopes
// remain visible when the inner scopes do not register the name. A View is a
// read snapshot handle — it never owns registrations.
//
// View 从内到外叠加注册表。读视图向下继承：最近注册胜出，内层未注册的
// 名称仍可见外层条目。View 是只读快照句柄——不拥有任何注册项。
type View[T any] struct {
	layers []*Registry[T] // layers[0] is the innermost (session).
}

// NewView builds a layered view. Pass registries innermost-first:
// NewView(session, host, global).
//
// NewView 构建分层视图。注册表按从内到外的顺序传入：
// NewView(session, host, global)。
func NewView[T any](layers ...*Registry[T]) *View[T] {
	return &View[T]{layers: layers}
}

// Resolve walks the layers from innermost to outermost and returns the first
// registration of name, together with its scope.
//
// Resolve 从内到外逐层查找 name 的第一个注册项及其作用域。
func (v *View[T]) Resolve(name string) (Entry[T], bool) {
	for _, layer := range v.layers {
		if val, ok := layer.Get(name); ok {
			return Entry[T]{Scope: layer.Scope(), Name: name, Value: val}, true
		}
	}
	var zero Entry[T]
	return zero, false
}

// List returns every entry visible through the view, deduplicated by name
// (nearest scope wins), sorted by name.
//
// List 返回视图可见的全部条目，按名去重（最近作用域胜出），按名排序。
func (v *View[T]) List() []Entry[T] {
	seen := make(map[string]Entry[T])
	for _, layer := range v.layers {
		for _, name := range layer.Names() {
			if _, ok := seen[name]; ok {
				continue // a nearer scope already won
			}
			val, _ := layer.Get(name)
			seen[name] = Entry[T]{Scope: layer.Scope(), Name: name, Value: val}
		}
	}
	out := make([]Entry[T], 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Snapshot returns the visible name→value map (nearest scope wins). The
// returned map is a copy — mutating it never affects the registries.
//
// Snapshot 返回可见的 name→value 映射（最近作用域胜出）。返回的是副本——
// 修改它绝不影响注册表。
func (v *View[T]) Snapshot() map[string]T {
	out := make(map[string]T)
	for _, e := range v.List() {
		out[e.Name] = e.Value
	}
	return out
}

// Change is one net-visible change of a View: a name that became visible
// (Removed=false) or stopped being visible (Removed=true) under the
// nearest-scope-wins rule. Value carries the winning entry at the change
// point — the newly visible entry for additions, the previously visible
// entry for removals.
//
// Change 是 View 的一次净可见变化：在"最近作用域胜出"规则下某个名称变为
// 可见（Removed=false）或不再可见（Removed=true）。Value 携带变化点的
// 胜出条目——新增时为新可见条目，移除时为先前可见条目。
type Change[T any] struct {
	Name    string
	Value   T
	Removed bool
}

// ObserveChanges subscribes to the net-visible change feed of the view:
// each call reports one name that appeared in or disappeared from the view
// snapshot, after shadowing by nearer scopes is applied. The initial
// snapshot is NOT replayed — the feed reports changes only, so consumers
// seed from Snapshot() and then apply deltas. A shadowed re-registration
// (a same name at an outer layer while an inner layer still wins) and a
// removal that reveals an outer winner produce no event: only visibility
// flips are reported. The callback runs outside any registry lock; the
// returned cancel removes the subscription.
//
// ObserveChanges 订阅视图的净可见变更馈送：每次回调报告一个名称在视图
// 快照中出现（Removed=false）或消失（Removed=true），已按最近作用域
// 遮蔽。初始快照不重放——馈送只报增量，消费方先取 Snapshot() 再应用
// 增量。被遮蔽的同名注册（外层注册而内层仍胜出）与"移除后外层条目
// 显露"都不产生事件：只有可见性翻转才上报。回调在注册表锁外执行；
// 返回的 cancel 移除订阅。
func (v *View[T]) ObserveChanges(fn func(Change[T])) (cancel func()) {
	var mu sync.Mutex
	// visible maps name → winning layer index (nearest scope wins).
	visible := make(map[string]int)
	for i, layer := range v.layers {
		for _, name := range layer.Names() {
			if _, ok := visible[name]; !ok {
				visible[name] = i
			}
		}
	}

	var cancels []func()
	for i, layer := range v.layers {
		idx := i
		reg := layer
		cancels = append(cancels, reg.Observe(func(e Entry[T]) {
			var emit *Change[T]
			mu.Lock()
			cur, ok := visible[e.Name]
			switch {
			case !ok:
				visible[e.Name] = idx
				emit = &Change[T]{Name: e.Name, Value: e.Value}
			case idx < cur:
				visible[e.Name] = idx
				emit = &Change[T]{Name: e.Name, Value: e.Value}
			}
			mu.Unlock()
			if emit != nil {
				fn(*emit)
			}
		}))
		cancels = append(cancels, reg.ObserveRemovals(func(e Entry[T]) {
			var emit *Change[T]
			mu.Lock()
			cur, ok := visible[e.Name]
			if ok && cur == idx {
				// Find the next outer winner; a removal that reveals an
				// outer entry keeps the name visible (no event).
				for j := idx + 1; j < len(v.layers); j++ {
					if _, ok := v.layers[j].Get(e.Name); ok {
						visible[e.Name] = j
						mu.Unlock()
						return
					}
				}
				delete(visible, e.Name)
				emit = &Change[T]{Name: e.Name, Value: e.Value, Removed: true}
			}
			mu.Unlock()
			if emit != nil {
				fn(*emit)
			}
		}))
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, c := range cancels {
				c()
			}
		})
	}
}
