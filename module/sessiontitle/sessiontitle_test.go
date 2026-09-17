package sessiontitle

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	coresession "github.com/go-gocel/gocel/core/session"
	"github.com/go-gocel/gocel/core/types"
)

func newService(t *testing.T, log *coresession.Log, provider TitleProvider) *Service {
	t.Helper()
	s, err := New(Config{Log: log, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestFallback_FirstQualifiedMessage: the deterministic fallback comes from
// the first human message's leading words (bounded by MaxFallbackWords).
func TestFallback_FirstQualifiedMessage(t *testing.T) {
	log := coresession.NewLog("s1")
	log.Append(types.NewSessionEvent(types.SessionEventSystem, types.NewSystemMessage("sys")))
	log.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("Please  fix   the build   failure now and also this")))
	s := newService(t, log, nil)

	// Default word bound is 8: the first 8 words, whitespace-normalized.
	if got := s.Get(); got != "Please fix the build failure now and also" {
		t.Fatalf("fallback = %q, want the bounded leading words", got)
	}
}

// TestFallback_EmptyWithoutHumanMessage: no qualified message yields an
// empty fallback.
func TestFallback_EmptyWithoutHumanMessage(t *testing.T) {
	log := coresession.NewLog("s1")
	log.Append(types.NewSessionEvent(types.SessionEventSystem, types.NewSystemMessage("sys")))
	s := newService(t, log, nil)
	if got := s.Get(); got != "" {
		t.Fatalf("fallback = %q, want empty", got)
	}
}

// TestRename_PinsDurably: Rename persists the title in the log; a rebuilt
// log (restart) recovers it, and Auto never overrides it.
func TestRename_PinsDurably(t *testing.T) {
	log := coresession.NewLog("s1")
	log.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("hello world")))
	s := newService(t, log, nil)
	if err := s.Rename("My Pinned Title"); err != nil {
		t.Fatal(err)
	}
	if got := s.Get(); got != "My Pinned Title" {
		t.Fatalf("Get = %q, want the pinned title", got)
	}

	// Rebuild the log: the title survives.
	rebuilt := coresession.NewLog("s1")
	if err := rebuilt.Restore(log.Events()); err != nil {
		t.Fatal(err)
	}
	s2 := newService(t, rebuilt, nil)
	if got := s2.Get(); got != "My Pinned Title" {
		t.Fatalf("rebuilt Get = %q, want the pinned title", got)
	}
	// Auto on a pinned title leaves it untouched.
	if got, err := s2.Auto(context.Background()); err != nil || got != "My Pinned Title" {
		t.Fatalf("Auto on pinned = %q, %v; want the pinned title", got, err)
	}
}

// TestAuto_ProviderRefines: the provider refines the fallback; a provider
// failure keeps the fallback (the run never fails because titling failed).
func TestAuto_ProviderRefines(t *testing.T) {
	log := coresession.NewLog("s1")
	log.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("fix the database connection timeout")))
	s := newService(t, log, &fakeProvider{out: "DB Timeout Fix"})
	got, err := s.Auto(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "DB Timeout Fix" {
		t.Fatalf("Auto = %q, want the provider refinement", got)
	}

	log2 := coresession.NewLog("s2")
	log2.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage("fix the database connection timeout")))
	s2 := newService(t, log2, &fakeProvider{err: errFake})
	got, err = s2.Auto(context.Background())
	if err != nil {
		t.Fatalf("Auto with failing provider = %v, want the fallback kept", err)
	}
	if got != "fix the database connection timeout" {
		t.Fatalf("Auto with failing provider = %q, want the fallback", got)
	}
}

// TestFallback_ByteBoundNeverSplitsRune: a multi-byte rune straddling the
// byte bound is preserved whole — the cut steps back to a rune start.
func TestFallback_ByteBoundNeverSplitsRune(t *testing.T) {
	log := coresession.NewLog("s1")
	// 汉字是 3 字节；80 字节的边界会落在某个汉字中间。
	content := strings.Repeat("测", 30) // 90 字节
	log.Append(types.NewSessionEvent(types.SessionEventUserMessage, types.NewUserMessage(content)))
	s, err := New(Config{Log: log})
	if err != nil {
		t.Fatal(err)
	}
	got := s.Get()
	if len(got) > 80 {
		t.Fatalf("fallback length = %d, want <= 80", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("fallback must be valid UTF-8 (never split a rune)")
	}
	// Tiny bound: must not panic and must stay valid.
	s2, err := New(Config{Log: log, MaxFallbackBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	got2 := s2.Get()
	if !utf8.ValidString(got2) {
		t.Fatalf("1-byte bound fallback = %q, must be valid UTF-8", got2)
	}
}

type fakeProvider struct {
	out string
	err error
}

func (f *fakeProvider) Refine(_ context.Context, _ string, _ []*types.Message) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.out, nil
}

var errFake = &fakeError{}

type fakeError struct{}

func (e *fakeError) Error() string { return "fake provider failure" }
