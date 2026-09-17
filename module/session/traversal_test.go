package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Regression: Get/Save/Delete accepted arbitrary caller-controlled session
// ids and joined them into the store path — a traversal id read/wrote/
// deleted files outside the store (C19). Traversal ids must fail loudly.
func TestFileSystemSessionService_RejectsTraversalIDs(t *testing.T) {
	dir := t.TempDir()
	s := NewFileSystemSessionService(dir)
	for _, id := range []string{"..", "../outside", `..\outside`, "/abs", "a/b", ".hidden", ""} {
		if _, err := s.Get(context.Background(), id); err == nil {
			t.Fatalf("Get(%q) = nil error, want rejection", id)
		}
		if err := s.Delete(context.Background(), id); err == nil {
			t.Fatalf("Delete(%q) = nil error, want rejection", id)
		}
	}
	// A legit session id still round-trips.
	sess, err := s.Create(context.Background(), "agent", "user", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background(), sess.ID())
	if err != nil || got == nil || got.ID() != sess.ID() {
		t.Fatalf("round-trip = %+v, %v", got, err)
	}
	// Nothing escaped the store dir.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" && e.Name() != "_index.json" {
			t.Fatalf("unexpected artifact %q", e.Name())
		}
	}
}
