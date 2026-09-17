package trash

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Regression: the trash name is basename + second-resolution timestamp, so
// two same-basename files trashed within one second collide — on Unix the
// second rename silently overwrites the first victim (data loss, C11).
func TestTrash_SameSecondCollisionNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := os.MkdirAll("b", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("a.txt", []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("b", "a.txt"), []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}

	tt := &trashTool{}
	if _, err := tt.Run(context.Background(), `{"path":"a.txt"}`); err != nil {
		t.Fatalf("first trash: %v", err)
	}
	if _, err := tt.Run(context.Background(), `{"path":"b/a.txt"}`); err != nil {
		t.Fatalf("second trash: %v", err)
	}

	trashDir := filepath.Join(".gocode", ".trash")
	entries, err := os.ReadDir(trashDir)
	if err != nil {
		t.Fatalf("read trash dir: %v", err)
	}

	// Skip hidden bookkeeping (the manifest) — only victims count.
	var victims []os.DirEntry
	for _, e := range entries {
		if len(e.Name()) > 0 && e.Name()[0] == '.' {
			continue
		}
		victims = append(victims, e)
	}
	if len(victims) != 2 {
		t.Fatalf("want 2 trashed victims, got %d — same-second collision overwrote one victim", len(victims))
	}

	names := map[string]bool{}
	contents := map[string]bool{}
	for _, e := range victims {
		names[e.Name()] = true
		data, err := os.ReadFile(filepath.Join(trashDir, e.Name()))
		if err != nil {
			t.Fatalf("read trashed file %s: %v", e.Name(), err)
		}
		contents[string(data)] = true
	}
	if len(names) != 2 {
		t.Fatalf("colliding trash names: %v", names)
	}
	if !contents["first"] || !contents["second"] {
		t.Fatalf("a victim's content was lost: %v", contents)
	}
}
