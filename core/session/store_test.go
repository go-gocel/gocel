package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-gocel/gocel/core/types"
)

func sampleEvents() []types.SessionEvent {
	return []types.SessionEvent{
		{Seq: 1, Kind: types.SessionEventUserMessage, Surface: types.SessionSurfaceAppend,
			Message: types.NewUserMessage("hello")},
		{Seq: 2, Kind: types.SessionEventAssistantMessage, Surface: types.SessionSurfaceAppend,
			Message: types.NewAssistantMessage("hi")},
	}
}

// TestFileStore_RoundTrip: save → load returns the identical seq-sorted
// list; list and delete behave.
func TestFileStore_RoundTrip(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Save(ctx, "s1", sampleEvents()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load(ctx, "s1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got[0].Seq != 1 || got[0].Message.Content != "hello" || got[1].Seq != 2 {
		t.Fatalf("loaded = %+v", got)
	}
	ids, err := s.List(ctx)
	if err != nil || len(ids) != 1 || ids[0] != "s1" {
		t.Fatalf("List = %v, %v", ids, err)
	}
	if err := s.Delete(ctx, "s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Load(ctx, "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load after delete = %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, "s1"); err != nil {
		t.Fatalf("delete missing id must be a no-op: %v", err)
	}
}

// TestFileStore_ReplaceWholeDocument: a second Save replaces the previous
// document (snapshot semantics, not append).
func TestFileStore_ReplaceWholeDocument(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewFileStore(dir)
	ctx := context.Background()
	if err := s.Save(ctx, "s1", sampleEvents()); err != nil {
		t.Fatal(err)
	}
	short := []types.SessionEvent{{Seq: 1, Kind: types.SessionEventSystem, Surface: types.SessionSurfaceAppend,
		Message: types.NewSystemMessage("only")}}
	if err := s.Save(ctx, "s1", short); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Load(ctx, "s1")
	if len(got) != 1 || got[0].Message.Content != "only" {
		t.Fatalf("replaced document = %+v", got)
	}
}

// TestFileStore_CorruptDocumentFailsLoud: a truncated or non-contiguous
// document is an error, never silently clipped.
func TestFileStore_CorruptDocumentFailsLoud(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewFileStore(dir)
	// Write a corrupt file directly under the store directory.
	path := filepath.Join(dir, encodeSegment("bad")+".json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(context.Background(), "bad"); err == nil {
		t.Fatal("corrupt document must fail loudly")
	}
	// Non-contiguous seqs are corrupt too.
	if err := os.WriteFile(path, []byte(`[{"seq":1},{"seq":3}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(context.Background(), "bad"); err == nil {
		t.Fatal("non-contiguous seqs must fail loudly")
	}
}

// TestFileStore_IdSanitized: an id with path separators cannot escape the
// store directory.
func TestFileStore_IdSanitized(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewFileStore(dir)
	ctx := context.Background()
	if err := s.Save(ctx, "../../etc/evil", sampleEvents()); err != nil {
		t.Fatalf("Save with hostile id: %v", err)
	}
	got, err := s.Load(ctx, "../../etc/evil")
	if err != nil || len(got) != 2 {
		t.Fatalf("Load hostile id = %d events, %v", len(got), err)
	}
	// Nothing was written outside the store directory.
	if _, err := os.Stat(filepath.Join(dir, "..", "..", "etc", "evil.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("hostile id escaped the store directory")
	}
	// The sanitized name is stable (same id → same document).
	names, _ := s.List(ctx)
	if len(names) != 1 || names[0] != "../../etc/evil" {
		t.Fatalf("listed id = %q, want the original id round-tripped", names[0])
	}
}
