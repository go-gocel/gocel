package graph

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
)

// GraphState carries shared state across nodes during graph execution.
// It is thread-safe and supports forking (copy-on-write semantics for sub-graphs).
//
// GraphState implements kernel.StateManager, making it usable both as the
// graph-level shared state and as the AgentContext state manager.
//
// GraphState 承载图执行过程中节点间共享的状态。
// 线程安全，支持分支（为子图提供写时复制语义）。
// GraphState 实现 kernel.StateManager，可作为图级共享状态和 AgentContext 的 StateManager。
type GraphState struct {
	mu       sync.RWMutex
	data     map[string]any
	history  []StateEntry
	watchers []*graphWatcherEntry
}

// graphWatcherEntry records a registered watcher.
type graphWatcherEntry struct {
	keys []string
	fn   kernel.StateChangeFn
}

// StateEntry records a single state mutation.
// StateEntry 记录一次状态变更。
type StateEntry struct {
	Key       string
	OldValue  any
	NewValue  any
	Source    string // node ID or component name that made the change
	Timestamp time.Time
}

// pendingCall carries a watcher callback to run AFTER the store lock is
// released (re-entrant mutations from watchers would otherwise deadlock —
// the same fix applied to core/runtime.InMemoryState).
type pendingCall struct {
	fn      kernel.StateChangeFn
	changes []kernel.StateChange
}

// NewGraphState creates an empty GraphState.
// NewGraphState 创建一个空的 GraphState。
func NewGraphState() *GraphState {
	return &GraphState{
		data:    make(map[string]any),
		history: make([]StateEntry, 0),
	}
}

// ── read operations ─────────────────────────────────────────

// Get returns the value for a key.
// Get 返回指定键的值。
func (gs *GraphState) Get(key string) (any, bool) {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	v, ok := gs.data[key]
	return v, ok
}

// GetString returns the string value for a key.
// Returns empty string if the key doesn't exist or the value is not a string.
// GetString 返回指定键的字符串值；键不存在或值不是字符串时返回空字符串。
func (gs *GraphState) GetString(key string) string {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	if v, ok := gs.data[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// GetInt returns the int value for a key.
// Returns 0 if the key doesn't exist or the value is not an int.
// GetInt 返回指定键的 int 值；键不存在或值不是 int 时返回 0。
func (gs *GraphState) GetInt(key string) int {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	if v, ok := gs.data[key]; ok {
		if i, ok := v.(int); ok {
			return i
		}
	}
	return 0
}

// GetFloat returns the float64 value for a key.
// Returns 0 if the key doesn't exist or the value is not a float64.
// GetFloat 返回指定键的 float64 值；键不存在或值不是 float64 时返回 0。
func (gs *GraphState) GetFloat(key string) float64 {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	if v, ok := gs.data[key]; ok {
		if f, ok := v.(float64); ok {
			return f
		}
	}
	return 0
}

// GetBool returns the bool value for a key.
// Returns false if the key doesn't exist or the value is not a bool.
// GetBool 返回指定键的 bool 值；键不存在或值不是 bool 时返回 false。
func (gs *GraphState) GetBool(key string) bool {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	if v, ok := gs.data[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// Has returns true if the key exists.
// Has 返回键是否存在。
func (gs *GraphState) Has(key string) bool {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	_, ok := gs.data[key]
	return ok
}

// Keys returns all keys in the state.
// Keys 返回状态中的所有键（按字典序排序）。
func (gs *GraphState) Keys() []string {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	keys := make([]string, 0, len(gs.data))
	for k := range gs.data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ── write operations ────────────────────────────────────────

// SetWithSource stores a value with a source identifier (typically the node ID).
// This is the Graph-specific variant that tracks the source node. Watcher
// callbacks run OUTSIDE the write lock.
//
// SetWithSource 使用来源标识（通常是节点 ID）存储值。这是 Graph 专用版本，追踪来源节点。
// 观察者回调在写锁外执行。
func (gs *GraphState) SetWithSource(key string, value any, source string) {
	gs.mu.Lock()

	oldVal := gs.data[key]
	gs.data[key] = value

	gs.history = append(gs.history, StateEntry{
		Key:       key,
		OldValue:  oldVal,
		NewValue:  value,
		Source:    source,
		Timestamp: time.Now(),
	})

	pending := gs.collectLocked([]kernel.StateChange{
		{Key: key, OldValue: oldVal, NewValue: value},
	})
	gs.mu.Unlock()

	for _, p := range pending {
		p.fn(p.changes)
	}
}

// Set implements kernel.StateManager.Set.
// It stores a value without a source identifier (the generic variant).
//
// Set 实现 kernel.StateManager.Set，无来源标识的通用版本。
func (gs *GraphState) Set(key string, value any) {
	gs.SetWithSource(key, value, "")
}

// Delete removes a key from the state.
// Delete 从状态中删除指定键。
func (gs *GraphState) Delete(key string) {
	gs.mu.Lock()

	oldVal, ok := gs.data[key]
	if !ok {
		gs.mu.Unlock()
		return
	}
	delete(gs.data, key)
	gs.history = append(gs.history, StateEntry{
		Key:       key,
		OldValue:  oldVal,
		NewValue:  nil,
		Source:    "delete",
		Timestamp: time.Now(),
	})

	pending := gs.collectLocked([]kernel.StateChange{
		{Key: key, OldValue: oldVal, NewValue: nil},
	})
	gs.mu.Unlock()

	for _, p := range pending {
		p.fn(p.changes)
	}
}

// ── snapshot & fork ─────────────────────────────────────────

// Snapshot returns a shallow copy of all key-value pairs.
// Snapshot 返回所有键值对的浅拷贝快照。
func (gs *GraphState) Snapshot() map[string]any {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	result := make(map[string]any, len(gs.data))
	for k, v := range gs.data {
		result[k] = v
	}
	return result
}

// Fork creates a child GraphState that shares the same underlying data
// until the child writes (copy-on-write semantics).
//
// After Fork, the parent and child can be modified independently.
// The child state is a shallow clone — mutable values (maps, slices) are
// not deep-copied.
//
// Fork 创建子 GraphState，采用写时复制语义。
// Fork 后父子可以独立修改。可变对象（map、slice）不做深拷贝。
func (gs *GraphState) Fork() *GraphState {
	gs.mu.RLock()
	defer gs.mu.RUnlock()

	child := &GraphState{
		data:     make(map[string]any, len(gs.data)),
		history:  make([]StateEntry, len(gs.history)),
		watchers: make([]*graphWatcherEntry, len(gs.watchers)),
	}
	for k, v := range gs.data {
		child.data[k] = v
	}
	copy(child.history, gs.history)
	copy(child.watchers, gs.watchers)
	return child
}

// MergeFrom merges keys from another GraphState into this one.
// Only keys that do NOT already exist in this state are copied over.
// Merged keys flow through the normal write path (history + watchers), so
// Rollback and observers see them (the old direct-data write bypassed both).
//
// MergeFrom 将另一个 GraphState 的键合并到当前状态。
// 仅复制当前状态中不存在的键。合并键走正常写路径（历史 + 观察者）——
// 旧实现直接写 data，Rollback 与观察者都看不到合并值。
func (gs *GraphState) MergeFrom(other *GraphState) error {
	other.mu.RLock()
	keys := make([]string, 0, len(other.data))
	for k := range other.data {
		keys = append(keys, k)
	}
	other.mu.RUnlock()

	gs.mu.Lock()
	var toSet []struct {
		key string
		val any
	}
	for _, k := range keys {
		if _, exists := gs.data[k]; !exists {
			v, _ := other.Get(k)
			toSet = append(toSet, struct {
				key string
				val any
			}{k, v})
		}
	}
	gs.mu.Unlock()

	for _, kv := range toSet {
		gs.SetWithSource(kv.key, kv.val, "merge")
	}
	return nil
}

// ── history & rollback ──────────────────────────────────────

// History returns a copy of the mutation history.
// History 返回变更历史的副本。
func (gs *GraphState) History() []StateEntry {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	result := make([]StateEntry, len(gs.history))
	copy(result, gs.history)
	return result
}

// HistoryBySource returns all state changes made by a specific source.
// HistoryBySource 返回指定来源的所有状态变更。
func (gs *GraphState) HistoryBySource(source string) []StateEntry {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	var result []StateEntry
	for _, entry := range gs.history {
		if entry.Source == source {
			result = append(result, entry)
		}
	}
	return result
}

// Rollback reverts the state to a previous version.
// version is the number of changes to keep (0 = reset to empty, len(history) = no change).
// Returns an error if version is out of range.
//
// Rollback 回滚状态到指定版本。
// version 是保留的变更数（0 重置为空，len(history) 不变）。
func (gs *GraphState) Rollback(version int) error {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	if version < 0 || version > len(gs.history) {
		return fmt.Errorf("graph: rollback version %d out of range [0, %d]", version, len(gs.history))
	}

	// rebuild data from remaining history entries
	gs.data = make(map[string]any)
	for i, entry := range gs.history {
		if i >= version {
			break
		}
		if entry.NewValue == nil && entry.OldValue != nil {
			// was a delete — no-op in reconstruction
			continue
		}
		if entry.NewValue == nil {
			delete(gs.data, entry.Key)
		} else {
			gs.data[entry.Key] = entry.NewValue
		}
	}

	// truncate history
	gs.history = gs.history[:version]
	return nil
}

// Clear removes all data and history.
// Clear 清空所有数据与历史记录。
func (gs *GraphState) Clear() {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	gs.data = make(map[string]any)
	gs.history = nil
	// watchers survive Clear
}

// Watch implements kernel.StateManager.Watch.
// Watch 实现 kernel.StateManager.Watch：注册指定键的变更回调，返回取消函数。
func (gs *GraphState) Watch(keys []string, fn kernel.StateChangeFn) (cancel func()) {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	entry := &graphWatcherEntry{keys: keys, fn: fn}
	gs.watchers = append(gs.watchers, entry)

	cancel = func() {
		gs.mu.Lock()
		defer gs.mu.Unlock()
		for i, w := range gs.watchers {
			if w == entry {
				gs.watchers = append(gs.watchers[:i], gs.watchers[i+1:]...)
				break
			}
		}
	}
	return cancel
}

// collectLocked gathers matching watcher callbacks. Must hold gs.mu; the
// returned callbacks must run AFTER the lock is released (re-entrant
// mutations from watchers would otherwise deadlock).
func (gs *GraphState) collectLocked(changes []kernel.StateChange) []pendingCall {
	if len(gs.watchers) == 0 {
		return nil
	}
	changed := make(map[string]struct{}, len(changes))
	for _, c := range changes {
		changed[c.Key] = struct{}{}
	}
	var pending []pendingCall
	for _, w := range gs.watchers {
		if w.keys == nil {
			pending = append(pending, pendingCall{fn: w.fn, changes: changes})
			continue
		}
		for _, k := range w.keys {
			if _, ok := changed[k]; ok {
				pending = append(pending, pendingCall{fn: w.fn, changes: changes})
				break
			}
		}
	}
	return pending
}

// ── debug / display ─────────────────────────────────────────

// String returns a human-readable summary of the state.
// String 返回状态的人类可读摘要。
func (gs *GraphState) String() string {
	gs.mu.RLock()
	defer gs.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("GraphState(%d keys, %d changes):\n", len(gs.data), len(gs.history)))

	keys := make([]string, 0, len(gs.data))
	for k := range gs.data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		v := gs.data[k]
		switch val := v.(type) {
		case string:
			if len(val) > 80 {
				sb.WriteString(fmt.Sprintf("  %s: (string, len=%d) %q...\n", k, len(val), val[:80]))
			} else {
				sb.WriteString(fmt.Sprintf("  %s: %q\n", k, val))
			}
		case int, float64, bool:
			sb.WriteString(fmt.Sprintf("  %s: %v\n", k, val))
		default:
			sb.WriteString(fmt.Sprintf("  %s: (%T)\n", k, val))
		}
	}
	return sb.String()
}
