package runner

import (
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// StreamHandle provides asynchronous event streaming and final result.
//
// StreamHandle 提供异步事件流和最终结果。
type StreamHandle struct {
	*kernel.AsyncIterator[*types.Event]
	result *kernel.Result
	done   chan struct{}
}

// Result blocks until the streaming run is complete and returns the final result.
// Result 阻塞直到流式运行完成，返回最终结果。
func (h *StreamHandle) Result() *kernel.Result {
	<-h.done
	return h.result
}
