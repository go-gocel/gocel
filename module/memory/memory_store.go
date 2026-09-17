package memory

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// SummaryNode represents a node in the hierarchical summary tree.
// The tree is structured as:
//
//	Level 0: single interaction summaries (leaf nodes)
//	Level 1: group of ~5 interaction summaries
//	Level 2: group of ~5 group summaries (25 interactions)
//	Level 3+: further aggregation
//
// SummaryNode 表示层级摘要树中的一个节点：第 0 层为单条交互摘要（叶子），
// 更高层为下层摘要的聚合。
type SummaryNode struct {
	// Summary is the condensed text for this node.
	Summary string

	// Level indicates the aggregation depth (0 = leaf/interaction-level).
	Level int

	// Children contains the sub-nodes that were aggregated into this node.
	Children []*SummaryNode

	// StartIndex and EndIndex indicate the range of interaction indices
	// covered by this node (inclusive-exclusive).
	StartIndex int
	EndIndex   int
}

// MemoryStore provides hierarchical summary storage for the MemoryModule.
// It maintains a tree of summaries at increasing levels of abstraction,
// allowing flexible retrieval: some recent interactions can be kept in full,
// while older ones are represented by increasingly condensed summaries.
//
// MemoryStore 为 MemoryModule 提供层级化摘要存储：维护一棵抽象层次递增的摘要树，
// 最近的交互可完整保留，更旧的交互用逐渐浓缩的摘要表示。
type MemoryStore struct {
	mu sync.RWMutex

	// roots contains the top-level summary nodes, one per aggregation group.
	roots []*SummaryNode

	// groupSize controls how many children are merged into one parent (default 5).
	groupSize int
}

// NewMemoryStore creates an empty MemoryStore.
//
// NewMemoryStore 创建一个空的 MemoryStore。
func NewMemoryStore(groupSize int) *MemoryStore {
	if groupSize <= 1 {
		groupSize = 5
	}
	return &MemoryStore{
		groupSize: groupSize,
	}
}

// Insert adds a leaf-level summary (for a single interaction) to the store.
// It returns the created SummaryNode.
//
// Insert 向存储中添加一条叶子级摘要（对应单条交互），并返回创建的 SummaryNode。
func (ms *MemoryStore) Insert(ctx context.Context, summary string, index int) *SummaryNode {
	node := &SummaryNode{
		Summary:    summary,
		Level:      0,
		StartIndex: index,
		EndIndex:   index + 1,
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	// Find the right root for appending
	ms.roots = append(ms.roots, node)

	// Try to rebuild aggregation if we have enough leaf nodes
	ms.rebuildLocked(ctx)

	return node
}

// InsertBatch inserts multiple leaf summaries at once.
//
// InsertBatch 一次性插入多条叶子级摘要。
func (ms *MemoryStore) InsertBatch(ctx context.Context, summaries []string, startIndex int) []*SummaryNode {
	nodes := make([]*SummaryNode, len(summaries))
	for i, s := range summaries {
		nodes[i] = &SummaryNode{
			Summary:    s,
			Level:      0,
			StartIndex: startIndex + i,
			EndIndex:   startIndex + i + 1,
		}
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	ms.roots = append(ms.roots, nodes...)
	ms.rebuildLocked(ctx)

	return nodes
}

// GetRoots returns all root-level summary nodes.
//
// GetRoots 返回所有根级摘要节点。
func (ms *MemoryStore) GetRoots() []*SummaryNode {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	result := make([]*SummaryNode, len(ms.roots))
	copy(result, ms.roots)
	return result
}

// BuildContext builds a string representation of the summary tree
// for injection into the system prompt. The representation uses the
// highest-level summaries to minimize token usage.
// maxLevel: maximum level to include (0 = only leaf summaries).
//
// BuildContext 生成摘要树的字符串表示以注入系统提示词，使用最高层摘要以
// 最小化 token 用量；maxLevel 为包含的最大层级（0 = 仅叶子摘要）。
func (ms *MemoryStore) BuildContext(maxLevel int) string {
	ms.mu.RLock()
	roots := ms.roots
	ms.mu.RUnlock()

	if len(roots) == 0 {
		return ""
	}

	// Find the deepest level we need
	if maxLevel < 0 {
		maxLevel = ms.maxLevelInternal(roots)
	}

	var sb strings.Builder
	sb.WriteString("[Conversation History]\n")
	ms.writeNodes(&sb, roots, maxLevel, 0)
	return sb.String()
}

// Clear removes all summaries.
//
// Clear 移除所有摘要。
func (ms *MemoryStore) Clear() {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.roots = nil
}

// Len returns the total number of leaf summaries stored.
//
// Len 返回存储的叶子级摘要总数。
func (ms *MemoryStore) Len() int {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	count := 0
	for _, r := range ms.roots {
		count += countLeaves(r)
	}
	return count
}

// --- internal ---

func (ms *MemoryStore) rebuildLocked(ctx context.Context) {
	_ = ctx // reserved for future async summarization

	// Incremental rebuild (P4): only process Level-0 nodes that haven't been
	// grouped yet. Once a group reaches groupSize, it is merged into a Level-1
	// parent node; otherwise it stays as a Level-0 leaf.
	var ungrouped []*SummaryNode
	for _, r := range ms.roots {
		if r.Level == 0 {
			ungrouped = append(ungrouped, r)
		}
	}

	// Not enough ungrouped leaves to form a group.
	if len(ungrouped) < ms.groupSize {
		return
	}

	// Take the first groupSize ungrouped leaves and merge them.
	chunk := ungrouped[:ms.groupSize]

	parts := make([]string, len(chunk))
	for j, n := range chunk {
		if n.Summary != "" {
			parts[j] = n.Summary
		}
	}
	combined := strings.Join(parts, "; ")

	parent := &SummaryNode{
		Summary:    combined,
		Level:      1,
		Children:   chunk,
		StartIndex: chunk[0].StartIndex,
		EndIndex:   chunk[len(chunk)-1].EndIndex,
	}

	// Replace the grouped leaves with their parent in roots.
	// We find and remove the leaf nodes, then append the parent.
	groupedIDs := make(map[*SummaryNode]bool, len(chunk))
	for _, n := range chunk {
		groupedIDs[n] = true
	}
	var remaining []*SummaryNode
	for _, r := range ms.roots {
		if !groupedIDs[r] {
			remaining = append(remaining, r)
		}
	}
	remaining = append(remaining, parent)
	ms.roots = remaining
}

func (ms *MemoryStore) collectLeavesLocked() []*SummaryNode {
	var leaves []*SummaryNode
	for _, r := range ms.roots {
		leaves = append(leaves, collectLeavesRecursive(r)...)
	}
	return leaves
}

func (ms *MemoryStore) maxLevelInternal(nodes []*SummaryNode) int {
	max := 0
	for _, n := range nodes {
		l := nodeLevel(n)
		if l > max {
			max = l
		}
	}
	return max
}

func (ms *MemoryStore) writeNodes(sb *strings.Builder, nodes []*SummaryNode, maxLevel int, indent int) {
	prefix := strings.Repeat("  ", indent)
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if n.Level <= maxLevel || len(n.Children) == 0 {
			if n.Summary != "" {
				sb.WriteString(fmt.Sprintf("%s- %s\n", prefix, n.Summary))
			}
		} else {
			ms.writeNodes(sb, n.Children, maxLevel, indent+1)
		}
	}
}

func nodeLevel(n *SummaryNode) int {
	if n == nil {
		return 0
	}
	max := n.Level
	for _, c := range n.Children {
		if l := nodeLevel(c); l > max {
			max = l
		}
	}
	return max
}

func countLeaves(n *SummaryNode) int {
	if n == nil {
		return 0
	}
	if len(n.Children) == 0 {
		return 1
	}
	count := 0
	for _, c := range n.Children {
		count += countLeaves(c)
	}
	return count
}

func collectLeavesRecursive(n *SummaryNode) []*SummaryNode {
	if n == nil {
		return nil
	}
	if len(n.Children) == 0 {
		return []*SummaryNode{n}
	}
	var leaves []*SummaryNode
	for _, c := range n.Children {
		leaves = append(leaves, collectLeavesRecursive(c)...)
	}
	return leaves
}
