// Package skill exposes the layered skill registry to the model: skill_list
// returns the digest-tagged catalog (the model replaces changed entries
// instead of re-reading everything — DSH incremental catalog replacement),
// and skill_load fetches one skill's full instructions on demand.
//
// Package skill 把分层技能注册表暴露给模型：skill_list 返回带摘要的目录
// （模型只替换变化条目而非全部重读——DSH 目录增量替换），skill_load 按需
// 取回单个技能的完整说明。
package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/go-gocel/gocel/core/kernel"
	skillcore "github.com/go-gocel/gocel/core/skill"
	"github.com/go-gocel/gocel/core/tool"
)

// Config wires the tool set to a registry.
// Config 将工具集连接到技能注册表。
type Config struct {
	// Registry is the layered skill registry the tools read.
	Registry *skillcore.Registry
}

// Tools builds skill_list / skill_load.
// Tools 构建 skill_list / skill_load 两个工具。
func Tools(cfg Config) ([]kernel.Tool, error) {
	if cfg.Registry == nil {
		return nil, fmt.Errorf("skill tools: nil registry")
	}
	c := &client{cfg: cfg}

	list, err := tool.ToolFromFunc(
		c.list,
		tool.WithToolName("skill_list"),
		tool.WithToolDescription("List the available skills: name, description, version, tags, model_invocable/user_invocable channels, and a content digest. Compare digests against a previous list to spot changed skills without re-reading everything."),
		tool.WithToolEffects(kernel.EffectRead),
	)
	if err != nil {
		return nil, err
	}
	load, err := tool.ToolFromFunc(
		c.load,
		tool.WithToolName("skill_load"),
		tool.WithToolDescription("Load one skill's full instructions by name. channel selects the invocation surface: \"model\" (default) requires the skill to be model-invocable, \"user\" requires it to be user-invocable; a mismatch is rejected."),
		tool.WithToolEffects(kernel.EffectRead),
	)
	if err != nil {
		return nil, err
	}
	return []kernel.Tool{list, load}, nil
}

// MustTools builds the tool set, panicking on configuration errors.
// MustTools 构建工具集，配置出错时 panic。
func MustTools(cfg Config) []kernel.Tool {
	ts, err := Tools(cfg)
	if err != nil {
		panic(err)
	}
	return ts
}

// catalogEntry is the JSON shape of one catalog entry.
type catalogEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Digest      string   `json:"digest"`
	// ModelInvocable / UserInvocable expose the dual-channel invocation
	// policy (DSH skill invocation): one discovery result serves the
	// model-facing tool and human-facing surfaces (commands, UI) without
	// conflating their catalogs.
	ModelInvocable bool `json:"model_invocable"`
	UserInvocable  bool `json:"user_invocable"`
}

type client struct {
	cfg Config
}

func (c *client) list(_ context.Context) (string, error) {
	cands := c.cfg.Registry.Candidates()
	entries := make([]catalogEntry, 0, len(cands))
	for _, cd := range cands {
		tags := cd.Tags
		if tags == nil {
			tags = []string{}
		}
		entries = append(entries, catalogEntry{
			Name:           cd.Name,
			Description:    cd.Description,
			Version:        cd.Version,
			Tags:           tags,
			Digest:         cd.Digest,
			ModelInvocable: cd.ModelInvocable,
			UserInvocable:  cd.UserInvocable,
		})
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("skill: marshal catalog: %w", err)
	}
	return string(b), nil
}

// loadArgs: name required; channel "model" (default) or "user".
type loadArgs struct {
	Name    string `json:"name" description:"The skill name from skill_list"`
	Channel string `json:"channel,omitempty" description:"Invocation surface: \"model\" (default) or \"user\""`
}

func (c *client) load(_ context.Context, args loadArgs) (string, error) {
	if args.Name == "" {
		return "", fmt.Errorf("skill: name is required")
	}
	cands := c.cfg.Registry.Candidates()
	var cand *skillcore.Candidate
	for i := range cands {
		if cands[i].Name == args.Name {
			cand = &cands[i]
			break
		}
	}
	// Channel gate (DSH invocation policy): fail closed — a skill not
	// admitted to the requested surface is a refusal, never a silent load.
	switch args.Channel {
	case "", "model":
		if cand != nil && !cand.ModelInvocable {
			return "", fmt.Errorf("skill: %q is not model-invocable", args.Name)
		}
	case "user":
		if cand != nil && !cand.UserInvocable {
			return "", fmt.Errorf("skill: %q is not user-invocable", args.Name)
		}
	default:
		return "", fmt.Errorf("skill: unknown channel %q (want \"model\" or \"user\")", args.Channel)
	}

	sk, err := c.cfg.Registry.Load(args.Name)
	if err != nil {
		return "", err
	}
	refs := sk.ListReferences()
	sort.Strings(refs)
	if len(refs) > 0 {
		return sk.Instructions + "\n\n## References\n" + joinNames(refs), nil
	}
	return sk.Instructions, nil
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
