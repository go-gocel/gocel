package sessionquery

import (
	"context"
	"testing"

	coresession "github.com/go-gocel/gocel/core/session"
	"github.com/go-gocel/gocel/core/types"
)

func newIndex(t *testing.T) *Index {
	t.Helper()
	idx, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

func logWith(t *testing.T, id string, contents ...string) *coresession.Log {
	t.Helper()
	log := coresession.NewLog(id)
	for _, c := range contents {
		if _, err := log.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage(c))); err != nil {
			t.Fatal(err)
		}
	}
	return log
}

// TestSearch_FindsAcrossLogs: the index finds the term across sessions and
// returns ranked hits.
func TestSearch_FindsAcrossLogs(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	logs := map[string]*coresession.Log{
		"s1": logWith(t, "s1", "fix the database connection timeout"),
		"s2": logWith(t, "s2", "deploy the new frontend build"),
		"s3": logWith(t, "s3", "the database migration ran fine"),
	}
	if err := idx.IndexLogs(ctx, logs); err != nil {
		t.Fatal(err)
	}
	hits, err := idx.Search(ctx, Query{Term: "database"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2 (s1, s3)", len(hits))
	}
	// Ranked: s1 (1 span) and s3 (1 span) — order by rank, both present.
	found := map[string]bool{}
	for _, h := range hits {
		found[h.SessionID] = true
	}
	if !found["s1"] || !found["s3"] {
		t.Fatalf("hits = %+v, want s1 and s3", hits)
	}
}

// TestSearch_LiteralPhrase: FTS syntax in the term is data, not a query —
// a search for `OR` does not become a boolean operator.
func TestSearch_LiteralPhrase(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	logs := map[string]*coresession.Log{
		"s1": logWith(t, "s1", "alpha OR beta"),
		"s2": logWith(t, "s2", "alpha beta"),
	}
	if err := idx.IndexLogs(ctx, logs); err != nil {
		t.Fatal(err)
	}
	// Searching for the literal "alpha OR beta" matches only s1.
	hits, err := idx.Search(ctx, Query{Term: "alpha OR beta"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].SessionID != "s1" {
		t.Fatalf("literal phrase hits = %+v, want only s1", hits)
	}
}

// TestSearch_EmptyTermAndNoMatch: empty terms return nothing; no-match
// returns nothing; FTS errors degrade to empty (never wrong).
func TestSearch_EmptyTermAndNoMatch(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	logs := map[string]*coresession.Log{"s1": logWith(t, "s1", "hello world")}
	if err := idx.IndexLogs(ctx, logs); err != nil {
		t.Fatal(err)
	}
	hits, err := idx.Search(ctx, Query{Term: "  "})
	if err != nil || len(hits) != 0 {
		t.Fatalf("empty term = %+v, %v; want none", hits, err)
	}
	hits, err = idx.Search(ctx, Query{Term: "absent-term"})
	if err != nil || len(hits) != 0 {
		t.Fatalf("no match = %+v, %v; want none", hits, err)
	}
	// A single-character term is unindexable by FTS5 — degrade to empty.
	hits, err = idx.Search(ctx, Query{Term: "h"})
	if err != nil || len(hits) != 0 {
		t.Fatalf("single char = %+v, %v; want empty (degrade)", hits, err)
	}
}

// TestSearch_LimitClamped: invalid page sizes fall back (<=0 → default 20)
// and oversized ones are capped (>100 → 100) — a caller can never request
// an unbounded result set.
func TestSearch_LimitClamped(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	var contents []string
	for i := 0; i < 150; i++ {
		contents = append(contents, "needle payload")
	}
	if err := idx.IndexLogs(ctx, map[string]*coresession.Log{"s1": logWith(t, "s1", contents...)}); err != nil {
		t.Fatal(err)
	}
	hits, err := idx.Search(ctx, Query{Term: "needle", Limit: -5})
	if err != nil || len(hits) != 20 {
		t.Fatalf("negative limit = %d hits, %v; want 20 (default)", len(hits), err)
	}
	hits, err = idx.Search(ctx, Query{Term: "needle", Limit: 9999})
	if err != nil || len(hits) != 100 {
		t.Fatalf("oversized limit = %d hits, %v; want 100 (cap)", len(hits), err)
	}
}

// TestIndexLogs_ReplacesContent: re-indexing replaces the previous content
// (the index always converges to the logs).
func TestIndexLogs_ReplacesContent(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()
	if err := idx.IndexLogs(ctx, map[string]*coresession.Log{"s1": logWith(t, "s1", "first version")}); err != nil {
		t.Fatal(err)
	}
	hits, _ := idx.Search(ctx, Query{Term: "first"})
	if len(hits) != 1 {
		t.Fatalf("first index hits = %d, want 1", len(hits))
	}
	// Replace: s1 now has different content; the old term disappears.
	if err := idx.IndexLogs(ctx, map[string]*coresession.Log{"s1": logWith(t, "s1", "second version")}); err != nil {
		t.Fatal(err)
	}
	hits, _ = idx.Search(ctx, Query{Term: "first"})
	if len(hits) != 0 {
		t.Fatalf("stale term still indexed: %+v", hits)
	}
	hits, _ = idx.Search(ctx, Query{Term: "second"})
	if len(hits) != 1 {
		t.Fatalf("new term hits = %d, want 1", len(hits))
	}
}
