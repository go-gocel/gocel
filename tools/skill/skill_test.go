package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	skillcore "github.com/go-gocel/gocel/core/skill"
)

func writeSkill(t *testing.T, root, dir, frontmatter, body string) {
	t.Helper()
	full := filepath.Join(root, dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "---\n" + frontmatter + "\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(full, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func newRegistry(t *testing.T) *skillcore.Registry {
	t.Helper()
	root := t.TempDir()
	writeSkill(t, root, "doc", "name: doc\ndescription: writes docs\nversion: \"2.0.0\"", "Document like this.")
	writeSkill(t, root, "refed", "name: refed\ndescription: has references", "Follow the refs.")
	refDir := filepath.Join(root, "refed", "references")
	os.MkdirAll(refDir, 0o755)
	os.WriteFile(filepath.Join(refDir, "guide.txt"), []byte("guide content"), 0o644)

	r := skillcore.New()
	if err := r.AddRoot(root, 500, 0); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}
	return r
}

func findTool(t *testing.T, tools []kernel.Tool, name string) kernel.Tool {
	t.Helper()
	for _, tl := range tools {
		if tl.Name() == name {
			return tl
		}
	}
	t.Fatalf("tool %q not built", name)
	return nil
}

// TestList_CatalogCarriesDigests proves the catalog output is the
// digest-tagged, sorted JSON the incremental-replacement design needs.
func TestList_CatalogCarriesDigests(t *testing.T) {
	tools := MustTools(Config{Registry: newRegistry(t)})
	out, err := findTool(t, tools, "skill_list").Run(context.Background(), "{}")
	if err != nil {
		t.Fatalf("skill_list: %v", err)
	}
	var entries []catalogEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("catalog is not a JSON array: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[0].Name != "doc" || entries[1].Name != "refed" {
		t.Fatalf("catalog must be sorted by name, got %v %v", entries[0].Name, entries[1].Name)
	}
	if len(entries[0].Digest) != 64 {
		t.Fatalf("entry must carry a sha256 digest, got %q", entries[0].Digest)
	}
	if entries[0].Description == "" || entries[0].Version != "2.0.0" {
		t.Fatalf("entry metadata incomplete: %+v", entries[0])
	}
}

// TestLoad_ReturnsInstructions proves on-demand loading: the body comes
// back, plus the sorted reference names when present.
func TestLoad_ReturnsInstructions(t *testing.T) {
	tools := MustTools(Config{Registry: newRegistry(t)})
	load := findTool(t, tools, "skill_load")

	out, err := load.Run(context.Background(), `{"name":"doc"}`)
	if err != nil {
		t.Fatalf("skill_load doc: %v", err)
	}
	if !strings.Contains(out, "Document like this") {
		t.Fatalf("load must return the instructions, got %q", out)
	}

	out, err = load.Run(context.Background(), `{"name":"refed"}`)
	if err != nil {
		t.Fatalf("skill_load refed: %v", err)
	}
	if !strings.Contains(out, "References") || !strings.Contains(out, "guide.txt") {
		t.Fatalf("load must list reference names, got %q", out)
	}
}

// TestLoad_UnknownIsErrNotFound proves the closed vocabulary reaches the
// model honestly.
func TestLoad_UnknownIsErrNotFound(t *testing.T) {
	tools := MustTools(Config{Registry: newRegistry(t)})
	_, err := findTool(t, tools, "skill_load").Run(context.Background(), `{"name":"ghost"}`)
	if err == nil || !strings.Contains(err.Error(), skillcore.ErrNotFound.Error()) {
		t.Fatalf("unknown skill must surface ErrNotFound, got %v", err)
	}
}

// TestLoad_ChannelGate: the invocation channel rejects skills not admitted
// to the requested surface (fail-closed), and accepts the model channel by
// default.
func TestLoad_ChannelGate(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "plain", "name: plain", "plain body")
	writeSkill(t, root, "user_only", "name: user_only\ndisable-model-invocation: true\nuser-invocable: true", "user body")
	r := skillcore.New()
	if err := r.AddRoot(root, 500, 0); err != nil {
		t.Fatal(err)
	}
	tools := MustTools(Config{Registry: r})
	load := findTool(t, tools, "skill_load")

	// Default (model) channel: plain is fine, user_only is refused.
	if _, err := load.Run(context.Background(), `{"name":"plain"}`); err != nil {
		t.Fatalf("model load of plain = %v, want nil", err)
	}
	if _, err := load.Run(context.Background(), `{"name":"user_only"}`); err == nil {
		t.Fatal("model load of user_only = nil, want channel refusal")
	}
	// Explicit model channel behaves the same.
	if _, err := load.Run(context.Background(), `{"name":"plain","channel":"model"}`); err != nil {
		t.Fatalf("model load with channel=model = %v, want nil", err)
	}
	// User channel: user_only loads; plain (model-only) is refused.
	if _, err := load.Run(context.Background(), `{"name":"user_only","channel":"user"}`); err != nil {
		t.Fatalf("user load of user_only = %v, want nil", err)
	}
	if _, err := load.Run(context.Background(), `{"name":"plain","channel":"user"}`); err == nil {
		t.Fatal("user load of model-only skill = nil, want channel refusal")
	}
	// Unknown channel is rejected.
	if _, err := load.Run(context.Background(), `{"name":"plain","channel":"bogus"}`); err == nil {
		t.Fatal("unknown channel = nil, want error")
	}
}

// TestLoad_MissingNameRejected proves required-argument enforcement.
func TestLoad_MissingNameRejected(t *testing.T) {
	tools := MustTools(Config{Registry: newRegistry(t)})
	if _, err := findTool(t, tools, "skill_load").Run(context.Background(), `{}`); err == nil {
		t.Fatal("missing required name must be rejected")
	}
}

// TestTools_ReadOnlyEffects proves both tools declare no mutating effects —
// they stay available in plan mode and read-only tiers.
func TestTools_ReadOnlyEffects(t *testing.T) {
	tools := MustTools(Config{Registry: newRegistry(t)})
	for _, tl := range tools {
		for _, e := range kernel.EffectiveEffects(tl) {
			if e == kernel.EffectWrite || e == kernel.EffectExec || e == kernel.EffectUserData {
				t.Fatalf("%s must not declare mutating effects, got %v", tl.Name(), kernel.EffectiveEffects(tl))
			}
		}
	}
}

// TestTools_NilRegistryRejected proves config validation at the entry.
func TestTools_NilRegistryRejected(t *testing.T) {
	if _, err := Tools(Config{}); err == nil {
		t.Fatal("nil registry must be rejected")
	}
}
