package spill

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Regression: SaveText joined the caller-supplied owner into the store
// path with no validation — a hostile owner escaped the store directory
// (C7, empirically reproduced). Hostile owners must fail loudly.
func TestFileStore_SaveTextRejectsHostileOwner(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"../../escape", `..\..\escape`, "/abs", "a/b", "a\\b", ""} {
		if _, err := s.SaveText(context.Background(), owner, "payload"); err == nil {
			t.Fatalf("SaveText(owner=%q) = nil error, want rejection", owner)
		}
	}
	// Sanity: a legit owner still works.
	ref, err := s.SaveText(context.Background(), "sess-1", "payload")
	if err != nil {
		t.Fatalf("legit owner failed: %v", err)
	}
	got, err := s.Retrieve(context.Background(), "sess-1", ref.Locator)
	if err != nil || got != "payload" {
		t.Fatalf("round-trip = %q, %v", got, err)
	}
	// And nothing escaped the store directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Base(e.Name()) != e.Name() {
			t.Fatalf("unexpected entry %q", e.Name())
		}
	}
}
