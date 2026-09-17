package kernel

// ❄️ FROZEN — Stable API. Must not change method signatures.
//
// StateManager provides runtime state management with change observation.
// It replaces raw map[string]any for cross-module, cross-agent, and graph-level state sharing.
//
// StateManager 提供运行时状态管理及变更观察能力。
// 用于跨模块、跨 Agent 和图级状态共享，替代原生的 map[string]any。
type StateManager interface {
	// Get returns the value for a key. Returns nil, false if not found.
	Get(key string) (any, bool)

	// Set stores a value for a key.
	Set(key string, value any)

	// Delete removes a key from the state.
	Delete(key string)

	// Keys returns all keys in the state (order unspecified).
	Keys() []string

	// Snapshot returns a shallow copy of all key-value pairs.
	Snapshot() map[string]any

	// Clear removes all keys and data.
	Clear()

	// Watch registers a callback that fires when any of the specified keys change.
	// If keys is empty/nil, the callback fires for every change.
	// Returns a cancel function to unregister the watcher.
	Watch(keys []string, fn StateChangeFn) (cancel func())
}

// StateChangeFn is a callback that receives a batch of state changes.
//
// StateChangeFn 是接收一批状态变更的回调函数。
type StateChangeFn func(changes []StateChange)

// StateChange records a single state mutation for observation.
//
// StateChange 记录单次状态变更，供观察者使用。
type StateChange struct {
	Key      string
	OldValue any
	NewValue any
}
