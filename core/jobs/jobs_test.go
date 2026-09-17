package jobs

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/types"
)

// TestTailStore_ConcurrentWriteRead proves the tail store is safe for the
// documented usage: a running job writes output while consumers read the
// incremental cursor. Run under -race; without synchronization this test
// reports a data race on the internal buffer/total fields.
func TestTailStore_ConcurrentWriteRead(t *testing.T) {
	s := newTailStore(4096)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_, _ = s.Write([]byte("chunk-0123456789\n"))
		}
	}()
	since := 0
	for i := 0; i < 2000; i++ {
		buf, total := s.read(since)
		if total < since {
			t.Fatalf("total %d went backwards from %d", total, since)
		}
		since = total
		_ = buf
	}
	wg.Wait()
	if got, _ := s.read(0); len(got) == 0 {
		t.Fatal("nothing readable after writes")
	}
}

// TestStart_StreamJobIncrementalCursor: a stream job's output is readable
// incrementally through a single cursor.
func TestStart_StreamJobIncrementalCursor(t *testing.T) {
	reg := NewRegistry()
	step := make(chan struct{})
	release := make(chan struct{})
	job, err := reg.Start(Spec{
		Kind:  "shell",
		Label: "build",
		Owner: "s1",
		Run: func(ctx context.Context, w io.Writer) (string, error) {
			fmt.Fprint(w, "line1\n")
			close(step)
			<-release
			fmt.Fprint(w, "line2\n")
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(job.ID, "shell-") {
		t.Fatalf("id = %q, want shell- prefix", job.ID)
	}
	<-step

	out, offset, err := reg.Output(job.ID, 0)
	if err != nil || string(out) != "line1\n" {
		t.Fatalf("first output = %q, %v; want line1", out, err)
	}
	close(release)
	settled, err := reg.Wait(context.Background(), job.ID)
	if err != nil || settled.Status != StatusCompleted {
		t.Fatalf("Wait = %+v, %v; want completed", settled, err)
	}
	// The same cursor reads the rest; re-reading from the old offset yields
	// the tail only.
	out2, offset2, err := reg.Output(job.ID, offset)
	if err != nil || string(out2) != "line2\n" {
		t.Fatalf("second output = %q, %v; want line2", out2, err)
	}
	full, _, err := reg.Output(job.ID, 0)
	if err != nil || string(full) != "line1\nline2\n" {
		t.Fatalf("full output = %q, %v", full, err)
	}
	_ = offset2
}

// TestStart_FinalOutputJob: final-output jobs expose their terminal value
// only after completion.
func TestStart_FinalOutputJob(t *testing.T) {
	reg := NewRegistry()
	job, _ := reg.Start(Spec{
		Kind:        "calc",
		Owner:       "s1",
		FinalOutput: true,
		Run: func(ctx context.Context, w io.Writer) (string, error) {
			return "the answer", nil
		},
	})
	settled, err := reg.Wait(context.Background(), job.ID)
	if err != nil || settled.Status != StatusCompleted {
		t.Fatalf("Wait = %+v, %v", settled, err)
	}
	out, _, err := reg.Output(job.ID, 0)
	if err != nil || string(out) != "the answer" {
		t.Fatalf("final output = %q, %v; want the answer", out, err)
	}
}

// TestKill_SettlesAsKilled: Kill cancels the run and the job settles as
// killed.
func TestKill_SettlesAsKilled(t *testing.T) {
	reg := NewRegistry()
	job, _ := reg.Start(Spec{
		Kind:  "shell",
		Owner: "s1",
		Run: func(ctx context.Context, w io.Writer) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	if err := reg.Kill(job.ID); err != nil {
		t.Fatalf("Kill = %v", err)
	}
	settled, err := reg.Wait(context.Background(), job.ID)
	if err != nil || settled.Status != StatusKilled {
		t.Fatalf("Wait = %+v, %v; want killed", settled, err)
	}
}

// TestNotice_SentExactlyOnce: the settlement notice fires exactly once with
// the job payload.
func TestNotice_SentExactlyOnce(t *testing.T) {
	reg := NewRegistry()
	var mu sync.Mutex
	var notices []*types.Notice
	reg.SetNotifier(func(n *types.Notice) {
		mu.Lock()
		notices = append(notices, n)
		mu.Unlock()
	})
	job, _ := reg.Start(Spec{
		Kind:  "shell",
		Label: "fast",
		Owner: "s1",
		Run: func(ctx context.Context, w io.Writer) (string, error) {
			return "", nil
		},
	})
	if _, err := reg.Wait(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	// Wait returns only after settle has run (done closes last), so the
	// notice is already delivered — no sleep needed.
	mu.Lock()
	defer mu.Unlock()
	if len(notices) != 1 || notices[0].Kind != types.NoticeKindJob || notices[0].ID != job.ID || notices[0].Status != string(StatusCompleted) {
		t.Fatalf("notices = %+v, want one completed job notice", notices)
	}
}

// TestOwnerCap: the per-owner concurrency cap rejects further starts.
func TestOwnerCap(t *testing.T) {
	reg := NewRegistry()
	reg.SetMaxPerOwner(2)
	release := make(chan struct{})
	blocking := Spec{
		Kind:  "shell",
		Owner: "s1",
		Run: func(ctx context.Context, w io.Writer) (string, error) {
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "", nil
		},
	}
	if _, err := reg.Start(blocking); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Start(blocking); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Start(blocking); err != ErrJobLimit {
		t.Fatalf("third Start = %v, want ErrJobLimit", err)
	}
	// A different owner is unaffected.
	other := blocking
	other.Owner = "s2"
	if _, err := reg.Start(other); err != nil {
		t.Fatalf("other owner Start = %v, want nil", err)
	}
	close(release)
}

// TestTeardown_ForceFailsWithoutWaiting: teardown cancels the owner's
// running jobs and drops settled records, never blocking.
func TestTeardown_ForceFailsWithoutWaiting(t *testing.T) {
	reg := NewRegistry()
	blocking := Spec{
		Kind:  "shell",
		Owner: "s1",
		Run: func(ctx context.Context, w io.Writer) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}
	job, _ := reg.Start(blocking)

	done := make(chan struct{})
	go func() {
		reg.Teardown("s1")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Teardown blocked on the running job")
	}
	if _, err := reg.Status(job.ID); err != ErrJobNotFound {
		t.Fatalf("Status after teardown = %v, want ErrJobNotFound", err)
	}
}

// TestBoundedStore_OldOffsetsGetAvailableTail: output storage is bounded;
// readers holding an offset older than the retained tail receive the whole
// available tail, and the cursor math stays coherent.
func TestBoundedStore_OldOffsetsGetAvailableTail(t *testing.T) {
	reg := NewRegistry()
	job, _ := reg.Start(Spec{
		Kind:          "shell",
		Owner:         "s1",
		StoreCapBytes: 16,
		Run: func(ctx context.Context, w io.Writer) (string, error) {
			fmt.Fprint(w, "0123456789ABCDEF") // 16 bytes
			fmt.Fprint(w, "GHIJ")             // pushes 4 more: oldest 4 dropped
			return "", nil
		},
	})
	if _, err := reg.Wait(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	// Stored tail: "456789ABCDEFGHIJ" (last 16 of the 20 written).
	out, offset, err := reg.Output(job.ID, 0)
	if err != nil || string(out) != "456789ABCDEFGHIJ" || offset != 20 {
		t.Fatalf("Output = %q, %d, %v; want tail + total 20", out, offset, err)
	}
	// A reader that already consumed the first 20 bytes sees nothing new.
	out2, offset2, err := reg.Output(job.ID, offset)
	if err != nil || len(out2) != 0 || offset2 != 20 {
		t.Fatalf("Output(20) = %q, %d, %v; want empty + offset 20", out2, offset2, err)
	}
}

// TestStart_ExplicitID pins the explicit-id contract (unique; ErrJobExists).
func TestStart_ExplicitID(t *testing.T) {
	reg := NewRegistry()
	spec := Spec{Kind: "task", ID: "t1", Run: func(ctx context.Context, w io.Writer) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}}
	if _, err := reg.Start(spec); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Start(spec); err != ErrJobExists {
		t.Fatalf("duplicate explicit id = %v, want ErrJobExists", err)
	}
	if _, err := reg.Status("t1"); err != nil {
		t.Fatalf("Status = %v", err)
	}
}

// TestList_OwnerFence: List filters by owner.
func TestList_OwnerFence(t *testing.T) {
	reg := NewRegistry()
	release := make(chan struct{})
	spec := Spec{
		Kind: "shell",
		Run: func(ctx context.Context, w io.Writer) (string, error) {
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "", nil
		},
	}
	spec.Owner = "s1"
	reg.Start(spec)
	spec.Owner = "s2"
	reg.Start(spec)

	if got := reg.List("s1"); len(got) != 1 || got[0].Owner != "s1" {
		t.Fatalf("List(s1) = %+v, want one s1 job", got)
	}
	if got := reg.List(""); len(got) != 2 {
		t.Fatalf("List(\"\") = %d jobs, want 2", len(got))
	}
	close(release)
}

// TestOnDone_ExactlyOncePerJob: the OnDone listener observes each settled
// job exactly once with the terminal snapshot.
func TestOnDone_ExactlyOncePerJob(t *testing.T) {
	reg := NewRegistry()
	var mu sync.Mutex
	var done []*Job
	unreg := reg.OnDone(func(j *Job) {
		mu.Lock()
		done = append(done, j)
		mu.Unlock()
	})
	defer unreg()

	release := make(chan struct{})
	j1, _ := reg.Start(Spec{Kind: "shell", Owner: "s1", Run: func(ctx context.Context, w io.Writer) (string, error) {
		<-release
		return "", nil
	}})
	j2, _ := reg.Start(Spec{Kind: "calc", Owner: "s2", FinalOutput: true, Run: func(ctx context.Context, w io.Writer) (string, error) {
		return "v", nil
	}})
	if _, err := reg.Wait(context.Background(), j2.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	if _, err := reg.Wait(context.Background(), j1.ID); err != nil {
		t.Fatal(err)
	}

	// Both settlements fan out before Wait returns (done closes last), so
	// the records are already delivered — assert directly, no polling.
	mu.Lock()
	if len(done) != 2 {
		mu.Unlock()
		t.Fatalf("done records = %d, want exactly 2", len(done))
	}
	byID := map[string]*Job{}
	for _, j := range done {
		byID[j.ID] = j
	}
	mu.Unlock()
	if byID[j1.ID] == nil || byID[j1.ID].Status != StatusCompleted || byID[j1.ID].Owner != "s1" {
		t.Fatalf("j1 record = %+v", byID[j1.ID])
	}
	if byID[j2.ID] == nil || byID[j2.ID].Status != StatusCompleted {
		t.Fatalf("j2 record = %+v", byID[j2.ID])
	}
}

// TestOnDone_Unregister: unregistering stops future deliveries.
func TestOnDone_Unregister(t *testing.T) {
	reg := NewRegistry()
	var mu sync.Mutex
	n := 0
	unreg := reg.OnDone(func(j *Job) {
		mu.Lock()
		n++
		mu.Unlock()
	})
	job, _ := reg.Start(Spec{Kind: "shell", Run: func(ctx context.Context, w io.Writer) (string, error) {
		return "", nil
	}})
	if _, err := reg.Wait(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	// Delivery is synchronous with settlement — Wait already saw it.
	unreg()
	job2, _ := reg.Start(Spec{Kind: "shell", Run: func(ctx context.Context, w io.Writer) (string, error) {
		return "", nil
	}})
	if _, err := reg.Wait(context.Background(), job2.ID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if n != 1 {
		t.Fatalf("deliveries after unregister = %d, want 1", n)
	}
}

// TestOnChanged_OwnerGranular: the change feed observes registration,
// stopping, settlement, and teardown removal at owner granularity.
func TestOnChanged_OwnerGranular(t *testing.T) {
	reg := NewRegistry()
	var mu sync.Mutex
	var owners []string
	unreg := reg.OnChanged(func(owner string) {
		mu.Lock()
		owners = append(owners, owner)
		mu.Unlock()
	})
	defer unreg()

	blocking := Spec{Kind: "shell", Owner: "s1", Run: func(ctx context.Context, w io.Writer) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}}
	j1, _ := reg.Start(blocking) // registration → s1
	if err := reg.Kill(j1.ID); err != nil {
		t.Fatal(err) // stopping → s1
	}
	if _, err := reg.Wait(context.Background(), j1.ID); err != nil {
		t.Fatal(err) // settlement → s1
	}
	reg.Teardown("s1") // removal → s1

	mu.Lock()
	if len(owners) != 4 {
		mu.Unlock()
		t.Fatalf("changed events = %v, want 4 s1 events", owners)
	}
	for _, o := range owners {
		if o != "s1" {
			mu.Unlock()
			t.Fatalf("changed event owner = %q, want s1", o)
		}
	}
	mu.Unlock()

	// An unowned job reports "".
	mu.Lock()
	owners = nil
	mu.Unlock()
	ju, _ := reg.Start(Spec{Kind: "shell", Run: func(ctx context.Context, w io.Writer) (string, error) {
		return "", nil
	}})
	if _, err := reg.Wait(context.Background(), ju.ID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(owners) != 2 || owners[0] != "" || owners[1] != "" {
		t.Fatalf("unowned changed events = %v, want two \"\" events", owners)
	}
}
