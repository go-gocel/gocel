package state

import "github.com/go-gocel/gocel/core/runtime"

// LocalState is the canonical in-memory StateManager: a type alias for
// core/runtime's default implementation. There is exactly ONE in-memory
// state manager in the framework — the duplicate implementation that used
// to live here (with different watcher semantics and a lock-held-callback
// deadlock risk in the core twin) is gone. Watcher callbacks run outside
// the store lock; a nil or empty key list watches every key.
//
// LocalState 是规范的内存 StateManager：core/runtime 默认实现的类型别名。
// 框架内只存在一个内存状态管理器——原驻留此处的重复实现（与 core 版观察
// 语义不同、且 core 孪生存在锁内回调死锁风险）已删除。观察者回调在锁外
// 执行；nil 或空键列表监听所有键。
type LocalState = runtime.InMemoryState

// NewLocalState creates a new empty LocalState.
// NewLocalState 创建新的空 LocalState。
func NewLocalState() *LocalState {
	return runtime.NewInMemoryState().(*runtime.InMemoryState)
}

// NewLocalStateWithData creates a LocalState pre-populated with the given
// data (copied — later mutation of the source map does not leak in).
//
// NewLocalStateWithData 创建以给定数据预填充的 LocalState（数据被复制——
// 之后修改源 map 不会泄漏进来）。
func NewLocalStateWithData(initial map[string]any) *LocalState {
	return runtime.NewInMemoryStateWithData(initial).(*runtime.InMemoryState)
}
