package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/go-gocel/gocel/core/kernel"

	"gopkg.in/yaml.v3"
)

// SkillFrontmatter holds the metadata parsed from a SKILL.md YAML frontmatter.
//
// SkillFrontmatter 保存从 SKILL.md YAML 前置元数据解析出的元信息。
type SkillFrontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Version     string   `yaml:"version"`
	Author      string   `yaml:"author"`
	License     string   `yaml:"license"`
	Tags        []string `yaml:"tags"`
	// DisableModelInvocation excludes the skill from the model-facing
	// catalog (DSH disable-model-invocation). False is the default: skills
	// are model-invocable unless explicitly disabled.
	//
	// DisableModelInvocation 把技能排除出模型侧目录（DSH
	// disable-model-invocation）。默认 false：技能面向模型，除非显式禁用。
	DisableModelInvocation bool `yaml:"disable-model-invocation"`
	// UserInvocable admits the skill to human-facing surfaces (commands,
	// UI) (DSH user-invocable). False is the legacy default: model-only.
	//
	// UserInvocable 允许技能出现在人类侧表面（命令、UI）（DSH
	// user-invocable）。默认 false 是旧版行为：仅模型。
	UserInvocable bool `yaml:"user-invocable"`
}

// Skill is a bundle of tools, instructions, references, and assets.
// It implements kernel.ToolSource and can be registered with a ToolRegistry.
//
// Skill 是工具、指令、引用和资源的集合包。实现 kernel.ToolSource 接口。
type Skill struct {
	mu   sync.RWMutex
	name string

	Description string
	Version     string
	Author      string
	License     string
	Tags        []string

	// Instructions describes how to use the skill's tools.
	// It is NOT automatically injected into any prompt; callers read it as needed.
	//
	// Instructions 描述如何使用技能的各个工具，不会被自动注入提示词。
	Instructions string

	// References holds named text data (e.g. config files, prompt templates).
	// References 保存命名文本数据（如配置文件、提示模板）。
	References map[string]string

	// Assets holds named binary data (e.g. images, audio).
	// Assets 保存命名二进制数据（如图片、音频）。
	Assets map[string][]byte

	// Tools is the set of tools this skill provides.
	// Tools 是此技能提供的工具集合。
	Tools []kernel.Tool
}

// NewSkill creates a Skill with the given name and description.
// NewSkill 根据名称和描述创建技能。
func NewSkill(name, description string) *Skill {
	return &Skill{
		name:        name,
		Description: description,
		References:  make(map[string]string),
		Assets:      make(map[string][]byte),
	}
}

// SkillName returns the skill's short name (without "skill:" prefix).
// SkillName 返回技能短名称（不含 "skill:" 前缀）。
func (s *Skill) SkillName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.name
}

// WithInstructions sets the usage instructions.
// WithInstructions 设置使用说明。
func (s *Skill) WithInstructions(instructions string) *Skill {
	s.mu.Lock()
	s.Instructions = instructions
	s.mu.Unlock()
	return s
}

// AddTools appends tools to the skill's tool set.
// AddTools 向技能的工具集合追加工具。
func (s *Skill) AddTools(tools ...kernel.Tool) *Skill {
	s.mu.Lock()
	s.Tools = append(s.Tools, tools...)
	s.mu.Unlock()
	return s
}

// AddReference stores a named text reference.
// AddReference 保存一条命名的文本引用。
func (s *Skill) AddReference(name, content string) *Skill {
	s.mu.Lock()
	s.References[name] = content
	s.mu.Unlock()
	return s
}

// AddAsset stores a named binary asset.
// AddAsset 保存一份命名的二进制资源。
func (s *Skill) AddAsset(name string, data []byte) *Skill {
	s.mu.Lock()
	s.Assets[name] = data
	s.mu.Unlock()
	return s
}

// WithVersion sets the version string.
// WithVersion 设置版本字符串。
func (s *Skill) WithVersion(version string) *Skill {
	s.mu.Lock()
	s.Version = version
	s.mu.Unlock()
	return s
}

// WithAuthor sets the author string.
// WithAuthor 设置作者字符串。
func (s *Skill) WithAuthor(author string) *Skill {
	s.mu.Lock()
	s.Author = author
	s.mu.Unlock()
	return s
}

// WithTags sets the tag list.
// WithTags 设置标签列表。
func (s *Skill) WithTags(tags ...string) *Skill {
	s.mu.Lock()
	s.Tags = tags
	s.mu.Unlock()
	return s
}

// Name returns the tool-source name prefixed with "skill:".
// Name 返回带 "skill:" 前缀的工具源名称。
func (s *Skill) Name() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return "skill:" + s.name
}

// ListTools returns the skill's tools. It implements kernel.ToolSource.
// ListTools 返回技能的工具集合。实现 kernel.ToolSource 接口。
func (s *Skill) ListTools(_ context.Context) ([]kernel.Tool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Tools, nil
}

// GetReference retrieves a named text reference.
// GetReference 获取命名的文本引用。
func (s *Skill) GetReference(name string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.References[name]
}

// GetAsset retrieves a named binary asset.
// GetAsset 获取命名的二进制资源。
func (s *Skill) GetAsset(name string) []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Assets[name]
}

// ListReferences returns the names of all stored references.
// ListReferences 返回所有已保存引用的名称。
func (s *Skill) ListReferences() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.References))
	for n := range s.References {
		names = append(names, n)
	}
	return names
}

// ListAssets returns the names of all stored assets.
// ListAssets 返回所有已保存资源的名称。
func (s *Skill) ListAssets() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.Assets))
	for n := range s.Assets {
		names = append(names, n)
	}
	return names
}

// SkillCandidate summarizes one on-disk skill for catalogs: metadata only,
// a content digest for incremental catalog replacement (DSH: entries carry
// a sha256 so consumers replace changed entries instead of re-sending the
// whole catalog), and a locator for lazy loading. Bodies are NOT loaded here
// — candidates are cheap, definitions load on demand.
//
// SkillCandidate 汇总一个磁盘技能的目录信息：仅元数据 + 用于增量目录替换
// 的内容摘要（DSH：条目携带 sha256，消费方只替换变化条目而无需全量重发）
// + 惰性加载定位符。此处不加载正文——候选廉价，定义按需加载。
type SkillCandidate struct {
	Name        string
	Description string
	Version     string
	// Tags is the frontmatter tag list (empty when absent).
	Tags []string
	// Digest is the lowercase hex sha256 of the SKILL.md content.
	Digest string
	// ModelInvocable mirrors DisableModelInvocation (inverted): the
	// model-facing catalog includes the skill unless explicitly disabled.
	ModelInvocable bool
	// UserInvocable mirrors the frontmatter user-invocable flag.
	UserInvocable bool
	// Dir is the locator: LoadSkillFromDir(Dir) loads the full skill.
	Dir string
}

// ScanSkillDir enumerates the single-level skill directories under root
// (root/<skill>/SKILL.md) and returns their candidates without loading
// bodies. A missing root yields zero candidates (DSH discovery: an absent
// source root is an empty source, not an error).
//
// ScanSkillDir 枚举 root 下单层技能目录（root/<skill>/SKILL.md），返回候选
// 而不加载正文。缺失的 root 产生零候选（DSH 发现语义：不存在的源根是空
// 源，不是错误）。
func ScanSkillDir(root string) ([]SkillCandidate, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan skill root %q: %w", root, err)
	}
	var out []SkillCandidate
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		content, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			if os.IsNotExist(err) {
				continue // not a skill directory
			}
			return nil, fmt.Errorf("skill %q: %w", e.Name(), err)
		}
		fm, _, err := parseSkillMD(string(content))
		if err != nil {
			return nil, fmt.Errorf("skill %q: %w", e.Name(), err)
		}
		name := fm.Name
		if name == "" {
			name = e.Name()
		}
		sum := sha256.Sum256(content)
		out = append(out, SkillCandidate{
			Name:           name,
			Description:    fm.Description,
			Version:        fm.Version,
			Tags:           fm.Tags,
			Digest:         hex.EncodeToString(sum[:]),
			ModelInvocable: !fm.DisableModelInvocation,
			UserInvocable:  fm.UserInvocable,
			Dir:            dir,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// LoadSkillFromDir loads a Skill from a directory with the following structure.
//
//	skill-name/
//	  SKILL.md          — YAML frontmatter (name, description, version, author, license, tags) + body as Instructions
//	  references/       — text files read into References map
//	  assets/           — binary files read into Assets map
//
// LoadSkillFromDir 从具有上述目录结构的目录加载技能：SKILL.md 的 YAML
// 前置元数据 + 正文（正文作为 Instructions），references/ 中的文本文件
// 读入 References，assets/ 中的二进制文件读入 Assets。
func LoadSkillFromDir(dir string) (*Skill, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("skill dir %q: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skill dir %q: not a directory", dir)
	}

	skillName := filepath.Base(dir)

	skillMDPath := filepath.Join(dir, "SKILL.md")
	content, err := os.ReadFile(skillMDPath)
	if err != nil {
		return nil, fmt.Errorf("skill %q: %w", skillName, err)
	}

	frontmatter, body, err := parseSkillMD(string(content))
	if err != nil {
		return nil, fmt.Errorf("skill %q: %w", skillName, err)
	}

	name := frontmatter.Name
	if name == "" {
		name = skillName
	}

	skill := NewSkill(name, frontmatter.Description).
		WithInstructions(body).
		WithVersion(frontmatter.Version).
		WithAuthor(frontmatter.Author).
		WithTags(frontmatter.Tags...)
	skill.License = frontmatter.License

	refDir := filepath.Join(dir, "references")
	if entries, err := os.ReadDir(refDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(refDir, e.Name()))
			if err != nil {
				continue
			}
			skill.References[e.Name()] = string(data)
		}
	}

	assetDir := filepath.Join(dir, "assets")
	if entries, err := os.ReadDir(assetDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(assetDir, e.Name()))
			if err != nil {
				continue
			}
			skill.Assets[e.Name()] = data
		}
	}

	return skill, nil
}

// parseSkillMD parses YAML frontmatter (delimited by ---) from a SKILL.md file.
// Returns the parsed frontmatter and the body text after the frontmatter.
// If no frontmatter is found, returns an empty frontmatter and the full content as body.
func parseSkillMD(content string) (*SkillFrontmatter, string, error) {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		return &SkillFrontmatter{}, content, nil
	}

	parts := strings.SplitN(content[3:], "---", 2)
	if len(parts) < 2 {
		return nil, "", fmt.Errorf("frontmatter not properly closed with ---")
	}

	frontmatterStr := strings.TrimSpace(parts[0])
	body := strings.TrimSpace(parts[1])

	fm := &SkillFrontmatter{}
	if frontmatterStr != "" {
		if err := yaml.Unmarshal([]byte(frontmatterStr), fm); err != nil {
			return nil, "", fmt.Errorf("parse frontmatter YAML: %w", err)
		}
	}

	return fm, body, nil
}
