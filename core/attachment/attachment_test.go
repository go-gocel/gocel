package attachment

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSave_ContentAddressedAndImmutable: the id is the sha256 of the bytes;
// saving identical bytes returns the same id (natural deduplication).
func TestSave_ContentAddressedAndImmutable(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ref, err := s.Save(ctx, "image/png", []byte("PNGDATA"), "a.png")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref.ID, "sha256:") || ref.Bytes != 7 {
		t.Fatalf("ref = %+v", ref)
	}
	ref2, err := s.Save(ctx, "image/png", []byte("PNGDATA"), "b.png")
	if err != nil {
		t.Fatal(err)
	}
	if ref2.ID != ref.ID {
		t.Fatalf("identical bytes must deduplicate: %s vs %s", ref.ID, ref2.ID)
	}
}

// TestRetrieve_VerifiesAndRoundTrips: retrieval returns the exact bytes and
// verifies the content address; a missing object is ErrNotFound.
func TestRetrieve_VerifiesAndRoundTrips(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	data := []byte("hello attachment")
	ref, err := s.Save(ctx, "text/plain", data, "h.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Retrieve(ctx, ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("round-trip = %q, want %q", got, data)
	}
	if _, err := s.Retrieve(ctx, "sha256:"+strings.Repeat("0", 64)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing object = %v, want ErrNotFound", err)
	}
	if ok, _ := s.Has(ctx, ref.ID); !ok {
		t.Fatal("Has must report the stored object")
	}
}

// TestRetrieve_CorruptDetected: tampering with the stored object is
// detected by the content-address verification (ErrCorrupt).
func TestRetrieve_CorruptDetected(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ref, err := s.Save(ctx, "text/plain", []byte("original"), "x.txt")
	if err != nil {
		t.Fatal(err)
	}
	// Tamper with the object file directly.
	obj := s.objectPath(ref.ID)
	if err := os.WriteFile(obj, []byte("TAMPERED"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retrieve(ctx, ref.ID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("tampered object = %v, want ErrCorrupt", err)
	}
}

// TestSave_RejectsEmpty: empty input fails before any write.
func TestSave_RejectsEmpty(t *testing.T) {
	s, _ := NewFileStore(t.TempDir())
	if _, err := s.Save(context.Background(), "image/png", nil, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty save = %v, want ErrInvalidInput", err)
	}
}

// TestRetrieve_RejectsInvalidIDs: ids that are not content addresses are
// rejected at the entry with ErrInvalidInput — a malformed id must never be
// joined into a file path (defense in depth beyond the content-address
// verification). Well-shaped ids keep their honest errors: ErrNotFound when
// absent, ErrCorrupt when tampered.
func TestRetrieve_RejectsInvalidIDs(t *testing.T) {
	s, _ := NewFileStore(t.TempDir())
	ctx := context.Background()
	for _, id := range []string{
		"",
		"sha256:not-hex",
		"sha256:abc",                            // too short
		"sha256:" + strings.Repeat("a", 64) + "x", // too long
		"../../etc/passwd",                      // traversal without scheme
		"sha256:..%2f..%2fetc%2fpasswd",         // traversal-looking payload
	} {
		if _, err := s.Retrieve(ctx, id); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Retrieve(%q) = %v, want ErrInvalidInput", id, err)
		}
		if _, err := s.Has(ctx, id); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Has(%q) = %v, want ErrInvalidInput", id, err)
		}
	}
	// Well-shaped but absent: ErrNotFound, not ErrInvalidInput.
	if _, err := s.Retrieve(ctx, "sha256:"+strings.Repeat("0", 64)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent well-shaped id = %v, want ErrNotFound", err)
	}
}

// TestConcurrentSave_SameBytes: concurrent saves of the same bytes both
// succeed (the link race resolves to the same immutable object).
func TestConcurrentSave_SameBytes(t *testing.T) {
	s, _ := NewFileStore(t.TempDir())
	ctx := context.Background()
	data := []byte("concurrent immutable bytes")
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := s.Save(ctx, "text/plain", data, "c.txt")
			done <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent save: %v", err)
		}
	}
	if !strings.HasPrefix(s.objectPath(ContentID(data)), filepath.Join(s.dir, "objects")) {
		t.Fatal("object must live under the store objects dir")
	}
}
