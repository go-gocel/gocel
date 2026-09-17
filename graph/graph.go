// Package graph provides a DAG-based orchestration engine for composing
// multiple agents into complex workflows with branching, parallel execution,
// state sharing, and interrupt/resume support.
//
// Graph 包提供基于 DAG 的编排引擎，用于将多个 Agent 组合成
// 复杂工作流，支持分支、并行执行、状态共享和中断恢复。
package graph

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// DefaultMaxSteps is the default MaxSteps value used when building AgentInput
// from graph state and no explicit "max_steps" key is set.
//
// DefaultMaxSteps 是在从 GraphState 构建 AgentInput 时，
// 且未设置显式 "max_steps" 键时使用的默认最大步数。
const DefaultMaxSteps = 10

// NodeID is a unique identifier for a graph node.
// NodeID 是图节点的唯一标识符。
type NodeID string

// Edge connects two nodes in the graph.
// Edge 连接图中的两个节点。
type Edge struct {
	From  NodeID
	To    NodeID
	Label string // used for conditional branching / 用于条件分支
}

// Graph is a DAG-based orchestration engine for composing agents.
// Graph 是基于 DAG 的 Agent 编排引擎。
type Graph struct {
	name  string
	nodes map[NodeID]*graphNode
	edges []Edge
	mu    sync.Mutex

	// indexes built during AddEdge
	outEdges map[NodeID][]Edge
	inEdges  map[NodeID][]Edge
	dirty    bool // true when indexes need rebuilding
}

// graphNode is the internal representation of a graph node.
type graphNode struct {
	id        NodeID
	kind      NodeKind
	agent     kernel.Agent
	agents    []kernel.Agent // for Chain / Parallel
	condition ConditionFn    // for Condition
	transform TransformFn    // for Passthrough

	// execution config
	maxRetries int
	timeout    time.Duration
	retryDelay time.Duration

	// metadata
	description string
}

// ── constructors ────────────────────────────────────────────

// New creates a new empty Graph.
// New 创建一个新的空图。
func New(name string) *Graph {
	return &Graph{
		name:     name,
		nodes:    make(map[NodeID]*graphNode),
		outEdges: make(map[NodeID][]Edge),
		inEdges:  make(map[NodeID][]Edge),
	}
}

// Name returns the graph's name.
// Name 返回图的名称。
func (g *Graph) Name() string { return g.name }

// ── node builders ───────────────────────────────────────────

// AddAgentNode adds a single Agent as a graph node.
// AddAgentNode 将单个 Agent 添加为图节点。
func (g *Graph) AddAgentNode(id NodeID, agent kernel.Agent, opts ...NodeOption) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.nodes[id]; exists {
		return fmt.Errorf("graph: node %q already exists", id)
	}
	n := &graphNode{
		id:    id,
		kind:  NodeKindAgent,
		agent: agent,
	}
	for _, o := range opts {
		o(n)
	}
	g.nodes[id] = n
	g.dirty = true
	return nil
}

// AddChainNode adds a sequential chain of agents as a single graph node.
// AddChainNode 将多个 Agent 的顺序链添加为单个图节点。
func (g *Graph) AddChainNode(id NodeID, agents []kernel.Agent, opts ...NodeOption) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.nodes[id]; exists {
		return fmt.Errorf("graph: node %q already exists", id)
	}
	if len(agents) == 0 {
		return fmt.Errorf("graph: chain node %q requires at least one agent", id)
	}
	n := &graphNode{
		id:     id,
		kind:   NodeKindChain,
		agents: agents,
	}
	for _, o := range opts {
		o(n)
	}
	g.nodes[id] = n
	g.dirty = true
	return nil
}

// AddParallelNode adds a set of agents that execute in parallel as a single graph node.
// All agents share the same input; their outputs are merged.
//
// AddParallelNode 将多个并行执行的 Agent 添加为单个图节点。
// 所有 Agent 共享同一输入，输出合并。
func (g *Graph) AddParallelNode(id NodeID, agents []kernel.Agent, opts ...NodeOption) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.nodes[id]; exists {
		return fmt.Errorf("graph: node %q already exists", id)
	}
	if len(agents) == 0 {
		return fmt.Errorf("graph: parallel node %q requires at least one agent", id)
	}
	n := &graphNode{
		id:     id,
		kind:   NodeKindParallel,
		agents: agents,
	}
	for _, o := range opts {
		o(n)
	}
	g.nodes[id] = n
	g.dirty = true
	return nil
}

// AddConditionNode adds a conditional branching node.
// The ConditionFn returns a label, which determines which outgoing edge to follow.
//
// AddConditionNode 添加条件分支节点。
// ConditionFn 返回标签，决定走哪条出边。
func (g *Graph) AddConditionNode(id NodeID, fn ConditionFn, opts ...NodeOption) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.nodes[id]; exists {
		return fmt.Errorf("graph: node %q already exists", id)
	}
	if fn == nil {
		return fmt.Errorf("graph: condition node %q requires non-nil ConditionFn", id)
	}
	n := &graphNode{
		id:        id,
		kind:      NodeKindCondition,
		condition: fn,
	}
	for _, o := range opts {
		o(n)
	}
	g.nodes[id] = n
	g.dirty = true
	return nil
}

// AddPassthroughNode adds a node that transforms the graph state without calling an LLM.
// This is useful for data preparation, validation, or result post-processing.
//
// AddPassthroughNode 添加透传节点，用于变换 GraphState 但不调用 LLM。
// 适用于数据准备、校验或结果后处理。
func (g *Graph) AddPassthroughNode(id NodeID, fn TransformFn, opts ...NodeOption) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.nodes[id]; exists {
		return fmt.Errorf("graph: node %q already exists", id)
	}
	if fn == nil {
		return fmt.Errorf("graph: passthrough node %q requires non-nil TransformFn", id)
	}
	n := &graphNode{
		id:        id,
		kind:      NodeKindPassthrough,
		transform: fn,
	}
	for _, o := range opts {
		o(n)
	}
	g.nodes[id] = n
	g.dirty = true
	return nil
}

// ── edges ───────────────────────────────────────────────────

// AddEdge adds a directed edge between two nodes.
// The condition-branch label is empty for unconditional edges.
//
// AddEdge 在两个节点之间添加有向边。
// 无条件边的 label 为空字符串。
func (g *Graph) AddEdge(from, to NodeID) error {
	return g.AddLabeledEdge(from, to, "")
}

// AddLabeledEdge adds a directed edge with a label (used for conditional branching).
// AddLabeledEdge 添加带标签的有向边（用于条件分支）。
func (g *Graph) AddLabeledEdge(from, to NodeID, label string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.nodes[from]; !exists {
		return fmt.Errorf("graph: source node %q does not exist", from)
	}
	if _, exists := g.nodes[to]; !exists {
		return fmt.Errorf("graph: target node %q does not exist", to)
	}

	// prevent duplicate edges
	for _, e := range g.edges {
		if e.From == from && e.To == to && e.Label == label {
			return nil // already exists / 已存在
		}
	}

	g.edges = append(g.edges, Edge{From: from, To: to, Label: label})
	g.outEdges[from] = append(g.outEdges[from], Edge{From: from, To: to, Label: label})
	g.inEdges[to] = append(g.inEdges[to], Edge{From: from, To: to, Label: label})
	g.dirty = true
	return nil
}

// ── compile ─────────────────────────────────────────────────

// Compile validates the graph, topologically sorts nodes,
// and returns a CompiledGraph ready for execution.
//
// Compile 验证图、拓扑排序节点，返回可执行的 CompiledGraph。
func (g *Graph) Compile(ctx context.Context) (*CompiledGraph, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if len(g.nodes) == 0 {
		return nil, fmt.Errorf("graph: no nodes defined")
	}

	// rebuild indexes if needed
	if g.dirty {
		g.rebuildIndexes()
	}

	// topological sort (Kahn's algorithm)
	order, err := g.topoSort()
	if err != nil {
		return nil, fmt.Errorf("graph: compile %q: %w", g.name, err)
	}

	// compute in-degree for execution tracking
	inDegree := make(map[NodeID]int, len(g.nodes))
	for nid := range g.nodes {
		inDegree[nid] = len(g.inEdges[nid])
	}

	cg := &CompiledGraph{
		g:         g,
		topoOrder: order,
		inDegree:  inDegree,
		outEdges:  g.outEdges,
		inEdges:   g.inEdges,
	}

	return cg, nil
}

// rebuildIndexes rebuilds the edge index maps.
func (g *Graph) rebuildIndexes() {
	g.outEdges = make(map[NodeID][]Edge, len(g.nodes))
	g.inEdges = make(map[NodeID][]Edge, len(g.nodes))
	for nid := range g.nodes {
		g.outEdges[nid] = nil
		g.inEdges[nid] = nil
	}
	for _, e := range g.edges {
		g.outEdges[e.From] = append(g.outEdges[e.From], e)
		g.inEdges[e.To] = append(g.inEdges[e.To], e)
	}
	g.dirty = false
}

// topoSort performs Kahn's algorithm for topological ordering.
// Returns error if a cycle is detected.
func (g *Graph) topoSort() ([]NodeID, error) {
	inDegree := make(map[NodeID]int, len(g.nodes))
	for nid := range g.nodes {
		inDegree[nid] = len(g.inEdges[nid])
	}

	var order []NodeID
	var queue []NodeID

	// start with nodes that have no incoming edges
	for nid := range g.nodes {
		if inDegree[nid] == 0 {
			queue = append(queue, nid)
		}
	}

	for len(queue) > 0 {
		nid := queue[0]
		queue = queue[1:]
		order = append(order, nid)

		for _, e := range g.outEdges[nid] {
			inDegree[e.To]--
			if inDegree[e.To] == 0 {
				queue = append(queue, e.To)
			}
		}
	}

	if len(order) != len(g.nodes) {
		// there's a cycle — find unprocessed nodes for the error message
		remaining := make([]NodeID, 0)
		for nid := range g.nodes {
			if inDegree[nid] > 0 {
				remaining = append(remaining, nid)
			}
		}
		return nil, fmt.Errorf("cycle detected involving nodes: %v", remaining)
	}

	return order, nil
}

// ── CompiledGraph ───────────────────────────────────────────

// CompiledGraph is a validated, ready-to-execute graph.
// CompiledGraph 是已验证、可执行的图。
type CompiledGraph struct {
	g         *Graph
	topoOrder []NodeID
	inDegree  map[NodeID]int
	outEdges  map[NodeID][]Edge
	inEdges   map[NodeID][]Edge
}

// nodeResult carries the execution result of a single node.
type nodeResult struct {
	id    NodeID
	state *GraphState
	err   error
}

// Run executes the compiled graph synchronously.
// It returns the final graph result containing all messages and state.
//
// Run 同步执行编译后的图，返回包含所有消息和状态的最终结果。
func (cg *CompiledGraph) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	if input == nil {
		input = &types.AgentInput{}
	}
	state := NewGraphState()

	// seed initial state from input
	if len(input.Messages) > 0 {
		state.SetWithSource("messages", input.Messages, "input")
	}
	if input.SystemPrompt != "" {
		state.SetWithSource("system_prompt", input.SystemPrompt, "input")
	}
	if input.Meta != nil {
		state.SetWithSource("meta", input.Meta, "input")
	}
	if input.MaxSteps > 0 {
		state.SetWithSource("max_steps", input.MaxSteps, "input")
	}

	finalState, firstErr := cg.runLoop(ctx, state, rt)

	// build final result
	allMsgs := cg.collectMessages(finalState)

	if firstErr != nil {
		return &kernel.Result{
			Messages: allMsgs,
			Err:      fmt.Errorf("graph %q: %w", cg.g.name, firstErr),
		}
	}

	content := ""
	if finalState != nil {
		if v, ok := finalState.Get("output"); ok {
			if s, ok := v.(string); ok {
				content = s
			}
		}
	}

	return &kernel.Result{
		Content:  content,
		Messages: allMsgs,
	}
}

// runLoop manages the graph execution lifecycle: setting up channels and tracking,
// starting entry nodes, running the main event loop (node completion → downstream
// dispatch), and draining remaining results on error or cancellation.
//
// runLoop 管理图执行的生命周期：初始化通道和跟踪状态、启动入口节点、
// 运行主事件循环（节点完成 → 下游调度）、在错误或取消时排空剩余结果。
func (cg *CompiledGraph) runLoop(ctx context.Context, initialState *GraphState, rt kernel.Runtime) (*GraphState, error) {
	s := cg.newScheduler(ctx, rt)
	s.startEntries(initialState)

	// ── execution loop ──
	var firstErr error
	var finalState *GraphState

	for s.running > 0 {
		select {
		case <-s.ctx.Done():
			// context cancelled (error or timeout in another node)
			drainResults(s.resultCh, &s.running, &firstErr, &finalState)
			if firstErr == nil {
				firstErr = s.ctx.Err()
			}
			return finalState, firstErr
		case res := <-s.resultCh:
			s.stateMu.Lock()
			s.running--
			s.stateMu.Unlock()
			if res.err != nil {
				if firstErr == nil {
					firstErr = res.err
				}
				s.cancel()
				// drain whatever is already available, then return: nodes that
				// ignore cancellation must not hang the graph forever.
				drainResults(s.resultCh, &s.running, &firstErr, &finalState)
				return finalState, firstErr
			}

			// success — update state and start downstream nodes
			s.stateMu.Lock()
			s.nodeStates[res.id] = res.state
			s.stateMu.Unlock()
			finalState = res.state
			s.complete(res.id)
		} // end select
	} // end for running
	close(s.resultCh)
	return finalState, firstErr
}

// graphScheduler owns the readiness state of one graph run: in-degree
// counters, condition gates, and the tryStart/skip/complete transitions.
type graphScheduler struct {
	cg         *CompiledGraph
	ctx        context.Context
	cancel     context.CancelFunc
	rt         kernel.Runtime
	resultCh   chan nodeResult
	running    int
	stateMu    sync.Mutex
	nodeStates map[NodeID]*GraphState

	// remaining counts non-condition predecessor edges; condition edges
	// are gated at runtime (condPending/condPassed) instead. condPending
	// counts condition predecessors not yet resolved (chosen OR unchosen);
	// condPassed counts those that actually selected this node's branch.
	// A node runs when all non-condition predecessors are done AND all
	// condition predecessors are resolved AND (at least one passed OR it
	// has a non-condition predecessor) — an unchosen branch alone never
	// blocks a join node that still has live work (verified defect).
	remaining   map[NodeID]int
	condPending map[NodeID]int
	condPassed  map[NodeID]int
	skipped     map[NodeID]bool
}

func (cg *CompiledGraph) newScheduler(ctx context.Context, rt kernel.Runtime) *graphScheduler {
	s := &graphScheduler{
		cg:          cg,
		rt:          rt,
		resultCh:    make(chan nodeResult, len(cg.g.nodes)),
		nodeStates:  make(map[NodeID]*GraphState),
		remaining:   make(map[NodeID]int, len(cg.g.nodes)),
		condPending: make(map[NodeID]int, len(cg.g.nodes)),
		condPassed:  make(map[NodeID]int, len(cg.g.nodes)),
		skipped:     make(map[NodeID]bool),
	}
	for nid, edges := range cg.inEdges {
		cond := 0
		for _, e := range edges {
			if n, ok := cg.g.nodes[e.From]; ok && n.kind == NodeKindCondition {
				cond++
			}
		}
		s.condPending[nid] = cond
		s.remaining[nid] = len(edges) - cond
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	return s
}

// startEntries dispatches every entry node (in-degree zero).
func (s *graphScheduler) startEntries(initialState *GraphState) {
	s.stateMu.Lock()
	for _, nid := range s.cg.topoOrder {
		if s.cg.inDegree[nid] == 0 {
			s.nodeStates[nid] = initialState
			s.running++
			go s.cg.executeNode(s.ctx, nid, initialState, s.rt, s.resultCh)
		}
	}
	s.stateMu.Unlock()
}

// tryStart dispatches a node once its non-condition predecessors are done
// and its condition predecessors are all resolved. A node reachable only
// through unchosen condition branches is skipped (cascade).
func (s *graphScheduler) tryStart(nid NodeID) {
	if s.skipped[nid] {
		return
	}
	if s.remaining[nid] > 0 || s.condPending[nid] > 0 {
		return
	}
	// All predecessors resolved; if every predecessor was a condition edge
	// and none passed, this subtree is unreachable — skip it.
	if s.condPassed[nid] == 0 && len(s.cg.inEdges[nid]) == s.condInCount(nid) && len(s.cg.inEdges[nid]) > 0 {
		s.skip(nid)
		return
	}
	s.running++
	parentState := s.cg.collectPredecessorState(nid, s.nodeStates)
	go s.cg.executeNode(s.ctx, nid, parentState, s.rt, s.resultCh)
}

// condInCount returns the number of condition predecessors of nid.
func (s *graphScheduler) condInCount(nid NodeID) int {
	cond := 0
	for _, e := range s.cg.inEdges[nid] {
		if n, ok := s.cg.g.nodes[e.From]; ok && n.kind == NodeKindCondition {
			cond++
		}
	}
	return cond
}

// skip virtually completes a node that will never execute (its branch was
// not chosen). Its outgoing edges flow through as if it had finished, so
// downstream join nodes are not blocked forever by an unchosen branch. A
// node with pending predecessors is never skipped — the cascade applies
// only to fully-unreachable subtrees (verified defect: mixed joins were
// skipped while a live predecessor was still running).
func (s *graphScheduler) skip(nid NodeID) {
	if s.skipped[nid] {
		return
	}
	if s.remaining[nid] > 0 || s.condPending[nid] > 0 {
		return
	}
	s.skipped[nid] = true
	n, _ := s.cg.g.nodes[nid]
	for _, e := range s.cg.outEdges[nid] {
		if n != nil && n.kind == NodeKindCondition {
			// A skipped condition node never resolves its branch: its
			// out-edges count as unchosen for downstream gating.
			s.condPending[e.To]--
		} else {
			s.remaining[e.To]--
		}
		s.tryStart(e.To)
	}
}

// complete signals that a node has finished; it decrements downstream
// in-degrees and dispatches the next ready node. For join nodes (diamond
// pattern), predecessor states are merged before dispatch. Condition nodes
// (and interrupt passthrough nodes that recorded a branch label) propagate
// only along the edge whose label matches the chosen branch.
func (s *graphScheduler) complete(doneID NodeID) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	// Resolve the chosen branch for condition nodes and branch-routing
	// interrupt nodes.
	chosen := ""
	isCond := false
	if n, ok := s.cg.g.nodes[doneID]; ok {
		if n.kind == NodeKindCondition || s.hasLabeledOut(doneID) {
			isCond = true
			if st, ok := s.nodeStates[doneID]; ok {
				if v, ok := st.Get("branch"); ok {
					chosen, _ = v.(string)
				}
			}
		}
	}

	for _, e := range s.cg.outEdges[doneID] {
		if isCond {
			if e.Label != chosen {
				// The unchosen branch is resolved (never blocks), but the
				// target may still have other live predecessors — tryStart
				// decides run vs skip.
				s.condPending[e.To]--
				s.tryStart(e.To)
				continue
			}
			s.condPassed[e.To]++
			s.condPending[e.To]--
			s.tryStart(e.To)
			continue
		}
		s.remaining[e.To]--
		s.tryStart(e.To)
	}
}

// hasLabeledOut reports whether the node has any labeled out-edge — the
// signal that it routes by branch (interrupt nodes).
func (s *graphScheduler) hasLabeledOut(nid NodeID) bool {
	for _, e := range s.cg.outEdges[nid] {
		if e.Label != "" {
			return true
		}
	}
	return false
}

// drainResults drains remaining node results after cancellation or error.
// It exits as soon as the context is done so a node that ignores
// cancellation cannot hang the graph forever; its goroutine finishes on
// its own and the buffered result channel absorbs the outcome.
func drainResults(resultCh <-chan nodeResult, running *int, firstErr *error, finalState **GraphState) {
	for *running > 0 {
		select {
		case res := <-resultCh:
			*running--
			if res.err != nil && *firstErr == nil {
				*firstErr = res.err
			}
			if *finalState == nil {
				*finalState = res.state
			}
		default:
			// No result is ready right now. The context is already canceled
			// (we only drain after cancel/error), so instead of blocking on
			// a node that may never return, stop draining and let the
			// goroutines finish on their own.
			return
		}
	}
}

// collectPredecessorState returns the appropriate parent state for a target node.
// For nodes with a single predecessor, it returns that predecessor's state directly.
// For join nodes (multiple predecessors, diamond pattern), it merges all predecessor
// states into a new state with first-write-wins conflict resolution.
//
// collectPredecessorState 返回目标节点的父状态。对于单一前驱节点直接返回其状态；
// 对于合并节点（多个前驱，钻石模式），合并所有前驱状态，冲突时保留先写入的值。
func (cg *CompiledGraph) collectPredecessorState(nid NodeID, nodeStates map[NodeID]*GraphState) *GraphState {
	preds := cg.inEdges[nid]
	if len(preds) == 0 {
		return nil
	}
	if len(preds) == 1 {
		if s, ok := nodeStates[preds[0].From]; ok {
			return s
		}
		return nil
	}

	// Join node: merge all predecessor states, first-write-wins for conflicts.
	var merged *GraphState
	for _, p := range preds {
		s, ok := nodeStates[p.From]
		if !ok {
			continue
		}
		if merged == nil {
			merged = s.Fork()
		} else {
			merged.MergeFrom(s)
		}
	}
	return merged
}

// executeNode runs a single graph node.
func (cg *CompiledGraph) executeNode(ctx context.Context, nid NodeID, parentState *GraphState, rt kernel.Runtime, ch chan<- nodeResult) {
	n, ok := cg.g.nodes[nid]
	if !ok {
		ch <- nodeResult{id: nid, err: fmt.Errorf("graph: node %q not found", nid)}
		return
	}
	if parentState == nil {
		parentState = NewGraphState()
	}

	// create child state (fork from parent)
	nodeState := parentState.Fork()

	execCtx := ctx
	var cancel context.CancelFunc
	if n.timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, n.timeout)
		defer cancel()
	}

	// retry loop
	var lastErr error
	for attempt := 0; attempt <= n.maxRetries; attempt++ {
		if attempt > 0 && n.retryDelay > 0 {
			select {
			case <-time.After(n.retryDelay):
			case <-execCtx.Done():
				ch <- nodeResult{id: nid, state: nodeState, err: execCtx.Err()}
				return
			}
		}

		err := cg.runNodeKind(execCtx, n, nodeState, rt)
		if err == nil {
			ch <- nodeResult{id: nid, state: nodeState}
			return
		}
		lastErr = err

		// non-retryable error — the single authority is kernel.IsRetryableError
		// (wrapped errors classify correctly, unlike the old local copy that
		// only recognized the bare Retryable() interface)
		if !kernel.IsRetryableError(err) {
			break
		}
	}

	ch <- nodeResult{id: nid, state: nodeState, err: lastErr}
}

// runNodeKind dispatches execution based on node type.
func (cg *CompiledGraph) runNodeKind(ctx context.Context, n *graphNode, state *GraphState, rt kernel.Runtime) error {
	switch n.kind {
	case NodeKindAgent:
		return cg.runAgentNode(ctx, n, state, rt)
	case NodeKindChain:
		return cg.runChainNode(ctx, n, state, rt)
	case NodeKindParallel:
		return cg.runParallelNode(ctx, n, state, rt)
	case NodeKindCondition:
		return cg.runConditionNode(ctx, n, state)
	case NodeKindPassthrough:
		return cg.runPassthroughNode(ctx, n, state)
	default:
		return fmt.Errorf("graph: unknown node kind %v for node %q", n.kind, n.id)
	}
}

// runAgentNode executes a single Agent.
func (cg *CompiledGraph) runAgentNode(ctx context.Context, n *graphNode, state *GraphState, rt kernel.Runtime) error {
	input := buildAgentInput(state)
	result := n.agent.Run(ctx, input, rt)
	if result.Err != nil {
		return result.Err
	}
	state.SetWithSource("output", result.Content, string(n.id))
	state.SetWithSource("messages", result.Messages, string(n.id))
	if result.TokenUsage != nil {
		state.SetWithSource("token_usage", result.TokenUsage, string(n.id))
	}
	return nil
}

// runChainNode executes agents sequentially, chaining outputs to inputs.
func (cg *CompiledGraph) runChainNode(ctx context.Context, n *graphNode, state *GraphState, rt kernel.Runtime) error {
	for i, agent := range n.agents {
		input := buildAgentInput(state)
		result := agent.Run(ctx, input, rt)
		if result.Err != nil {
			return fmt.Errorf("graph: chain node %q step %d: %w", n.id, i, result.Err)
		}
		state.SetWithSource("output", result.Content, string(n.id))
		state.SetWithSource("messages", result.Messages, string(n.id))
	}
	return nil
}

// runParallelNode executes agents concurrently and merges results.
func (cg *CompiledGraph) runParallelNode(ctx context.Context, n *graphNode, state *GraphState, rt kernel.Runtime) error {
	type agentResult struct {
		index int
		msg   string
		err   error
	}

	agents := n.agents
	results := make(chan agentResult, len(agents))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for i, agent := range agents {
		i, agent := i, agent
		go func() {
			input := buildAgentInput(state)
			result := agent.Run(ctx, input, rt)
			if result.Err != nil {
				results <- agentResult{index: i, err: result.Err}
				return
			}
			results <- agentResult{index: i, msg: result.Content}
		}()
	}

	var firstErr error
	outputs := make([]string, len(agents))
	for range agents {
		select {
		case res := <-results:
			if res.err != nil && firstErr == nil {
				firstErr = res.err
				cancel()
			}
			outputs[res.index] = res.msg
		case <-ctx.Done():
			// An agent ignored cancellation — stop waiting for it; the
			// buffered channel absorbs its eventual result.
			if firstErr == nil {
				firstErr = ctx.Err()
			}
			return firstErr
		}
	}

	if firstErr != nil {
		return firstErr
	}

	// merge outputs into state
	state.SetWithSource("outputs", outputs, string(n.id))
	// set a combined output
	combined := ""
	for i, o := range outputs {
		if i > 0 {
			combined += "\n---\n"
		}
		combined += o
	}
	state.SetWithSource("output", combined, string(n.id))
	return nil
}

// runConditionNode evaluates the condition function and stores the chosen label in state.
// The chosen label must match one of the node's outgoing edges; otherwise the
// graph stops with an error.
func (cg *CompiledGraph) runConditionNode(ctx context.Context, n *graphNode, state *GraphState) error {
	label, err := n.condition(ctx, state)
	if err != nil {
		return fmt.Errorf("graph: condition node %q: %w", n.id, err)
	}
	matched := false
	for _, e := range cg.outEdges[n.id] {
		if e.Label == label {
			matched = true
			break
		}
	}
	if !matched {
		return fmt.Errorf("graph: condition node %q chose branch %q but no matching edge exists", n.id, label)
	}
	state.SetWithSource("branch", label, string(n.id))
	state.SetWithSource("output", label, string(n.id))
	return nil
}

// runPassthroughNode applies the transform function to state.
func (cg *CompiledGraph) runPassthroughNode(ctx context.Context, n *graphNode, state *GraphState) error {
	if err := n.transform(ctx, state); err != nil {
		return fmt.Errorf("graph: passthrough node %q: %w", n.id, err)
	}
	return nil
}

// buildAgentInput constructs an AgentInput from graph state.
func buildAgentInput(state *GraphState) *types.AgentInput {
	input := &types.AgentInput{
		Messages:        []*types.Message{},
		MaxSteps:        DefaultMaxSteps,
		EnableStreaming: false,
	}

	if v, ok := state.Get("messages"); ok {
		if msgs, ok := v.([]*types.Message); ok {
			input.Messages = msgs
		}
	}
	if v, ok := state.Get("system_prompt"); ok {
		if s, ok := v.(string); ok {
			input.SystemPrompt = s
		}
	}
	if v, ok := state.Get("meta"); ok {
		if m, ok := v.(map[string]any); ok {
			input.Meta = m
		}
	}
	if v, ok := state.Get("max_steps"); ok {
		if s, ok := v.(int); ok {
			input.MaxSteps = s
		}
	}

	return input
}

// collectMessages gathers all messages from the final state.
func (cg *CompiledGraph) collectMessages(state *GraphState) []*types.Message {
	if state == nil {
		return nil
	}
	if v, ok := state.Get("messages"); ok {
		if msgs, ok := v.([]*types.Message); ok {
			return msgs
		}
	}
	return nil
}

// isRetryableKernelError delegates to kernel.IsRetryableError — the
// framework's single retryability authority. The local copy that only
// recognized the bare Retryable() interface (making fmt.Errorf-wrapped
// retryable errors non-retryable and WithNodeMaxRetries ineffective) is
// gone.
func isRetryableKernelError(err error) bool {
	return kernel.IsRetryableError(err)
}
