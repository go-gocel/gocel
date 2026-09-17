package spill

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryStore_RoundTripAndFence(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	ref, err := s.SaveText(ctx, "s1", "hello spill")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Locator == "" || ref.Bytes != len("hello spill") {
		t.Fatalf("ref = %+v, want locator + bytes", ref)
	}
	got, err := s.Retrieve(ctx, "s1", ref.Locator)
	if err != nil || got != "hello spill" {
		t.Fatalf("Retrieve = %q, %v", got, err)
	}
	// Owner fence: another owner cannot retrieve the locator.
	if _, err := s.Retrieve(ctx, "s2", ref.Locator); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner Retrieve = %v, want ErrNotFound", err)
	}
}

func TestFileStore_RoundTripAndFence(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	ref, err := s.SaveText(ctx, "s1", "big output")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref.Locator, "s1"+string(filepath.Separator)) {
		t.Fatalf("locator = %q, want owner-prefixed", ref.Locator)
	}
	got, err := s.Retrieve(ctx, "s1", ref.Locator)
	if err != nil || got != "big output" {
		t.Fatalf("Retrieve = %q, %v", got, err)
	}
	// Owner fence + traversal attempts are rejected.
	if _, err := s.Retrieve(ctx, "s2", ref.Locator); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner Retrieve = %v, want ErrNotFound", err)
	}
	if _, err := s.Retrieve(ctx, "s1", "../../../etc/passwd"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("traversal Retrieve = %v, want ErrNotFound", err)
	}
}
