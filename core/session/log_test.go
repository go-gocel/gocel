package session

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/go-gocel/gocel/core/types"
)

// TestAppend_AssignsSeqAndDerives: seqs are monotonic and contiguous, and
// append records extend derived history in order.
func TestAppend_AssignsSeqAndDerives(t *testing.T) {
	l := NewLog("s1")
	msgs := []*types.Message{
		types.NewSystemMessage("sys"),
		types.NewUserMessage("u1"),
		types.NewAssistantMessage("a1"),
	}
	for i, m := range msgs {
		e, err := l.Append(types.NewSessionEvent(types.SessionEventUserMessage, m))
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if e.Seq != int64(i+1) {
			t.Fatalf("seq = %d, want %d", e.Seq, i+1)
		}
	}
	if l.Seq() != 3 {
		t.Fatalf("Seq() = %d, want 3", l.Seq())
	}
	got := l.DeriveMessages()
	if len(got) != 3 {
		t.Fatalf("derived = %d messages, want 3", len(got))
	}
	for i, m := range got {
		if m.Content != msgs[i].Content {
			t.Fatalf("derived[%d] = %q, want %q", i, m.Content, msgs[i].Content)
		}
	}
	evs := l.Events()
	if len(evs) != 3 || evs[0].Seq != 1 || evs[2].Seq != 3 {
		t.Fatalf("events = %+v", evs)
	}
}

// TestAppend_PreassignedSeqRejected: callers never assign seqs.
func TestAppend_PreassignedSeqRejected(t *testing.T) {
	l := NewLog("s1")
	e := types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("x"))
	e.Seq = 7
	if _, err := l.Append(e); !errors.Is(err, ErrSeqAssigned) {
		t.Fatalf("preassigned seq = %v, want ErrSeqAssigned", err)
	}
	if l.Len() != 0 {
		t.Fatal("failed append must not grow the log")
	}
}

// TestReplace_ShadowsRange: a replacement substitutes the shadowed range at
// its position, keeps the shadowed records in the log, and the surface no
// longer contains them.
func TestReplace_ShadowsRange(t *testing.T) {
	l := NewLog("s1")
	for _, c := range []string{"a", "b", "c", "d"} {
		if _, err := l.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage(c))); err != nil {
			t.Fatal(err)
		}
	}
	// Replace [2,3] (b,c) with a summary.
	e, err := l.Append(types.NewReplaceEvent(types.SessionEventUserMessage, 2, 3, types.NewUserMessage("[summary]")))
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if e.Seq != 5 {
		t.Fatalf("replace seq = %d, want 5", e.Seq)
	}
	got := l.DeriveMessages()
	if len(got) != 3 {
		t.Fatalf("derived = %d, want 3 (a, summary, d)", len(got))
	}
	if got[0].Content != "a" || got[1].Content != "[summary]" || got[2].Content != "d" {
		t.Fatalf("derived = %q, %q, %q", got[0].Content, got[1].Content, got[2].Content)
	}
	// The log keeps everything (append-only).
	if l.Len() != 5 {
		t.Fatalf("log len = %d, want 5 (shadowed records retained)", l.Len())
	}
	// The surface seqs reflect the replacement position.
	seqs := l.SurfaceSeq()
	if len(seqs) != 3 || seqs[0] != 1 || seqs[1] != 5 || seqs[2] != 4 {
		t.Fatalf("surface seqs = %v, want [1 5 4]", seqs)
	}
}

// TestReplace_OutOfRangeRejected: ranges outside the surface fail loudly
// and leave both log and surface untouched.
func TestReplace_OutOfRangeRejected(t *testing.T) {
	l := NewLog("s1")
	l.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("a")))
	l.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("b")))

	if _, err := l.Append(types.NewReplaceEvent(types.SessionEventUserMessage, 1, 5, types.NewUserMessage("x"))); !errors.Is(err, ErrSurfaceRange) {
		t.Fatalf("range [1,5] = %v, want ErrSurfaceRange", err)
	}
	if _, err := l.Append(types.NewReplaceEvent(types.SessionEventUserMessage, 3, 3, types.NewUserMessage("x"))); !errors.Is(err, ErrSurfaceRange) {
		t.Fatalf("seq 3 not on surface = %v, want ErrSurfaceRange", err)
	}
	if _, err := l.Append(types.NewReplaceEvent(types.SessionEventUserMessage, 0, 1, types.NewUserMessage("x"))); !errors.Is(err, ErrSurfaceRange) {
		t.Fatalf("from=0 = %v, want ErrSurfaceRange", err)
	}
	if l.Len() != 2 || len(l.DeriveMessages()) != 2 {
		t.Fatalf("failed replaces must not change the log: len=%d derived=%d", l.Len(), len(l.DeriveMessages()))
	}
}

// TestReplace_AfterPriorReplace: a range crossing a prior replacement node
// is rejected because the shadowed seqs are no longer on the surface.
func TestReplace_AfterPriorReplace(t *testing.T) {
	l := NewLog("s1")
	for _, c := range []string{"a", "b", "c"} {
		l.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage(c)))
	}
	if _, err := l.Append(types.NewReplaceEvent(types.SessionEventUserMessage, 2, 2, types.NewUserMessage("b2"))); err != nil {
		t.Fatal(err)
	}
	// seq 2 is no longer on the surface; [1,2] spans the replacement node.
	if _, err := l.Append(types.NewReplaceEvent(types.SessionEventUserMessage, 1, 2, types.NewUserMessage("x"))); !errors.Is(err, ErrSurfaceRange) {
		t.Fatalf("range crossing a prior replacement = %v, want ErrSurfaceRange", err)
	}
	// A replace over the current surface nodes works: surface is [1, 4, 3].
	if _, err := l.Append(types.NewReplaceEvent(types.SessionEventUserMessage, 4, 4, types.NewUserMessage("b3"))); err != nil {
		t.Fatalf("replace seq 4 (the prior replacement node): %v", err)
	}
	got := l.DeriveMessages()
	if len(got) != 3 || got[1].Content != "b3" {
		t.Fatalf("derived = %+v, want [a b3 c]", got)
	}
}

// TestLogOnly_StaysOutOfHistory: log-only records never join derived
// history but remain in the log.
func TestLogOnly_StaysOutOfHistory(t *testing.T) {
	l := NewLog("s1")
	l.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("u")))
	l.Append(types.NewLogOnlyEvent(types.SessionEventCheckpoint, map[string]any{"step": 1}))
	if len(l.DeriveMessages()) != 1 {
		t.Fatalf("derived = %d, want 1 (log-only excluded)", len(l.DeriveMessages()))
	}
	if l.Len() != 2 {
		t.Fatalf("log len = %d, want 2", l.Len())
	}
}

// TestSubscribe_ReceivesCommittedRecords: subscribers see every committed
// record; cancel stops delivery; a nil-message append record still
// notifies (log-only kinds notify too).
func TestSubscribe_ReceivesCommittedRecords(t *testing.T) {
	l := NewLog("s1")
	var mu sync.Mutex
	var got []types.SessionEvent
	cancel := l.Subscribe(func(e types.SessionEvent) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	})
	l.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("a")))
	l.Append(types.NewLogOnlyEvent(types.SessionEventCheckpoint, nil))
	cancel()
	l.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("b")))

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("subscribed records = %+v, want seqs [1 2]", got)
	}
}

// TestClose_SealsTheLog: appends fail after Close; reads still work.
func TestClose_SealsTheLog(t *testing.T) {
	l := NewLog("s1")
	l.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("a")))
	l.Close()
	if _, err := l.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("b"))); !errors.Is(err, ErrClosed) {
		t.Fatalf("append after close = %v, want ErrClosed", err)
	}
	if len(l.DeriveMessages()) != 1 {
		t.Fatal("reads must still work after close")
	}
}

// TestConcurrentAppend_ContiguousSeqs: concurrent appends assign
// contiguous seqs with no duplicates.
func TestConcurrentAppend_ContiguousSeqs(t *testing.T) {
	l := NewLog("s1")
	var wg sync.WaitGroup
	seqs := make(chan int64, 200)
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			e, err := l.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage(string(rune('a'+n%26)))))
			if err != nil {
				t.Errorf("append: %v", err)
				return
			}
			seqs <- e.Seq
		}(i)
	}
	wg.Wait()
	close(seqs)
	seen := make(map[int64]bool)
	for s := range seqs {
		if seen[s] {
			t.Fatalf("duplicate seq %d", s)
		}
		seen[s] = true
	}
	if len(seen) != 200 {
		t.Fatalf("seqs = %d, want 200", len(seen))
	}
	for i := int64(1); i <= 200; i++ {
		if !seen[i] {
			t.Fatalf("seq %d missing", i)
		}
	}
	if len(l.DeriveMessages()) != 200 {
		t.Fatalf("derived = %d, want 200", len(l.DeriveMessages()))
	}
}

// TestJSONRoundTrip: events survive JSON serialization losslessly (the
// persistence contract for products mounting a durable backend).
func TestJSONRoundTrip(t *testing.T) {
	l := NewLog("s1")
	l.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("hello")))
	l.Append(types.NewReplaceEvent(types.SessionEventUserMessage, 1, 1, types.NewUserMessage("[sum]")))
	l.Append(types.NewLogOnlyEvent(types.SessionEventCheckpoint, map[string]any{"step": 2}))

	data, err := json.Marshal(l.Events())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back []types.SessionEvent
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back) != 3 {
		t.Fatalf("round-trip = %d events, want 3", len(back))
	}
	if back[0].Seq != 1 || back[0].Kind != types.SessionEventUserMessage || back[0].Message.Content != "hello" {
		t.Fatalf("event[0] = %+v", back[0])
	}
	if back[1].Surface != types.SessionSurfaceReplace || back[1].SourceFrom != 1 || back[1].SourceTo != 1 {
		t.Fatalf("event[1] = %+v", back[1])
	}
	if back[2].Surface != types.SessionSurfaceLogOnly || back[2].Meta["step"] != float64(2) {
		t.Fatalf("event[2] = %+v", back[2])
	}
}

// TestRestore_ThenSubscribe_AppendNotifies: after Restore the log is fully
// usable — a subscriber established afterwards receives every subsequent
// commit, the restored records stay readable, and the watermark reflects
// the restored seq. (Restore itself commits before any subscription
// exists; consumers catch up via Events(), exactly like hub.Subscribe never
// replays.)
func TestRestore_ThenSubscribe_AppendNotifies(t *testing.T) {
	src := NewLog("s1")
	src.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("a")))
	src.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("b")))
	restored := NewLog("s1")
	if err := restored.Restore(src.Events()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restored.Seq() != 2 {
		t.Fatalf("watermark after restore = %d, want 2", restored.Seq())
	}
	if got := len(restored.DeriveMessages()); got != 2 {
		t.Fatalf("derived after restore = %d, want 2", got)
	}

	var mu sync.Mutex
	var got []types.SessionEvent
	cancel := restored.Subscribe(func(e types.SessionEvent) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	})
	defer cancel()
	// Only the post-restore commit reaches the subscriber.
	restored.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("c")))
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].Seq != 3 {
		t.Fatalf("subscribed after restore = %+v, want just seq 3", got)
	}
}

// TestRestore_RebuildsFromPersistedEvents: Restore reconstructs the log and
// its derived surface from a persisted seq-contiguous event list — the
// durable backend's load path.
func TestRestore_RebuildsFromPersistedEvents(t *testing.T) {
	src := NewLog("s1")
	src.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("a")))
	src.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("b")))
	src.Append(types.NewReplaceEvent(types.SessionEventUserMessage, 2, 2, types.NewUserMessage("b2")))

	restored := NewLog("s1")
	if err := restored.Restore(src.Events()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restored.Seq() != 3 || restored.Len() != 3 {
		t.Fatalf("restored seq/len = %d/%d, want 3/3", restored.Seq(), restored.Len())
	}
	got := restored.DeriveMessages()
	if len(got) != 2 || got[0].Content != "a" || got[1].Content != "b2" {
		t.Fatalf("restored surface = %+v, want [a b2]", got)
	}
	// Further appends continue after the restored watermark.
	e, err := restored.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("c")))
	if err != nil || e.Seq != 4 {
		t.Fatalf("append after restore = %+v, %v; want seq 4", e, err)
	}
}

// TestRestore_RejectsNonEmptyLog: Restore is only valid on an empty log.
func TestRestore_RejectsNonEmptyLog(t *testing.T) {
	l := NewLog("s1")
	l.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("a")))
	if err := l.Restore(l.Events()); err == nil {
		t.Fatal("Restore on a non-empty log must fail")
	}
}

// TestRestore_RejectsNonContiguousSeqs: persisted events must be
// seq-contiguous — the load contract.
func TestRestore_RejectsNonContiguousSeqs(t *testing.T) {
	l := NewLog("s1")
	bad := []types.SessionEvent{
		{Seq: 1, Kind: types.SessionEventUserMessage, Surface: types.SessionSurfaceAppend, Message: types.NewUserMessage("a")},
		{Seq: 3, Kind: types.SessionEventUserMessage, Surface: types.SessionSurfaceAppend, Message: types.NewUserMessage("b")},
	}
	if err := l.Restore(bad); err == nil {
		t.Fatal("non-contiguous seqs must fail Restore")
	}
	// The failed restore leaves the log untouched (fail-closed).
	if l.Len() != 0 || l.Seq() != 0 {
		t.Fatalf("failed restore must leave the log empty, got len=%d seq=%d", l.Len(), l.Seq())
	}
}

// TestRestore_RollsBackOnSurfaceFailure: a restore whose replacement range
// is not on the surface fails BEFORE committing — the log stays empty and
// usable (no half-built state).
func TestRestore_RollsBackOnSurfaceFailure(t *testing.T) {
	l := NewLog("s1")
	// A replace referencing seq 5 with only one append event on the
	// surface: the range is not fully contained → Restore must fail and
	// roll back.
	bad := []types.SessionEvent{
		{Seq: 1, Kind: types.SessionEventUserMessage, Surface: types.SessionSurfaceAppend, Message: types.NewUserMessage("a")},
		{Seq: 2, Kind: types.SessionEventUserMessage, Surface: types.SessionSurfaceReplace, SourceFrom: 1, SourceTo: 5, Message: types.NewUserMessage("x")},
	}
	if err := l.Restore(bad); err == nil {
		t.Fatal("out-of-surface replace must fail Restore")
	}
	if l.Len() != 0 || l.Seq() != 0 || len(l.DeriveMessages()) != 0 {
		t.Fatalf("failed restore must leave the log untouched, got len=%d seq=%d derived=%d",
			l.Len(), l.Seq(), len(l.DeriveMessages()))
	}
	// The log is still usable for a correct restore.
	good := []types.SessionEvent{
		{Seq: 1, Kind: types.SessionEventUserMessage, Surface: types.SessionSurfaceAppend, Message: types.NewUserMessage("a")},
	}
	if err := l.Restore(good); err != nil {
		t.Fatalf("restore after rollback = %v, want nil", err)
	}
	if msgs := l.DeriveMessages(); len(msgs) != 1 || msgs[0].Content != "a" {
		t.Fatalf("derived = %+v, want [a]", msgs)
	}
}
