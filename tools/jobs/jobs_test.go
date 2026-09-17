package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/jobs"
	"github.com/go-gocel/gocel/core/kernel"
)

func toolsFor(reg *jobs.Registry) []kernel.Tool {
	ts, err := Tools(Config{Registry: reg})
	if err != nil {
		panic(err)
	}
	return ts
}

func TestJobTools_ListOutputKill(t *testing.T) {
	reg := jobs.NewRegistry()
	release := make(chan struct{})
	// written signals that the first output chunk has been flushed — the
	// read below waits on it, so the test is event-driven rather than a
	// timing-dependent poll of the job goroutine.
	written := make(chan struct{})
	job, err := reg.Start(jobs.Spec{
		Kind:  "shell",
		Label: "build",
		Owner: "s1",
		Run: func(ctx context.Context, w io.Writer) (string, error) {
			fmt.Fprint(w, "building...\n")
			close(written)
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := toolsFor(reg)
	list, output, kill := ts[0], ts[1], ts[2]

	// job_list shows the live job with its owner.
	out, err := list.Run(context.Background(), `{"owner":"s1"}`)
	if err != nil || !strings.Contains(out, job.ID) || !strings.Contains(out, "build") {
		t.Fatalf("job_list = %q, %v; want the job", out, err)
	}

	// job_output reads incrementally through the cursor. The first write is
	// asynchronous — wait for the job's flush signal instead of racing the
	// job goroutine, then a single read is deterministic.
	select {
	case <-written:
	case <-time.After(5 * time.Second):
		t.Fatal("job never flushed its first output")
	}
	var first struct {
		Offset int    `json:"offset"`
		Output string `json:"output"`
		Status string `json:"status"`
	}
	out, err = output.Run(context.Background(), `{"id":"`+job.ID+`"}`)
	if err != nil {
		t.Fatalf("job_output = %v", err)
	}
	if err := json.Unmarshal([]byte(out), &first); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if !strings.Contains(first.Output, "building...") || first.Status != string(jobs.StatusRunning) {
		t.Fatalf("first read = %+v, want building + running", first)
	}

	// job_kill terminates.
	out, err = kill.Run(context.Background(), `{"id":"`+job.ID+`"}`)
	if err != nil {
		t.Fatalf("job_kill = %v", err)
	}
	settled, err := reg.Wait(context.Background(), job.ID)
	if err != nil || settled.Status != jobs.StatusKilled {
		t.Fatalf("Wait = %+v, %v; want killed", settled, err)
	}
}

// TestJobTools_WaitBlocksUntilSettled: job_wait returns the settled
// snapshot once the job completes.
func TestJobTools_WaitBlocksUntilSettled(t *testing.T) {
	reg := jobs.NewRegistry()
	job, err := reg.Start(jobs.Spec{
		Kind: "shell",
		Run: func(ctx context.Context, w io.Writer) (string, error) {
			return "the answer", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := toolsFor(reg)
	wait := ts[3]
	out, err := wait.Run(context.Background(), `{"id":"`+job.ID+`"}`)
	if err != nil {
		t.Fatalf("job_wait = %v", err)
	}
	var res struct {
		Status    string `json:"status"`
		TimedOut  bool   `json:"timed_out"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("wait output %q: %v", out, err)
	}
	if res.TimedOut || res.Status != string(jobs.StatusCompleted) {
		t.Fatalf("job_wait = %+v, want completed, not timed out", res)
	}
}

// TestJobTools_WaitTimeoutReturnsLiveSnapshot: a bounded wait that expires
// reports the live snapshot with timed_out=true instead of failing.
func TestJobTools_WaitTimeoutReturnsLiveSnapshot(t *testing.T) {
	reg := jobs.NewRegistry()
	release := make(chan struct{})
	job, err := reg.Start(jobs.Spec{
		Kind: "shell",
		Run: func(ctx context.Context, w io.Writer) (string, error) {
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := toolsFor(reg)
	wait := ts[3]
	out, err := wait.Run(context.Background(), `{"id":"`+job.ID+`","timeout_ms":50}`)
	if err != nil {
		t.Fatalf("job_wait = %v", err)
	}
	var res struct {
		Status   string `json:"status"`
		TimedOut bool   `json:"timed_out"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("wait output %q: %v", out, err)
	}
	if !res.TimedOut || res.Status != string(jobs.StatusRunning) {
		t.Fatalf("job_wait(timeout) = %+v, want running + timed_out", res)
	}
	close(release)
	if _, err := reg.Wait(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
}
