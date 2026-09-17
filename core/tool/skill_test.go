package tool_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/tool"
)

// ── parseSkillMD tests (via exported types) ────────────────────────────────

func TestNewSkill(t *testing.T) {
	s := tool.NewSkill("test", "a test skill")
	if s == nil {
		t.Fatal("NewSkill returned nil")
	}
	if s.SkillName() != "test" {
		t.Errorf("SkillName = %q, want %q", s.SkillName(), "test")
	}
	if s.Name() != "skill:test" {
		t.Errorf("Name() = %q, want %q", s.Name(), "skill:test")
	}
}

func TestSkill_WithInstructions(t *testing.T) {
	s := tool.NewSkill("test", "").
		WithInstructions("use this tool by doing X")
	if s.Instructions != "use this tool by doing X" {
		t.Errorf("Instructions = %q", s.Instructions)
	}
}

func TestSkill_AddTools(t *testing.T) {
	fn := func(ctx context.Context, s string) (string, error) { return s, nil }
	t1, err := tool.ToolFromFunc(fn, tool.WithToolName("tool1"))
	if err != nil {
		t.Fatalf("ToolFromFunc: %v", err)
	}
	t2, err := tool.ToolFromFunc(fn, tool.WithToolName("tool2"))
	if err != nil {
		t.Fatalf("ToolFromFunc: %v", err)
	}

	s := tool.NewSkill("test", "").AddTools(t1, t2)
	tools, err := s.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}
	if tools[0].Name() != "tool1" || tools[1].Name() != "tool2" {
		t.Errorf("tools = %q, %q", tools[0].Name(), tools[1].Name())
	}
}

func TestSkill_AddReference(t *testing.T) {
	s := tool.NewSkill("test", "").
		AddReference("config.json", `{"key": "value"}`).
		AddReference("prompt.txt", "be helpful")

	if s.GetReference("config.json") != `{"key": "value"}` {
		t.Error("GetReference config.json failed")
	}
	if s.GetReference("prompt.txt") != "be helpful" {
		t.Error("GetReference prompt.txt failed")
	}
	if s.GetReference("nonexistent") != "" {
		t.Error("GetReference nonexistent should be empty")
	}

	refs := s.ListReferences()
	if len(refs) != 2 {
		t.Fatalf("ListReferences len = %d, want 2", len(refs))
	}
}

func TestSkill_AddAsset(t *testing.T) {
	s := tool.NewSkill("test", "").
		AddAsset("img.png", []byte{0x89, 0x50, 0x4e}).
		AddAsset("data.bin", []byte{0x00, 0x01, 0x02})

	if len(s.GetAsset("img.png")) != 3 {
		t.Error("GetAsset img.png failed")
	}
	if s.GetAsset("nonexistent") != nil {
		t.Error("GetAsset nonexistent should be nil")
	}

	assets := s.ListAssets()
	if len(assets) != 2 {
		t.Fatalf("ListAssets len = %d, want 2", len(assets))
	}
}

func TestSkill_Metadata(t *testing.T) {
	s := tool.NewSkill("test", "desc").
		WithVersion("1.0.0").
		WithAuthor("me").
		WithTags("util", "text")

	if s.Version != "1.0.0" {
		t.Errorf("Version = %q", s.Version)
	}
	if s.Author != "me" {
		t.Errorf("Author = %q", s.Author)
	}
	if len(s.Tags) != 2 || s.Tags[0] != "util" {
		t.Errorf("Tags = %v", s.Tags)
	}
}

func TestSkill_LoadFromDir(t *testing.T) {
	dir := t.TempDir()

	skillDir := filepath.Join(dir, "my_skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	skilMD := `---
name: MySkill
description: A test skill
version: 2.0.0
author: tester
license: MIT
tags: [test, demo]
---
Use this skill by calling the available tools.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skilMD), 0644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	refDir := filepath.Join(skillDir, "references")
	if err := os.MkdirAll(refDir, 0755); err != nil {
		t.Fatalf("mkdir references: %v", err)
	}
	if err := os.WriteFile(filepath.Join(refDir, "prompt.txt"), []byte("be helpful"), 0644); err != nil {
		t.Fatalf("write reference: %v", err)
	}

	assetDir := filepath.Join(skillDir, "assets")
	if err := os.MkdirAll(assetDir, 0755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assetDir, "icon.png"), []byte{0x89, 0x50, 0x4e}, 0644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	skill, err := tool.LoadSkillFromDir(skillDir)
	if err != nil {
		t.Fatalf("LoadSkillFromDir: %v", err)
	}

	if skill.SkillName() != "MySkill" {
		t.Errorf("SkillName = %q, want %q", skill.SkillName(), "MySkill")
	}
	if skill.Description != "A test skill" {
		t.Errorf("Description = %q", skill.Description)
	}
	if skill.Version != "2.0.0" {
		t.Errorf("Version = %q", skill.Version)
	}
	if skill.Author != "tester" {
		t.Errorf("Author = %q", skill.Author)
	}
	if skill.License != "MIT" {
		t.Errorf("License = %q", skill.License)
	}
	if len(skill.Tags) != 2 || skill.Tags[0] != "test" {
		t.Errorf("Tags = %v", skill.Tags)
	}
	if skill.Instructions != "Use this skill by calling the available tools." {
		t.Errorf("Instructions = %q", skill.Instructions)
	}

	if ref := skill.GetReference("prompt.txt"); ref != "be helpful" {
		t.Errorf("reference = %q", ref)
	}
	if asset := skill.GetAsset("icon.png"); len(asset) != 3 {
		t.Errorf("asset len = %d", len(asset))
	}
}

func TestSkill_LoadFromDir_NoFrontmatter(t *testing.T) {
	dir := t.TempDir()

	skillDir := filepath.Join(dir, "plain_skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("Just plain text instructions."), 0644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	skill, err := tool.LoadSkillFromDir(skillDir)
	if err != nil {
		t.Fatalf("LoadSkillFromDir: %v", err)
	}

	if skill.SkillName() != "plain_skill" {
		t.Errorf("SkillName = %q, want dir name", skill.SkillName())
	}
	if skill.Instructions != "Just plain text instructions." {
		t.Errorf("Instructions = %q", skill.Instructions)
	}
}

func TestSkill_LoadFromDir_InvalidDir(t *testing.T) {
	_, err := tool.LoadSkillFromDir("nonexistent_skill_dir_xyz")
	if err == nil {
		t.Fatal("expected error for nonexistent dir")
	}
}

func TestSkill_LoadFromDir_NotADir(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(f, []byte("content"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_, err := tool.LoadSkillFromDir(f)
	if err == nil {
		t.Fatal("expected error for file path")
	}
}

func TestSkill_LoadFromDir_MissingSKILLMD(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "empty_skill")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := tool.LoadSkillFromDir(dir)
	if err == nil {
		t.Fatal("expected error for missing SKILL.md")
	}
}

func TestSkill_ToolSourceInterface(t *testing.T) {
	s := tool.NewSkill("test", "desc")
	var _ kernel.ToolSource = s
}

func TestSkill_ListToolsEmpty(t *testing.T) {
	s := tool.NewSkill("empty", "")
	tools, err := s.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestSkill_Concurrency(t *testing.T) {
	s := tool.NewSkill("concurrent", "test")

	fn := func(ctx context.Context, s string) (string, error) { return s, nil }
	t1, _ := tool.ToolFromFunc(fn, tool.WithToolName("t1"))
	t2, _ := tool.ToolFromFunc(fn, tool.WithToolName("t2"))
	s.AddTools(t1, t2)
	s.AddReference("r1", "v1")
	s.AddAsset("a1", []byte{0x01})

	done := make(chan struct{}, 20)
	for i := 0; i < 20; i++ {
		go func() {
			s.ListTools(context.Background())
			s.SkillName()
			s.GetReference("r1")
			s.GetAsset("a1")
			s.ListReferences()
			s.ListAssets()
			done <- struct{}{}
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
}

func TestSkill_LoadFromDir_FrontmatterTags(t *testing.T) {
	dir := t.TempDir()

	skillDir := filepath.Join(dir, "tagged_skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	skilMD := `---
name: TaggedSkill
tags:
  - util
  - text
  - demo
---
body
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skilMD), 0644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	skill, err := tool.LoadSkillFromDir(skillDir)
	if err != nil {
		t.Fatalf("LoadSkillFromDir: %v", err)
	}

	if len(skill.Tags) != 3 || skill.Tags[0] != "util" || skill.Tags[1] != "text" {
		t.Errorf("Tags = %v", skill.Tags)
	}
}
