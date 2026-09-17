package session

import (
	"context"
	"testing"

	coresession "github.com/go-gocel/gocel/core/session"
	"github.com/go-gocel/gocel/core/types"
)

// TestLogSession_DerivesMessagesFromLog: message history is a projection of
// the event log — appends extend it in order, and the log is the truth.
func TestLogSession_DerivesMessagesFromLog(t *testing.T) {
	ls := NewLogSession("agent-a")
	ls.AddMessage(types.NewUserMessage("u1"))
	ls.AddMessage(types.NewAssistantMessage("a1"))
	ls.AddMessage(types.NewUserMessage("u2"))

	msgs := ls.GetMessages()
	if len(msgs) != 3 {
		t.Fatalf("derived = %d, want 3", len(msgs))
	}
	if msgs[0].Content != "u1" || msgs[1].Content != "a1" || msgs[2].Content != "u2" {
		t.Fatalf("derived order = %q %q %q", msgs[0].Content, msgs[1].Content, msgs[2].Content)
	}
	// The log holds every event (the truth).
	if ls.Log().Len() != 3 || ls.Log().Seq() != 3 {
		t.Fatalf("log len/seq = %d/%d, want 3/3", ls.Log().Len(), ls.Log().Seq())
	}
	if ls.ID() == "" || ls.AgentName() != "agent-a" || ls.Status() != "active" {
		t.Fatalf("session identity = %+v", ls)
	}
}

// TestLogSession_PersistReplayRoundTrip: a log-backed session survives
// Store save → load with its full history reconstructed from the log.
func TestLogSession_PersistReplayRoundTrip(t *testing.T) {
	store := coresession.NewMemoryStore()
	svc := NewLogSessionService(store)
	ctx := context.Background()

	ls, err := svc.Create(ctx, "agent-a", "user-1", map[string]any{"project": "x"})
	if err != nil {
		t.Fatal(err)
	}
	ls.AddMessage(types.NewUserMessage("hello"))
	ls.AddMessage(types.NewAssistantMessage("hi"))
	ls.AddTokenUsage(42)
	if err := svc.Save(ctx, ls); err != nil {
		t.Fatal(err)
	}

	loaded, err := svc.Get(ctx, ls.ID())
	if err != nil {
		t.Fatal(err)
	}
	msgs := loaded.GetMessages()
	if len(msgs) != 2 || msgs[0].Content != "hello" || msgs[1].Content != "hi" {
		t.Fatalf("replayed messages = %+v, want [hello hi]", msgs)
	}
	if loaded.UsedTokens() != 42 {
		t.Fatalf("used tokens = %d, want 42", loaded.UsedTokens())
	}
	if loaded.Meta()["project"] != "x" {
		t.Fatalf("meta = %+v, want project=x", loaded.Meta())
	}
}

// TestLogSession_TrimKeepsLog: trimming replaces the surface view while the
// shadowed history stays in the log (DSH compaction-style).
func TestLogSession_TrimKeepsLog(t *testing.T) {
	ls := NewLogSession("agent-a")
	ls.AddMessage(types.NewUserMessage("old-1"))
	ls.AddMessage(types.NewUserMessage("old-2"))
	ls.AddMessage(types.NewUserMessage("old-3"))
	ls.AddMessage(types.NewUserMessage("new-1"))

	ls.TrimMessages([]*types.Message{types.NewUserMessage("new-1")})
	msgs := ls.GetMessages()
	if len(msgs) == 0 {
		t.Fatal("trimmed session must still derive a surface")
	}
	// The log still holds everything (nothing lost).
	if ls.Log().Len() < 4 {
		t.Fatalf("log len = %d, want >= 4 (shadowed history retained)", ls.Log().Len())
	}
}

// TestLogSessionService_FileStore: the durable backend is swappable — a
// file store round-trips the same way.
func TestLogSessionService_FileStore(t *testing.T) {
	store, err := coresession.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLogSessionService(store)
	ctx := context.Background()

	ls, err := svc.Create(ctx, "agent-a", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ls.AddMessage(types.NewUserMessage("persisted"))
	if err := svc.Save(ctx, ls); err != nil {
		t.Fatal(err)
	}
	loaded, err := svc.Get(ctx, ls.ID())
	if err != nil {
		t.Fatal(err)
	}
	if msgs := loaded.GetMessages(); len(msgs) != 1 || msgs[0].Content != "persisted" {
		t.Fatalf("file round-trip = %+v", msgs)
	}
	if err := svc.Delete(ctx, ls.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, ls.ID()); err == nil {
		t.Fatal("deleted session must be gone")
	}
}
