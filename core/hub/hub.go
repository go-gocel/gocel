// Package hub provides the process-wide stream event hub: a broadcast
// sink with per-stream sequence replay for late subscribers. It is the
// generic mechanism behind GUI event feeds (SSE), log tails, and
// dashboards — products supply their own event type T and their own wire
// protocol; the hub owns delivery, bounded buffering, and replay.
//
// Semantics (validated by gocode's production StreamHub):
//
//   - Publish delivers to every subscriber without ever blocking the
//     producer. A slow subscriber drops its oldest queued event to make
//     room for the newest — a stalled consumer can never stall the agent.
//   - Events carrying a non-empty StreamID are retained in a bounded
//     per-stream replay buffer (newest kept, oldest dropped). Replay
//     returns the buffered events with Seq > afterSeq in publish order, so
//     a re-attaching client (page refresh, reconnect) catches up; content
//     persisted elsewhere covers everything older than the buffer.
//   - Seq is assigned by the caller — the hub never rewrites event fields.
//     The per-stream watermark discipline (monotonic Seq per stream) is the
//     caller's contract, documented here.
//
// The hub is safe for concurrent use.
//
// Package hub 提供进程级流事件枢纽：广播汇 + 按流 seq 重放，供迟到订阅者
// 补读。它是 GUI 事件馈送（SSE）、日志尾、看板背后的通用机制——产品提供
// 自己的事件类型 T 与线协议；枢纽只负责投递、有界缓冲与重放。
//
// 语义（经 gocode 生产 StreamHub 验证）：
//
//   - Publish 向每个订阅者投递且绝不阻塞生产者。慢订阅者丢弃最旧排队
//     事件为新事件腾位——停摆的消费方绝不会拖停 agent。
//   - 携带非空 StreamID 的事件保留在按流有界重放缓冲中（留新丢旧）。
//     Replay 按发布顺序返回 Seq > afterSeq 的缓冲事件，让重新接入的
//     客户端（页面刷新、重连）补读；缓冲之外更旧的内容由调用方在别处
//     持久化覆盖。
//   - Seq 由调用方赋值——枢纽绝不改写事件字段。按流水位纪律（每流 Seq
//     单调）是调用方契约，记录于此。
//
// 枢纽并发安全。
package hub

import "sync"

// Event is the minimal contract the hub needs from a stream event: an
// optional stream identity and a per-stream sequence number. Products
// embed this in their own event type.
//
// Event 是枢纽对流事件的最小需求：可选流身份与按流序号。产品在自己的
// 事件类型中内嵌它。
type Event interface {
	// StreamID identifies the stream the event belongs to; empty events
	// are broadcast-only (never retained for replay).
	StreamID() string
	// Seq is the per-stream monotonic sequence number (the watermark).
	Seq() int64
}

// Hub broadcasts stream events to subscribers with bounded per-subscriber
// queues and bounded per-stream replay buffers.
//
// Hub 向订阅者广播流事件，带每订阅者有界队列与每流有界重放缓冲。
type Hub[T Event] struct {
	mu   sync.Mutex
	next uint64
	subs map[uint64]chan T
	// recent keeps the newest events per stream (oldest dropped first) for
	// replay to late subscribers; released by Forget when the stream ends.
	recent map[string][]T
	// subBuffer bounds each subscriber queue.
	subBuffer int
	// replayCap bounds each stream's replay buffer.
	replayCap int
}

// Option configures a hub.
// Option 配置 hub。
type Option func(*options)

type options struct {
	subBuffer int
	replayCap int
}

// WithSubscriberBuffer bounds each subscriber's queue (default 512). A full
// subscriber drops its oldest event to make room.
//
// WithSubscriberBuffer 限制每个订阅者队列（默认 512）。满时丢弃最旧
// 事件为新事件腾位。
func WithSubscriberBuffer(n int) Option {
	return func(o *options) { o.subBuffer = n }
}

// WithReplayCap bounds each stream's replay buffer (default 512).
// WithReplayCap 限制每流重放缓冲（默认 512）。
func WithReplayCap(n int) Option {
	return func(o *options) { o.replayCap = n }
}

// New creates an empty hub with default bounds. Bounds <= 0 (unset or
// invalid) fall back to the defaults, mirroring the registry's cap
// discipline — a negative bound must never panic the hub.
//
// New 以默认边界创建空枢纽。<= 0 的边界（未设置或非法）回退默认值，
// 与 registry 的 cap 纪律一致——负边界绝不能让枢纽 panic。
func New[T Event](opts ...Option) *Hub[T] {
	o := &options{subBuffer: 512, replayCap: 512}
	for _, opt := range opts {
		opt(o)
	}
	if o.subBuffer <= 0 {
		o.subBuffer = 512
	}
	if o.replayCap <= 0 {
		o.replayCap = 512
	}
	return &Hub[T]{
		subs:      make(map[uint64]chan T),
		recent:    make(map[string][]T),
		subBuffer: o.subBuffer,
		replayCap: o.replayCap,
	}
}

// Publish delivers ev to every subscriber without blocking. Events with a
// non-empty StreamID are retained in the stream's bounded replay buffer.
//
// Delivery happens under the hub lock: every send is non-blocking (a slow
// subscriber drops its oldest event), and holding the lock makes the
// delivery loop atomic with respect to Unsubscribe — a channel can never be
// closed between the subscriber snapshot and the send (the former
// send-on-closed-channel race panicked the process under contention).
//
// Publish 向每个订阅者投递 ev 且不阻塞。携带非空 StreamID 的事件保留在
// 该流的有界重放缓冲中。
//
// 投递在枢纽锁内完成：每次发送都是非阻塞的（慢订阅者丢弃最旧事件），
// 且持锁使投递循环与退订原子——通道绝不可能在快照与发送之间被关闭
// （旧实现的 send-on-closed-channel 竞态在争用下会 panic 进程）。
func (h *Hub[T]) Publish(ev T) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if sid := ev.StreamID(); sid != "" {
		buf := h.recent[sid]
		buf = append(buf, ev)
		if len(buf) > h.replayCap {
			buf = buf[len(buf)-h.replayCap:]
		}
		h.recent[sid] = buf
	}
	for _, ch := range h.subs {
		select {
		case ch <- ev:
		default:
			// Slow consumer: drop the oldest queued event, keep the newest.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- ev:
			default:
			}
		}
	}
}

// Subscribe registers one subscriber and returns its event channel plus an
// idempotent unsubscribe function. The channel has the configured buffer;
// nothing is replayed into it — use Replay for catch-up.
//
// Subscribe 注册一个订阅者，返回其事件通道与幂等退订函数。通道具有配置
// 的缓冲；不向其重放——补读请用 Replay。
func (h *Hub[T]) Subscribe() (ch <-chan T, unsubscribe func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	id := h.next
	c := make(chan T, h.subBuffer)
	h.subs[id] = c
	var once sync.Once
	return c, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, id)
			close(c)
			h.mu.Unlock()
		})
	}
}

// Replay returns the buffered events of one stream with Seq > afterSeq, in
// publish order. Unknown streams yield nil. The caller's watermark
// discipline (monotonic Seq per stream) makes this a correct catch-up: a
// re-attaching client replays the buffer and continues from the last Seq.
//
// Replay 返回某流中 Seq > afterSeq 的缓冲事件（发布顺序）。未知流返回
// nil。调用方的水位纪律（每流 Seq 单调）使这是正确的补读：重新接入的
// 客户端重放缓冲后从最后 Seq 继续。
func (h *Hub[T]) Replay(streamID string, afterSeq int64) []T {
	if h == nil || streamID == "" {
		return nil
	}
	h.mu.Lock()
	buf, ok := h.recent[streamID]
	if !ok {
		h.mu.Unlock()
		return nil
	}
	buf = append([]T(nil), buf...)
	h.mu.Unlock()
	out := make([]T, 0, len(buf))
	for _, ev := range buf {
		if ev.Seq() > afterSeq {
			out = append(out, ev)
		}
	}
	return out
}

// Forget releases the replay buffer of a finished stream.
// Forget 释放已结束流的重放缓冲。
func (h *Hub[T]) Forget(streamID string) {
	if h == nil || streamID == "" {
		return
	}
	h.mu.Lock()
	delete(h.recent, streamID)
	h.mu.Unlock()
}

// Len returns the number of active subscribers.
// Len 返回活跃订阅者数。
func (h *Hub[T]) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
