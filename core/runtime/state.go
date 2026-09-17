package runtime

import (
	"sync"

	"github.com/go-gocel/gocel/core/kernel"
)

// InMemoryState is the default StateManager implementation: a
// concurrency-safe key-value store with change observation. It is the
// canonical default for Runtime.State() — shared across modules, agents,
// and graphs during a run. Watcher callbacks run OUTSIDE the store lock
// (re-entrant mutations are safe); a nil OR empty key list watches every
// key.
//
// InMemoryState 是默认 StateManager 实现：并发安全的键值存储 + 变更观察。
// 它是 Runtime.State() 的标准默认实现——在运行期间跨模块、跨 Agent、跨图
// 共享。观察者回调在锁外执行（可重入变更安全）；nil 或空键列表监听所有键。
type InMemoryState struct {
	mu       sync.RWMutex
	data     map[string]any
	watchers []*stateWatcher
}

type stateWatcher struct {
	keys []string
	fn   kernel.StateChangeFn
}

type pendingCall struct {
	fn      kernel.StateChangeFn
	changes []kernel.StateChange
}

// NewInMemoryState creates a default StateManager.
// NewInMemoryState 创建默认 StateManager。
func NewInMemoryState() kernel.StateManager {
	return &InMemoryState{data: make(map[string]any)}
}

// NewInMemoryStateWithData creates a StateManager pre-populated with the
// given data (copied — later mutation of the source map does not leak in).
//
// NewInMemoryStateWithData 创建预填充数据的 StateManager（复制——源 map 的
// 后续变更不会渗入）。
func NewInMemoryStateWithData(initial map[string]any) kernel.StateManager {
	data := make(map[string]any, len(initial))
	for k, v := range initial {
		data[k] = v
	}
	return &InMemoryState{data: data}
}

// Get returns the value stored under key and whether the key exists.
// Get 返回 key 下存储的值，以及该键是否存在。
func (s *InMemoryState) Get(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[key]
	return v, ok
}

// Set stores value under key and notifies matching watchers once the
// lock is released.
// Set 将 value 存入 key，并在锁释放后通知匹配的观察者。
func (s *InMemoryState) Set(key string, value any) {
	s.mu.Lock()
	old := s.data[key]
	s.data[key] = value
	pending := s.collectLocked([]kernel.StateChange{{Key: key, OldValue: old, NewValue: value}})
	s.mu.Unlock()
	for _, p := range pending {
		p.fn(p.changes)
	}
}

// Delete removes key from the store and notifies matching watchers when
// the key existed.
// Delete 从存储中删除 key；若该键存在则通知匹配的观察者。
func (s *InMemoryState) Delete(key string) {
	s.mu.Lock()
	old, ok := s.data[key]
	if ok {
		delete(s.data, key)
	}
	pending := s.collectLocked([]kernel.StateChange{{Key: key, OldValue: old, NewValue: nil}})
	s.mu.Unlock()
	if ok {
		for _, p := range pending {
			p.fn(p.changes)
		}
	}
}

// Keys returns all stored keys in no particular order.
// Keys 返回所有已存储的键，顺序不保证。
func (s *InMemoryState) Keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.data))
	for k := range s.data {
		keys = append(keys, k)
	}
	return keys
}

// Snapshot returns a copy of the entire store.
// Snapshot 返回整个存储的一份副本。
func (s *InMemoryState) Snapshot() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]any, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

// Clear removes all keys from the store.
// Clear 清空存储中的所有键。
func (s *InMemoryState) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = make(map[string]any)
}

// Watch registers fn to observe changes to the given keys (a nil or
// empty key list observes every key) and returns a cancel function to
// stop observing.
// Watch 注册 fn 监听指定键的变更（nil 或空键列表监听所有键），并返回
// 用于停止监听的取消函数。
func (s *InMemoryState) Watch(keys []string, fn kernel.StateChangeFn) (cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := &stateWatcher{keys: keys, fn: fn}
	s.watchers = append(s.watchers, w)
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, e := range s.watchers {
			if e == w {
				s.watchers = append(s.watchers[:i], s.watchers[i+1:]...)
				break
			}
		}
	}
}

// collectLocked gathers matching watcher callbacks. Must hold s.mu; the
// returned callbacks must run AFTER the lock is released (re-entrant
// mutations from watchers would otherwise deadlock).
func (s *InMemoryState) collectLocked(changes []kernel.StateChange) []pendingCall {
	if len(s.watchers) == 0 {
		return nil
	}
	changed := make(map[string]struct{}, len(changes))
	for _, c := range changes {
		changed[c.Key] = struct{}{}
	}
	var pending []pendingCall
	for _, w := range s.watchers {
		if len(w.keys) == 0 {
			// nil OR empty key list = watch everything.
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
