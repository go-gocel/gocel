package toolutil

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFileVersion_StableAndSensitive: the fingerprint is stable while the
// file is untouched and changes when the content changes.
func TestFileVersion_StableAndSensitive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.txt")
	if err := os.WriteFile(path, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	v1, err := FileVersion(path)
	if err != nil {
		t.Fatal(err)
	}
	// Same content, same mtime window: the fingerprint must not change on
	// re-stat alone.
	v1b, err := FileVersion(path)
	if err != nil {
		t.Fatal(err)
	}
	if v1 != v1b {
		t.Fatalf("fingerprint changed without a write: %q vs %q", v1, v1b)
	}
	// A content change must change the fingerprint (size differs).
	if err := os.WriteFile(path, []byte("one two three"), 0o644); err != nil {
		t.Fatal(err)
	}
	v2, err := FileVersion(path)
	if err != nil {
		t.Fatal(err)
	}
	if v1 == v2 {
		t.Fatal("fingerprint must change when the content changes")
	}
}

// TestCheckVersion_StaleDetection: a mismatched expectation is a
// StaleVersionError; an empty expectation is unguarded (legacy).
func TestCheckVersion_StaleDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cur, _ := FileVersion(path)

	if err := CheckVersion(path, cur); err != nil {
		t.Fatalf("matching version = %v, want nil", err)
	}
	if err := CheckVersion(path, "stale-fingerprint"); !IsStaleVersion(err) {
		t.Fatalf("stale version = %v, want StaleVersionError", err)
	}
	if err := CheckVersion(path, ""); err != nil {
		t.Fatalf("empty expectation must be unguarded, got %v", err)
	}
	// A change after capture is detected.
	time.Sleep(2 * time.Millisecond)
	if err := os.WriteFile(path, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckVersion(path, cur); !IsStaleVersion(err) {
		t.Fatalf("changed file = %v, want StaleVersionError", err)
	}
}
