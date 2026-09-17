package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill creates root/<dir>/SKILL.md with the given frontmatter and a
// distinctive body.
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

// TestLayeredPrecedence proves DSH layering: the most recent layer wins for
// a same-named skill, and Load resolves the winner's definition.
func TestLayeredPrecedence(t *testing.T) {
	bundled := t.TempDir()
	project := t.TempDir()
	writeSkill(t, bundled, "writer", "name: writer\ndescription: bundled writer", "BUNDLED BODY")
	writeSkill(t, project, "writer", "name: writer\ndescription: project writer", "PROJECT BODY")

	r := New()
	if err := r.AddRoot(bundled, 600, 0); err != nil {
		t.Fatalf("bundled root: %v", err)
	}
	if err := r.AddRoot(project, 100, 1); err != nil {
		t.Fatalf("project root: %v", err)
	}

	cands := r.Candidates()
	if len(cands) != 1 {
		t.Fatalf("candidates = %d, want 1 (deduped)", len(cands))
	}
	if cands[0].Description != "project writer" {
		t.Fatalf("winner description = %q, want the project layer", cands[0].Description)
	}
	sk, err := r.Load("writer")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(sk.Instructions, "PROJECT BODY") {
		t.Fatalf("Load must resolve the winning layer, got %q", sk.Instructions)
	}
}

// TestRankDecidesWithinLayer proves DSH rank: within one layer the lower
// rank wins.
func TestRankDecidesWithinLayer(t *testing.T) {
	low := t.TempDir()
	high := t.TempDir()
	writeSkill(t, low, "doc", "name: doc\ndescription: low rank", "LOW")
	writeSkill(t, high, "doc", "name: doc\ndescription: high rank", "HIGH")

	r := New()
	r.AddRoot(high, 900, 0)
	r.AddRoot(low, 200, 0)
	if got := r.Candidates()[0].Description; got != "low rank" {
		t.Fatalf("within one layer the lower rank must win, got %q", got)
	}
}

// TestLayerBeatsRank proves the precedence order: a higher layer always
// wins regardless of rank.
func TestLayerBeatsRank(t *testing.T) {
	oldLayer := t.TempDir()
	newLayer := t.TempDir()
	writeSkill(t, oldLayer, "doc", "name: doc\ndescription: old layer", "OLD")
	writeSkill(t, newLayer, "doc", "name: doc\ndescription: new layer", "NEW")

	r := New()
	r.AddRoot(oldLayer, 1, 0) // better rank, older layer
	r.AddRoot(newLayer, 999, 1)
	if got := r.Candidates()[0].Description; got != "new layer" {
		t.Fatalf("the most recent layer must win over rank, got %q", got)
	}
}

// TestMissingRootContributesNothing locks the discovery semantics through
// the registry.
func TestMissingRootContributesNothing(t *testing.T) {
	r := New()
	if err := r.AddRoot(filepath.Join(t.TempDir(), "absent"), 100, 1); err != nil {
		t.Fatalf("missing root must register without error: %v", err)
	}
	if len(r.Candidates()) != 0 {
		t.Fatal("missing root must contribute no candidates")
	}
}

// TestLoadUnknownIsErrNotFound proves the closed lookup vocabulary.
func TestLoadUnknownIsErrNotFound(t *testing.T) {
	r := New()
	if _, err := r.Load("ghost"); !strings.Contains(err.Error(), ErrNotFound.Error()) {
		t.Fatalf("unknown skill error = %v, want ErrNotFound", err)
	}
}

// TestCandidatesSortedDeterministically locks catalog ordering.
func TestCandidatesSortedDeterministically(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "zebra", "name: zebra", "z")
	writeSkill(t, root, "alpha", "name: alpha", "a")
	writeSkill(t, root, "mike", "name: mike", "m")
	r := New()
	r.AddRoot(root, 500, 0)
	names := []string{r.Candidates()[0].Name, r.Candidates()[1].Name, r.Candidates()[2].Name}
	want := []string{"alpha", "mike", "zebra"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", names, want)
		}
	}
}

// TestRefreshPicksUpChanges proves the invalidation hook: rescanning
// reflects added, removed, and edited skills, and a failed refresh keeps
// the old catalog.
func TestRefreshPicksUpChanges(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "doc", "name: doc", "v1")
	r := New()
	r.AddRoot(root, 500, 0)
	if len(r.Candidates()) != 1 {
		t.Fatal("seed scan failed")
	}

	writeSkill(t, root, "extra", "name: extra", "new")
	if err := r.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(r.Candidates()) != 2 {
		t.Fatalf("after adding a skill, candidates = %d, want 2", len(r.Candidates()))
	}

	// Break a skill, refresh must fail loudly AND keep the old catalog.
	dir := filepath.Join(root, "doc")
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: [broken\n"), 0o644); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if err := r.Refresh(); err == nil {
		t.Fatal("Refresh must fail on a broken skill")
	}
	if len(r.Candidates()) != 2 {
		t.Fatalf("a failed refresh must keep the previous catalog, got %d", len(r.Candidates()))
	}
}

// TestDigestInCatalog proves the catalog carries per-entry content digests
// for incremental replacement.
func TestDigestInCatalog(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "doc", "name: doc", "body")
	r := New()
	r.AddRoot(root, 500, 0)
	if len(r.Candidates()[0].Digest) != 64 {
		t.Fatal("catalog entries must carry a sha256 digest")
	}
}

// TestInvocationFlagsPropagate proves the dual-channel invocation fields
// survive the scan → registry → catalog path, and that a winning layer's
// flags override the shadowed layer's.
func TestInvocationFlagsPropagate(t *testing.T) {
	bundled := t.TempDir()
	project := t.TempDir()
	// Bundled: model-only. Project: user-invocable only (disable-model).
	writeSkill(t, bundled, "doc", "name: doc", "BUNDLED")
	writeSkill(t, project, "doc", "name: doc\ndisable-model-invocation: true\nuser-invocable: true", "PROJECT")

	r := New()
	r.AddRoot(bundled, 600, 0)
	r.AddRoot(project, 100, 1)

	cands := r.Candidates()
	if len(cands) != 1 {
		t.Fatalf("candidates = %d, want 1", len(cands))
	}
	c := cands[0]
	if c.ModelInvocable || !c.UserInvocable {
		t.Fatalf("winner flags = model:%v user:%v, want the project layer's (user-only)", c.ModelInvocable, c.UserInvocable)
	}
}

// TestInvocationFlagsDefaultModelOnly locks the backward-compatible zero
// value: a skill without invocation frontmatter is model-invocable and not
// user-invocable.
func TestInvocationFlagsDefaultModelOnly(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "doc", "name: doc", "body")
	r := New()
	r.AddRoot(root, 500, 0)
	c := r.Candidates()[0]
	if !c.ModelInvocable || c.UserInvocable {
		t.Fatalf("default flags = model:%v user:%v, want model-only", c.ModelInvocable, c.UserInvocable)
	}
}
