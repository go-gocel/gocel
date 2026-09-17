package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkillDir(t *testing.T, root, name, frontmatter string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "---\n" + frontmatter + "\n---\nBody of " + name + ".\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return dir
}

// TestScanSkillDir_CandidatesWithoutBodies proves the candidate/definition
// separation: scanning returns metadata and a stable content digest without
// loading instruction bodies.
func TestScanSkillDir_CandidatesWithoutBodies(t *testing.T) {
	root := t.TempDir()
	writeSkillDir(t, root, "writer", "name: writer\nversion: \"1.2.0\"\ntags: [write, docs]\n")
	writeSkillDir(t, root, "reader", "name: reader\ndescription: reads things\n")
	os.MkdirAll(filepath.Join(root, "not-a-skill"), 0o755) // no SKILL.md → skipped

	cands, err := ScanSkillDir(root)
	if err != nil {
		t.Fatalf("ScanSkillDir: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want 2", len(cands))
	}
	// Deterministic order by name.
	if cands[0].Name != "reader" || cands[1].Name != "writer" {
		t.Fatalf("candidates not sorted by name: %v %v", cands[0].Name, cands[1].Name)
	}
	w := cands[1]
	if w.Version != "1.2.0" || len(w.Tags) != 2 || w.Digest == "" {
		t.Fatalf("writer candidate metadata = %+v", w)
	}
	if len(w.Digest) != 64 {
		t.Fatalf("digest %q is not a sha256 hex", w.Digest)
	}
	// The locator resolves to the full skill; the body loads on demand.
	sk, err := LoadSkillFromDir(w.Dir)
	if err != nil {
		t.Fatalf("LoadSkillFromDir: %v", err)
	}
	if !strings.Contains(sk.Instructions, "Body of writer") {
		t.Fatal("loaded skill must carry the instruction body")
	}
}

// TestScanSkillDir_MissingRootIsEmptySource locks the DSH discovery
// semantics: an absent root yields zero candidates, not an error.
func TestScanSkillDir_MissingRootIsEmptySource(t *testing.T) {
	cands, err := ScanSkillDir(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("ScanSkillDir: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("missing root must yield zero candidates, got %d", len(cands))
	}
}

// TestScanSkillDir_DigestTracksContent proves the digest changes exactly
// when the skill content changes — the basis of incremental catalog
// replacement.
func TestScanSkillDir_DigestTracksContent(t *testing.T) {
	root := t.TempDir()
	dir := writeSkillDir(t, root, "doc", "name: doc\n")
	before, err := ScanSkillDir(root)
	if err != nil {
		t.Fatalf("ScanSkillDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: doc\n---\nNew body.\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	after, err := ScanSkillDir(root)
	if err != nil {
		t.Fatalf("ScanSkillDir: %v", err)
	}
	if before[0].Digest == after[0].Digest {
		t.Fatal("digest must change when the content changes")
	}
}

// TestScanSkillDir_MalformedFrontmatterFails loudly: a broken skill is an
// error at the source, not a silent skip.
func TestScanSkillDir_MalformedFrontmatterFails(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "broken")
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: [unclosed\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ScanSkillDir(root); err == nil {
		t.Fatal("malformed frontmatter must fail the scan")
	}
}

// TestScanSkillDir_InvocationFlags: the dual-channel invocation fields are
// parsed from the frontmatter with the DSH vocabulary (disable-model-
// invocation / user-invocable), defaulting to model-only.
func TestScanSkillDir_InvocationFlags(t *testing.T) {
	root := t.TempDir()
	writeSkillDir(t, root, "model_only", "name: model_only\n")
	writeSkillDir(t, root, "both", "name: both\nuser-invocable: true\n")
	writeSkillDir(t, root, "user_only", "name: user_only\ndisable-model-invocation: true\nuser-invocable: true\n")

	cands, err := ScanSkillDir(root)
	if err != nil {
		t.Fatalf("ScanSkillDir: %v", err)
	}
	byName := map[string]SkillCandidate{}
	for _, c := range cands {
		byName[c.Name] = c
	}
	if c := byName["model_only"]; !c.ModelInvocable || c.UserInvocable {
		t.Fatalf("model_only = model:%v user:%v, want model-only default", c.ModelInvocable, c.UserInvocable)
	}
	if c := byName["both"]; !c.ModelInvocable || !c.UserInvocable {
		t.Fatalf("both = model:%v user:%v, want both channels", c.ModelInvocable, c.UserInvocable)
	}
	if c := byName["user_only"]; c.ModelInvocable || !c.UserInvocable {
		t.Fatalf("user_only = model:%v user:%v, want user-only", c.ModelInvocable, c.UserInvocable)
	}
}
