package goal

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestCreate_Validation(t *testing.T) {
	m := NewManager(NewMemoryStore())
	ctx := context.Background()

	if _, err := m.Create(ctx, "   ", 0); !errors.Is(err, ErrInvalidObjective) {
		t.Fatalf("empty objective = %v, want ErrInvalidObjective", err)
	}
	if _, err := m.Create(ctx, "x", -1); !errors.Is(err, ErrInvalidMaxRounds) {
		t.Fatalf("negative maxRounds = %v, want ErrInvalidMaxRounds", err)
	}
	g, err := m.Create(ctx, "ship it", 5)
	if err != nil {
		t.Fatal(err)
	}
	if g.Phase != PhaseActive || g.Revision != 1 || g.Rounds != 0 || g.MaxRounds != 5 {
		t.Fatalf("created goal = %+v, want active rev1 rounds0 cap5", g)
	}
	if got, err := m.Get(ctx, g.ID); err != nil || got.Objective != "ship it" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := m.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) = %v, want ErrNotFound", err)
	}
}

func TestPauseResumeComplete_Transitions(t *testing.T) {
	m := NewManager(NewMemoryStore())
	ctx := context.Background()
	g, _ := m.Create(ctx, "objective", 0)

	// pause from active; pause again is illegal.
	if _, err := m.Pause(ctx, g.ID); err != nil {
		t.Fatalf("pause = %v", err)
	}
	if _, err := m.Pause(ctx, g.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("double pause = %v, want ErrInvalidTransition", err)
	}
	// resume from paused.
	g, err := m.Resume(ctx, g.ID)
	if err != nil || g.Phase != PhaseActive {
		t.Fatalf("resume = %+v, %v; want active", g, err)
	}
	// complete from active; completing again is an idempotent no-op.
	g, err = m.Complete(ctx, g.ID)
	if err != nil || g.Phase != PhaseComplete {
		t.Fatalf("complete = %+v, %v; want complete", g, err)
	}
	if g2, err := m.Complete(ctx, g.ID); err != nil || g2.Phase != PhaseComplete {
		t.Fatalf("complete twice = %+v, %v; want complete, nil", g2, err)
	}
	// nothing transitions out of complete.
	if _, err := m.Pause(ctx, g.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("pause completed = %v, want ErrInvalidTransition", err)
	}
	if _, err := m.StartRound(ctx, g.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("startRound completed = %v, want ErrInvalidTransition", err)
	}
}

func TestResume_ClearsBlocker(t *testing.T) {
	m := NewManager(NewMemoryStore())
	ctx := context.Background()
	g, _ := m.Create(ctx, "objective", 0)
	if _, err := m.Block(ctx, g.ID, "env missing"); err != nil {
		t.Fatal(err)
	}
	g, err := m.Resume(ctx, g.ID)
	if err != nil || g.BlockerReason != "" || g.BlockedStreak != 0 {
		t.Fatalf("resume after block = %+v, %v; want cleared blocker", g, err)
	}
}

func TestBlock_HostAndAutonomousLines(t *testing.T) {
	m := NewManager(NewMemoryStore())
	ctx := context.Background()
	g, _ := m.Create(ctx, "objective", 0)

	// No reason → rejected.
	if _, err := m.Block(ctx, g.ID, "  "); !errors.Is(err, ErrInvalidBlockReason) {
		t.Fatalf("block without reason = %v, want ErrInvalidBlockReason", err)
	}

	// Host block: active → blocked, streak 1.
	g, err := m.Block(ctx, g.ID, "flaky env")
	if err != nil || g.Phase != PhaseBlocked || g.BlockedStreak != 1 {
		t.Fatalf("host block = %+v, %v; want blocked streak1", g, err)
	}
	// Same reason extends the streak (host path).
	g, _ = m.Block(ctx, g.ID, "flaky env")
	if g.BlockedStreak != 2 {
		t.Fatalf("host re-block streak = %d, want 2", g.BlockedStreak)
	}
	// Different reason restarts it.
	g, _ = m.Block(ctx, g.ID, "different")
	if g.BlockedStreak != 1 || g.BlockerReason != "different" {
		t.Fatalf("host re-block different = %+v, want streak1 different", g)
	}

	// Autonomous line: a round can never block an active goal.
	if _, err := m.Resume(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Block(ctx, g.ID, "round says so", WithAutonomousBlock(3)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("autonomous block on active = %v, want ErrInvalidTransition", err)
	}
	// Autonomous continuation needs the streak to persist first.
	_, _ = m.Block(ctx, g.ID, "round says so") // host: streak 1
	if _, err := m.Block(ctx, g.ID, "round says so", WithAutonomousBlock(3)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("autonomous block at streak1 = %v, want ErrInvalidTransition", err)
	}
	_, _ = m.Block(ctx, g.ID, "round says so") // host: streak 2 (or a prior round's continuation)
	g, err = m.Block(ctx, g.ID, "round says so", WithAutonomousBlock(3))
	if err != nil || g.BlockedStreak != 3 {
		t.Fatalf("autonomous block at streak2 = %+v, %v; want streak3", g, err)
	}
	// A different reason can never be reported autonomously.
	if _, err := m.Block(ctx, g.ID, "other", WithAutonomousBlock(3)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("autonomous block different reason = %v, want ErrInvalidTransition", err)
	}
}

func TestStartRound_CapAndPhases(t *testing.T) {
	m := NewManager(NewMemoryStore())
	ctx := context.Background()
	g, _ := m.Create(ctx, "objective", 2)

	g, err := m.StartRound(ctx, g.ID)
	if err != nil || g.Rounds != 1 {
		t.Fatalf("round 1 = %+v, %v; want rounds1", g, err)
	}
	g, err = m.StartRound(ctx, g.ID)
	if err != nil || g.Rounds != 2 {
		t.Fatalf("round 2 = %+v, %v; want rounds2", g, err)
	}
	if _, err := m.StartRound(ctx, g.ID); !errors.Is(err, ErrMaxRoundsReached) {
		t.Fatalf("round 3 = %v, want ErrMaxRoundsReached", err)
	}
	// Paused goals claim no rounds.
	g2, _ := m.Create(ctx, "other", 0)
	_, _ = m.Pause(ctx, g2.ID)
	if _, err := m.StartRound(ctx, g2.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("startRound paused = %v, want ErrInvalidTransition", err)
	}
}

func TestUpdate_CASRejectsStale(t *testing.T) {
	store := NewMemoryStore()
	m1 := NewManager(store)
	m2 := NewManager(store)
	ctx := context.Background()
	g, _ := m1.Create(ctx, "objective", 0)

	// m2 commits while m1's update function is still in flight.
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var updErr error
	go func() {
		defer close(done)
		_, updErr = m1.Update(ctx, g.ID, func(g *Goal) error {
			close(entered)
			<-release
			g.Objective = "from m1"
			return nil
		})
	}()
	<-entered
	if _, err := m2.Update(ctx, g.ID, func(g *Goal) error {
		g.Objective = "from m2"
		return nil
	}); err != nil {
		t.Fatalf("m2 update = %v", err)
	}
	close(release)
	<-done
	if !errors.Is(updErr, ErrStaleRevision) {
		t.Fatalf("m1 update = %v, want ErrStaleRevision", updErr)
	}
	got, _ := m1.Get(ctx, g.ID)
	if got.Objective != "from m2" {
		t.Fatalf("objective = %q, want from m2", got.Objective)
	}
}

func TestClear_IsNoOpWhenMissing(t *testing.T) {
	m := NewManager(NewMemoryStore())
	ctx := context.Background()
	if err := m.Clear(ctx, "missing"); err != nil {
		t.Fatalf("Clear(missing) = %v, want nil", err)
	}
	g, _ := m.Create(ctx, "x", 0)
	if err := m.Clear(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(ctx, g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Clear = %v, want ErrNotFound", err)
	}
}

func TestMemoryStore_ReturnsCopies(t *testing.T) {
	s := NewMemoryStore()
	g := &Goal{ID: "g1", Objective: "original", Phase: PhaseActive, Revision: 1}
	_ = s.Save(context.Background(), g)
	g.Objective = "mutated"
	got, _ := s.Load(context.Background(), "g1")
	if got.Objective != "original" {
		t.Fatal("store must return copies, not shared state")
	}
	got.Objective = "caller-mutated"
	again, _ := s.Load(context.Background(), "g1")
	if again.Objective != "original" {
		t.Fatal("caller mutation leaked into the store")
	}
}

func TestFileStore_RoundTripAndStrictness(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.Load(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load(missing) = %v, want ErrNotFound", err)
	}
	g := &Goal{ID: "a", Objective: "one", Phase: PhaseActive, Revision: 1}
	if err := s.Save(ctx, g); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx, "a")
	if err != nil || got.Objective != "one" {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v; want 1 goal", list, err)
	}
	if err := s.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "a"); err != nil {
		t.Fatalf("Delete(missing) = %v, want nil", err)
	}
}

func TestManager_ConcurrentRoundClaims(t *testing.T) {
	m := NewManager(NewMemoryStore())
	ctx := context.Background()
	g, _ := m.Create(ctx, "objective", 0)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, errs[n] = m.StartRound(ctx, g.ID)
		}(i)
	}
	wg.Wait()
	// Exactly 8 rounds committed: every claim serializes under the CAS lock.
	got, _ := m.Get(ctx, g.ID)
	if got.Rounds != 8 {
		t.Fatalf("rounds = %d, want 8", got.Rounds)
	}
	for _, err := range errs {
		if err != nil {
			t.Fatalf("round claim = %v, want nil", err)
		}
	}
}
