package schedule

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	coresession "github.com/go-gocel/gocel/core/session"
	"github.com/go-gocel/gocel/core/kernel"
)

// newModule builds a schedule module over a fresh log with a recording
// deliver callback.
func newModule(t *testing.T) (*Module, *sync.Mutex, map[string][]Reminder) {
	t.Helper()
	log := coresession.NewLog("s1")
	var mu sync.Mutex
	delivered := make(map[string][]Reminder)
	m, err := New("s1", Config{
		Log: log,
		Deliver: func(_ context.Context, sessionID string, r Reminder) {
			mu.Lock()
			delivered[sessionID] = append(delivered[sessionID], r)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, &mu, delivered
}

func toolsOf(t *testing.T, m *Module) map[string]kernel.Tool {
	t.Helper()
	ts, err := m.Tools()
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]kernel.Tool, len(ts))
	for _, tl := range ts {
		out[tl.Name()] = tl
	}
	return out
}

// TestConcurrentCreate_NoLostUpdates: whole-value replacement paths
// (create / delete / fireDue) are serialized — concurrent creates must not
// overwrite each other (fold→modify→append is atomic under the state lock).
func TestConcurrentCreate_NoLostUpdates(t *testing.T) {
	m, _, _ := newModule(t)
	tools := toolsOf(t, m)

	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := tools["schedule_create"].Run(context.Background(),
				`{"prompt":"reminder `+string(rune('a'+i%26))+`","after_seconds":3600}`)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent create: %v", err)
		}
	}

	out, err := tools["schedule_list"].Run(context.Background(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var rs []Reminder
	if err := json.Unmarshal([]byte(out), &rs); err != nil {
		t.Fatalf("list %q: %v", out, err)
	}
	if len(rs) != n {
		t.Fatalf("list = %d reminders, want %d (lost updates)", len(rs), n)
	}
}

// TestList_EmptyIsArray: an empty schedule serializes as [] not null.
func TestList_EmptyIsArray(t *testing.T) {
	m, _, _ := newModule(t)
	tools := toolsOf(t, m)
	out, err := tools["schedule_list"].Run(context.Background(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `[]`) {
		t.Fatalf("empty list = %q, want []", out)
	}
}

// TestCreate_AfterAndList: after_seconds creates a durable reminder visible
// in the list.
func TestCreate_AfterAndList(t *testing.T) {
	m, _, _ := newModule(t)
	tools := toolsOf(t, m)

	out, err := tools["schedule_create"].Run(context.Background(), `{"prompt":"check build","after_seconds":60}`)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(out) < 10 {
		t.Fatalf("create output = %q, want an id", out)
	}
	list, err := tools["schedule_list"].Run(context.Background(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < 5 || !contains(list, "check build") {
		t.Fatalf("list = %q, want the reminder", list)
	}
}

// TestCreate_Validation: exactly one selector; at must be future RFC3339.
func TestCreate_Validation(t *testing.T) {
	m, _, _ := newModule(t)
	tools := toolsOf(t, m)

	if _, err := tools["schedule_create"].Run(context.Background(), `{"prompt":"x"}`); err == nil {
		t.Fatal("no selector must fail")
	}
	if _, err := tools["schedule_create"].Run(context.Background(), `{"prompt":"x","after_seconds":1,"at":"2030-01-01T00:00:00Z"}`); err == nil {
		t.Fatal("two selectors must fail")
	}
	if _, err := tools["schedule_create"].Run(context.Background(), `{"prompt":"x","at":"2020-01-01T00:00:00Z"}`); err == nil {
		t.Fatal("past at must fail")
	}
	if _, err := tools["schedule_create"].Run(context.Background(), `{"prompt":"x","every_seconds":30}`); err == nil {
		t.Fatal("every below 300s must fail")
	}
	if _, err := tools["schedule_create"].Run(context.Background(), `{"after_seconds":1}`); err == nil {
		t.Fatal("missing prompt must fail")
	}
}

// TestScheduler_FiresAfterAndPersists: the scheduler delivers the reminder
// when it comes due and marks it done in the log — a restart (new module
// over the same log) does not re-deliver it.
func TestScheduler_FiresAfterAndPersists(t *testing.T) {
	log := coresession.NewLog("s1")
	var mu sync.Mutex
	var fired []Reminder
	m, err := New("s1", Config{
		Log: log,
		Deliver: func(_ context.Context, _ string, r Reminder) {
			mu.Lock()
			fired = append(fired, r)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := toolsOf(t, m)
	// A 1-second after reminder.
	if _, err := tools["schedule_create"].Run(context.Background(), `{"prompt":"soon","after_seconds":1}`); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(fired)
		mu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			m.Stop()
			t.Fatal("reminder never fired")
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	m.Stop()

	mu.Lock()
	if fired[0].Prompt != "soon" {
		t.Fatalf("fired = %+v, want the soon reminder", fired[0])
	}
	mu.Unlock()

	// Restart over the same log: the fired reminder is done and does not
	// re-deliver. A sentinel reminder created now (due in 1s) proves the
	// scheduler actually ticked during the observation window — the
	// assertion is event-driven, not a fixed sleep.
	var mu2 sync.Mutex
	var fired2 []string
	m2, err := New("s1", Config{
		Log: log,
		Deliver: func(_ context.Context, _ string, r Reminder) {
			mu2.Lock()
			fired2 = append(fired2, r.Prompt)
			mu2.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools2 := toolsOf(t, m2)
	if _, err := tools2["schedule_create"].Run(context.Background(), `{"prompt":"sentinel","after_seconds":1}`); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	m2.Start(ctx2)
	deadline2 := time.Now().Add(5 * time.Second)
	for {
		mu2.Lock()
		n := len(fired2)
		mu2.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline2) {
			cancel2()
			m2.Stop()
			t.Fatal("sentinel never fired")
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel2()
	m2.Stop()
	mu2.Lock()
	defer mu2.Unlock()
	if len(fired2) != 1 || fired2[0] != "sentinel" {
		t.Fatalf("restart deliveries = %v, want only the sentinel (no re-delivery of the fired reminder)", fired2)
	}
}

// TestScheduler_EveryRepeats: every-reminders fire repeatedly on their
// cadence.
func TestScheduler_EveryRepeats(t *testing.T) {
	log := coresession.NewLog("s1")
	var mu sync.Mutex
	var count int
	m, err := New("s1", Config{
		Log: log,
		Deliver: func(_ context.Context, _ string, _ Reminder) {
			mu.Lock()
			count++
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := toolsOf(t, m)
	// every=300s, but backdate the anchor so the first fire is immediate
	// and the cadence re-arms to 300s (which we do not wait for).
	if _, err := tools["schedule_create"].Run(context.Background(), `{"prompt":"tick","every_seconds":300}`); err != nil {
		t.Fatal(err)
	}
	// Force the anchor into the past so the first tick fires now.
	rs := m.fold()
	rs[0].At = time.Now().UTC().Add(-time.Second)
	_ = m.replaceAll(rs)

	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := count
		mu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			m.Stop()
			t.Fatal("every reminder never fired")
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	m.Stop()
	// The reminder is NOT done (every re-arms).
	for _, r := range m.fold() {
		if r.Done {
			t.Fatal("every reminder must not be done after firing")
		}
	}
}

// TestDelete_RemovesReminder: deleting a reminder removes it from the log.
func TestDelete_RemovesReminder(t *testing.T) {
	m, _, _ := newModule(t)
	tools := toolsOf(t, m)
	if _, err := tools["schedule_create"].Run(context.Background(), `{"prompt":"x","after_seconds":60}`); err != nil {
		t.Fatal(err)
	}
	rs := m.fold()
	if len(rs) != 1 {
		t.Fatalf("reminders = %d, want 1", len(rs))
	}
	out, err := tools["schedule_delete"].Run(context.Background(), `{"id":"`+rs[0].ID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, "true") {
		t.Fatalf("delete = %q, want deleted:true", out)
	}
	if len(m.fold()) != 0 {
		t.Fatal("reminder must be gone after delete")
	}
	// Unknown id is a no-op.
	out, err = tools["schedule_delete"].Run(context.Background(), `{"id":"ghost"}`)
	if err != nil || !contains(out, "false") {
		t.Fatalf("delete unknown = %q, %v; want deleted:false", out, err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
