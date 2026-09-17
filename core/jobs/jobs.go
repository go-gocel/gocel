// Package jobs provides the process-level background-job registry: start,
// incremental output, termination, and settlement — the unified observation
// surface for every long-running piece of work (shell commands today;
// subagents and workflows later). The semantics follow DSH's jobs:
//
//   - Stream jobs expose a single read cursor over their accumulated
//     output (non-blocking incremental reads); final-output jobs expose
//     only their terminal value.
//   - Every job belongs to an owner (session id): listing and termination
//     are fenced by it, and owners have a concurrency cap.
//   - Settlement is reported exactly once (reported bit): a completion
//     notice through the registered notifier, never duplicated.
//   - Teardown force-fails: it only updates registry records — it never
//     waits for the underlying work to stop, and never claims it has.
//
// Package jobs 提供进程级后台作业注册表：启动、增量输出、终止与了结——
// 是一切长时工作（现在是 shell 命令；以后是子代理与工作流）的统一观察面。
// 语义照搬 DSH jobs：
//
//   - stream 作业以单一读游标暴露累积输出（非阻塞增量读）；final-output
//     作业只暴露终值。
//   - 每个作业归属一个 owner（会话 id）：列举与终止受其围栏约束，
//     owner 有并发上限。
//   - 了结只上报一次（reported 位）：经注册的 notifier 发完成通知，
//     绝不重复。
//   - teardown force-fail：只更新注册表记录——绝不等待底层工作停止，
//     也绝不谎称已停止。
package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/types"
)

// Status is the lifecycle phase of a background job.
// Status 是后台作业的生命周期阶段。
type Status string

const (
	// StatusRunning marks a job whose work is executing.
	// StatusRunning 表示作业正在执行。
	StatusRunning Status = "running"
	// StatusStopping marks a job whose termination was requested and whose
	// work is winding down.
	// StatusStopping 表示已请求终止，作业正在收尾。
	StatusStopping Status = "stopping"
	// StatusCompleted marks a job whose work finished successfully.
	// StatusCompleted 表示作业已成功完成。
	StatusCompleted Status = "completed"
	// StatusKilled marks a job whose work was finished by termination.
	// StatusKilled 表示作业因终止而结束。
	StatusKilled Status = "killed"
	// StatusFailed marks a job whose work errored.
	// StatusFailed 表示作业出错。
	StatusFailed Status = "failed"
)

var (
	// ErrJobNotFound is returned when the id is unknown.
	// ErrJobNotFound 是 id 未知时返回的错误。
	ErrJobNotFound = errors.New("jobs: job not found")
	// ErrJobExists is returned when an explicit id is already taken.
	// ErrJobExists 是显式 id 已被占用时返回的错误。
	ErrJobExists = errors.New("jobs: job already exists")
	// ErrJobLimit is returned when the owner's concurrency cap is reached.
	// ErrJobLimit 是 owner 并发上限已达时返回的错误。
	ErrJobLimit = errors.New("jobs: owner concurrency limit reached")
)

// Job is the public projection of one background job.
// Job 是单个后台作业的公开投影。
type Job struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Label    string `json:"label,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Status   Status `json:"status"`
	ExitCode int    `json:"exit_code,omitempty"`
}

// Spec describes one job.
// Spec 描述一个作业。
type Spec struct {
	// ID is an optional explicit id; empty auto-assigns "<kind>-N".
	// Explicit ids must be unique (ErrJobExists).
	ID string
	// Kind names the work family (e.g. "shell"); the id is "<kind>-N".
	Kind string
	// Label is a short human-readable name for notices.
	Label string
	// Owner is the session id the job is fenced by; empty = unowned.
	Owner string
	// StoreCapBytes bounds the stored output (oldest bytes dropped);
	// 0 = the default cap (1MB). Hour-scale jobs must not grow memory
	// unboundedly.
	StoreCapBytes int
	// FinalOutput marks a final-output job: Run returns the whole result at
	// the end; incremental reads are empty until then.
	FinalOutput bool
	// Run executes the work. The context cancels on Kill/Teardown. Stream
	// jobs write incremental output to w; final-output jobs ignore it and
	// return their value as the final content.
	Run func(ctx context.Context, w io.Writer) (string, error)
}

// defaultStoreCap bounds a job's stored output: 1MB of tail.
const defaultStoreCap = 1 << 20

type jobEntry struct {
	job    Job
	cancel context.CancelFunc
	mu     sync.Mutex
	status Status
	output tailStore
	err    error
	// reported is the settlement-notice bit: the notice fires exactly once.
	reported bool
	done     chan struct{}
	once     sync.Once
}

// tailStore keeps the newest cap bytes plus the total ever written, so the
// single-cursor read semantics survive truncation: readers holding an old
// offset get the whole available tail instead of garbage. Safe for
// concurrent Write (the running job) and read (incremental consumers).
type tailStore struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	cap   int
	total int64
}

func newTailStore(cap int) tailStore {
	if cap <= 0 {
		cap = defaultStoreCap
	}
	return tailStore{cap: cap}
}

// Write appends p to the tail buffer, keeping only the newest cap bytes.
// Write 把 p 追加到尾部缓冲，只保留最新的 cap 字节。
func (s *tailStore) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.Write(p)
	s.total += int64(len(p))
	if s.buf.Len() > s.cap {
		excess := s.buf.Len() - s.cap
		data := s.buf.Bytes()
		s.buf.Reset()
		s.buf.Write(data[excess:])
	}
	return len(p), nil
}

// read returns the bytes after since (a previously returned total offset)
// and the new offset.
func (s *tailStore) read(since int) ([]byte, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	buf := s.buf.Bytes()
	total := int(s.total)
	start := since - (total - len(buf))
	if start < 0 {
		start = 0
	}
	if start > len(buf) {
		start = len(buf)
	}
	out := append([]byte(nil), buf[start:]...)
	return out, total
}

// Registry manages background jobs. It is safe for concurrent use.
// Registry 管理后台作业。并发安全。
type Registry struct {
	mu          sync.Mutex
	seq         int
	items       map[string]*jobEntry
	perOwner    map[string]int
	maxPerOwner int
	notifier    func(*types.Notice)

	// doneListeners observe terminal records, exactly once per job
	// (DSH onJobDone). Changed listeners observe visible-set moves at
	// owner granularity (DSH onJobsChanged).
	doneListeners    []func(*Job)
	changedListeners []func(owner string)
}

// NewRegistry creates an empty registry with a default per-owner cap of 10.
// NewRegistry 创建空注册表，默认每 owner 上限为 10。
func NewRegistry() *Registry {
	return &Registry{
		items:       make(map[string]*jobEntry),
		perOwner:    make(map[string]int),
		maxPerOwner: 10,
	}
}

// SetNotifier registers the settlement-notice sink (e.g. a run's
// StreamSender); settlement is reported exactly once per job.
// SetNotifier 注册了结通知汇（如 run 的 StreamSender）；每个作业的了结
// 恰好上报一次。
func (r *Registry) SetNotifier(fn func(*types.Notice)) {
	r.mu.Lock()
	r.notifier = fn
	r.mu.Unlock()
}

// SetMaxPerOwner adjusts the per-owner concurrency cap (0 = default 10).
// SetMaxPerOwner 调整每 owner 并发上限（0 = 默认 10）。
func (r *Registry) SetMaxPerOwner(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 {
		n = 10
	}
	r.maxPerOwner = n
}

// OnDone registers a listener invoked exactly once per settled job with the
// settled snapshot (DSH onJobDone). Listeners run outside the registry lock
// and are never awaited; a throwing listener is contained by the caller's
// contract (the registry does not recover). Returns an unregister function.
//
// OnDone 注册终态监听器：每个作业了结时恰好调用一次，携带了结快照。
// 监听器在注册表锁外执行、不被等待；返回注销函数。
func (r *Registry) OnDone(fn func(*Job)) func() {
	r.mu.Lock()
	r.doneListeners = append(r.doneListeners, fn)
	i := len(r.doneListeners) - 1
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		r.doneListeners[i] = nil
		r.mu.Unlock()
	}
}

// OnChanged registers a listener invoked on visible-set changes — a job
// registration, the stopping transition, settlement, and teardown removal —
// carrying the owner whose set moved, or "" for an unowned job (DSH
// onJobsChanged). Removal is a change no per-job record can express, so
// listeners observe it through this feed. Returns an unregister function.
//
// OnChanged 注册可见集合变更监听器：作业注册、stopping 转换、了结与
// teardown 移除都触发，携带集合发生变化的 owner（无主作业传 ""）。
// 返回注销函数。
func (r *Registry) OnChanged(fn func(owner string)) func() {
	r.mu.Lock()
	r.changedListeners = append(r.changedListeners, fn)
	i := len(r.changedListeners) - 1
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		r.changedListeners[i] = nil
		r.mu.Unlock()
	}
}

// fireChanged notifies the visible-set change listeners with the owner
// whose set moved. Called outside the registry lock.
func (r *Registry) fireChanged(owner string) {
	r.mu.Lock()
	fns := append([]func(string){}, r.changedListeners...)
	r.mu.Unlock()
	for _, fn := range fns {
		if fn != nil {
			fn(owner)
		}
	}
}

// fireDone notifies the settlement listeners with the settled snapshot.
// Called outside the registry lock, exactly once per job (the settle path
// is guarded by the reported bit).
func (r *Registry) fireDone(j *Job) {
	r.mu.Lock()
	fns := append([]func(*Job){}, r.doneListeners...)
	r.mu.Unlock()
	for _, fn := range fns {
		if fn != nil {
			fn(j)
		}
	}
}

// Start launches the job in the background and returns its handle.
// Start 在后台启动作业并返回其句柄。
func (r *Registry) Start(spec Spec) (*Job, error) {
	if spec.Run == nil {
		return nil, errors.New("jobs: nil run function")
	}
	r.mu.Lock()
	if spec.Owner != "" && r.perOwner[spec.Owner] >= r.maxPerOwner {
		r.mu.Unlock()
		return nil, ErrJobLimit
	}
	kind := spec.Kind
	if kind == "" {
		kind = "job"
	}
	id := spec.ID
	if id == "" {
		r.seq++
		id = fmt.Sprintf("%s-%d", kind, r.seq)
	} else if _, exists := r.items[id]; exists {
		r.mu.Unlock()
		return nil, ErrJobExists
	}
	e := &jobEntry{
		job:    Job{ID: id, Kind: kind, Label: spec.Label, Owner: spec.Owner, Status: StatusRunning},
		status: StatusRunning,
		output: newTailStore(spec.StoreCapBytes),
		done:   make(chan struct{}),
	}
	r.items[id] = e
	if spec.Owner != "" {
		r.perOwner[spec.Owner]++
	}
	r.mu.Unlock()
	r.fireChanged(spec.Owner)

	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel

	go func() {
		defer e.once.Do(func() { close(e.done) })
		final, err := spec.Run(ctx, &e.output)
		e.mu.Lock()
		if err != nil {
			if ctx.Err() != nil {
				e.status = StatusKilled
			} else {
				e.status = StatusFailed
				e.err = err
			}
		} else {
			if spec.FinalOutput {
				e.output.buf.Reset()
				e.output.Write([]byte(final))
			}
			e.status = StatusCompleted
		}
		e.mu.Unlock()
		r.settle(e)
		r.release(spec.Owner)
	}()

	return &e.job, nil
}

// settle reports the settlement notice exactly once (reported bit) and
// fans out the terminal snapshot to OnDone listeners plus the visible-set
// change to OnChanged listeners.
func (r *Registry) settle(e *jobEntry) {
	e.mu.Lock()
	if e.reported {
		e.mu.Unlock()
		return
	}
	e.reported = true
	c := e.job
	c.Status = e.status
	label := e.job.Label
	errText := ""
	if e.err != nil {
		errText = e.err.Error()
	}
	e.mu.Unlock()

	r.mu.Lock()
	fn := r.notifier
	r.mu.Unlock()
	if fn != nil {
		fn(&types.Notice{
			Kind:   types.NoticeKindJob,
			ID:     e.job.ID,
			Status: string(c.Status),
			Label:  label,
			Err:    errText,
			At:     time.Now(),
			Meta:   map[string]any{"owner": e.job.Owner},
		})
	}
	r.fireDone(&c)
	r.fireChanged(c.Owner)
}

// release decrements the owner's running count.
func (r *Registry) release(owner string) {
	if owner == "" {
		return
	}
	r.mu.Lock()
	if r.perOwner[owner] > 0 {
		r.perOwner[owner]--
	}
	r.mu.Unlock()
}

// Output returns the bytes written after the caller's cursor position and
// the new cursor — a single-cursor incremental read (DSH semantics). The
// store is bounded: readers holding an offset older than the retained tail
// receive the whole available tail.
// Output 返回调用方游标之后写入的字节与新游标——单游标增量读（DSH 语义）。
// 存储有界：持有早于保留尾部的偏移的读取方会得到整个可用尾部。
func (r *Registry) Output(id string, since int) ([]byte, int, error) {
	e, err := r.lookup(id)
	if err != nil {
		return nil, 0, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out, offset := e.output.read(since)
	return out, offset, nil
}

// Status returns the job's current snapshot.
// Status 返回作业的当前快照。
func (r *Registry) Status(id string) (*Job, error) {
	e, err := r.lookup(id)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	c := e.job
	c.Status = e.status
	return &c, nil
}

// Kill requests termination: status moves to stopping, the run context is
// canceled, and the job settles as killed when the work returns.
// Kill 请求终止：状态转到 stopping，运行上下文被取消；工作返回时作业以
// killed 了结。
func (r *Registry) Kill(id string) error {
	e, err := r.lookup(id)
	if err != nil {
		return err
	}
	e.mu.Lock()
	if e.status != StatusRunning {
		e.mu.Unlock()
		return nil // already settling or settled
	}
	e.status = StatusStopping
	cancel := e.cancel
	e.mu.Unlock()
	r.fireChanged(e.job.Owner)
	if cancel != nil {
		cancel()
	}
	return nil
}

// Wait blocks until the job settles or ctx is done, returning the settled
// snapshot.
// Wait 阻塞直到作业了结或 ctx 结束，返回了结快照。
func (r *Registry) Wait(ctx context.Context, id string) (*Job, error) {
	e, err := r.lookup(id)
	if err != nil {
		return nil, err
	}
	select {
	case <-e.done:
		return r.Status(id)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// List returns the jobs fenced by owner (all jobs when owner is empty).
// Settled jobs remain listed until their owner tears down or the process
// ends — records are honest about what has settled.
// List 返回 owner 围栏内的作业（owner 为空时返回全部）。已了结作业仍被
// 列出，直到其 owner teardown 或进程结束——记录如实反映已了结者。
func (r *Registry) List(owner string) []*Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*Job
	for _, e := range r.items {
		if owner != "" && e.job.Owner != owner {
			continue
		}
		e.mu.Lock()
		c := e.job
		c.Status = e.status
		e.mu.Unlock()
		out = append(out, &c)
	}
	return out
}

// Teardown force-fails an owner's running jobs: it cancels them and marks
// the records as killed WITHOUT waiting — the registry never claims the
// underlying work has stopped, and never blocks on it (DSH teardown
// semantics). Settled records are dropped.
// Teardown 强制失败某 owner 的运行中作业：取消它们并把记录标记为 killed，
// 绝不等待——注册表绝不谎称底层工作已停止，也绝不阻塞（DSH teardown
// 语义）。已了结记录被丢弃。
func (r *Registry) Teardown(owner string) {
	if owner == "" {
		return
	}
	var cancels []context.CancelFunc
	var ids []string
	r.mu.Lock()
	for id, e := range r.items {
		if e.job.Owner != owner {
			continue
		}
		e.mu.Lock()
		if e.status == StatusRunning {
			e.status = StatusKilled
			if e.cancel != nil {
				cancels = append(cancels, e.cancel)
			}
		}
		if e.status == StatusKilled || e.status == StatusCompleted || e.status == StatusFailed {
			ids = append(ids, id)
		}
		e.mu.Unlock()
	}
	delete(r.perOwner, owner)
	for _, id := range ids {
		delete(r.items, id)
	}
	r.mu.Unlock()
	r.fireChanged(owner)
	for _, c := range cancels {
		c()
	}
}

func (r *Registry) lookup(id string) (*jobEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.items[id]
	if !ok {
		return nil, ErrJobNotFound
	}
	return e, nil
}
