// Package skill provides the layered skill registry (DSH skill service):
// skill sources register as ranked roots across layers, same-named skills
// resolve by precedence (the most recent layer wins; within one layer the
// lower rank wins), catalogs expose candidates only, and definitions load
// lazily through the winning locator. The single-file parser and loader
// live in core/tool — this package owns layering and resolution.
//
// Package skill 提供分层技能注册表（DSH 技能服务）：技能源以分层分级根
// 注册，同名技能按优先级裁决（最近层胜出；单层内低 rank 胜出），目录只
// 暴露候选，定义经胜出定位符惰性加载。单文件解析与加载器在 core/tool——
// 本包只负责分层与裁决。
package skill

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/go-gocel/gocel/core/tool"
)

// ErrNotFound is returned when no root provides the named skill.
//
// ErrNotFound 在没有任何根提供该技能时返回。
var ErrNotFound = errors.New("skill: not found")

// Root is one registered skill source.
// Root 是一个已注册的技能源。
type Root struct {
	// Dir is the skills directory (root/<skill>/SKILL.md).
	Dir string
	// Rank orders roots within one layer: lower wins (DSH rank).
	Rank int
	// Layer orders roots across layers: higher wins (DSH: the most recent
	// registered layer overrides earlier layers).
	Layer int
}

// Candidate is a catalog entry: metadata plus a content digest for
// incremental catalog replacement. No body.
// Candidate 是目录条目：元数据 + 供增量目录替换使用的内容摘要，不含正文。
type Candidate struct {
	Name        string
	Description string
	Version     string
	Tags        []string
	Digest      string
	// ModelInvocable controls whether the model-facing skill tool may
	// invoke this skill (DSH modelInvocable). The zero value (false) keeps
	// the legacy single-channel behavior for callers that do not consult
	// the field.
	//
	// ModelInvocable 控制模型侧 skill 工具能否调用该技能。零值
	// （false）对不读取该字段的调用方保持旧版单通道行为。
	ModelInvocable bool
	// UserInvocable controls whether a human-facing surface (commands, UI)
	// may invoke this skill (DSH userInvocable). The zero value (false) is
	// the legacy default: skills were model-facing only.
	//
	// UserInvocable 控制人类侧表面（命令、UI）能否调用该技能。零值
	// （false）是旧版默认：技能仅面向模型。
	UserInvocable bool
}

// Registry resolves skills across layered roots.
// Registry 在分层根之间裁决技能解析。
type Registry struct {
	mu      sync.RWMutex
	roots   []Root
	entries []entry // winners, sorted by name
}

type entry struct {
	cand  Candidate
	dir   string
	rank  int
	layer int
}

// New creates an empty registry.
// New 创建空注册表。
func New() *Registry { return &Registry{} }

// AddRoot registers a skills directory and scans it immediately. A missing
// directory contributes no candidates (DSH discovery). The scan is
// fail-loud: a broken SKILL.md under an existing root aborts the add.
//
// AddRoot 注册技能目录并立即扫描。缺失目录不贡献候选（DSH 发现语义）。
// 扫描失败即报错：现有根下损坏的 SKILL.md 中止本次注册。
func (r *Registry) AddRoot(dir string, rank, layer int) error {
	cands, err := tool.ScanSkillDir(dir)
	if err != nil {
		return fmt.Errorf("skill: add root %q: %w", dir, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.roots = append(r.roots, Root{Dir: dir, Rank: rank, Layer: layer})
	for _, c := range cands {
		r.insert(Candidate{
			Name:           c.Name,
			Description:    c.Description,
			Version:        c.Version,
			Tags:           c.Tags,
			Digest:         c.Digest,
			ModelInvocable: c.ModelInvocable,
			UserInvocable:  c.UserInvocable,
		}, c.Dir, rank, layer)
	}
	return nil
}

// insert applies DSH precedence: (layer desc, rank asc, insertion order).
// insert 应用 DSH 优先级：(层降序，rank 升序，注册顺序)。
func (r *Registry) insert(c Candidate, dir string, rank, layer int) {
	for i := range r.entries {
		if r.entries[i].cand.Name != c.Name {
			continue
		}
		if layer > r.entries[i].layer || (layer == r.entries[i].layer && rank < r.entries[i].rank) {
			r.entries[i] = entry{cand: c, dir: dir, rank: rank, layer: layer}
		}
		return
	}
	r.entries = append(r.entries, entry{cand: c, dir: dir, rank: rank, layer: layer})
	sort.Slice(r.entries, func(i, j int) bool { return r.entries[i].cand.Name < r.entries[j].cand.Name })
}

// Candidates returns the resolved catalog (deduplicated winners, sorted by
// name). The returned slice is a copy.
//
// Candidates 返回裁决后的目录（去重胜出者，按名称排序）。返回副本。
func (r *Registry) Candidates() []Candidate {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Candidate, len(r.entries))
	for i, e := range r.entries {
		out[i] = e.cand
	}
	return out
}

// Load resolves the named skill to its winning root and loads the full
// definition (frontmatter body + references + assets). ErrNotFound when no
// root provides the name.
//
// Load 把指定技能裁决到胜出根并加载完整定义（前置正文 + references +
// assets）。无根提供该名称时返回 ErrNotFound。
func (r *Registry) Load(name string) (*tool.Skill, error) {
	r.mu.RLock()
	var dir string
	for _, e := range r.entries {
		if e.cand.Name == name {
			dir = e.dir
			break
		}
	}
	r.mu.RUnlock()
	if dir == "" {
		return nil, fmt.Errorf("skill %q: %w", name, ErrNotFound)
	}
	sk, err := tool.LoadSkillFromDir(dir)
	if err != nil {
		return nil, fmt.Errorf("skill: load %q: %w", name, err)
	}
	return sk, nil
}

// Refresh re-scans every registered root in registration order — the
// manual hook for DSH's watch-driven invalidation (fs watching stays out of
// the mechanism; a consumer may call Refresh on its own events). The
// rebuild is atomic: a scan failure leaves the previous catalog intact.
//
// Refresh 按注册顺序重扫所有根——DSH 观察驱动失效重扫的手动钩子（fs
// 观察不进入机制；消费方可按自身事件调用 Refresh）。重建是原子的：
// 扫描失败保留原目录。
func (r *Registry) Refresh() error {
	r.mu.RLock()
	roots := append([]Root(nil), r.roots...)
	r.mu.RUnlock()
	fresh := &Registry{}
	for _, root := range roots {
		if err := fresh.AddRoot(root.Dir, root.Rank, root.Layer); err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.entries = fresh.entries
	r.mu.Unlock()
	return nil
}
