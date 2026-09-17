package shell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireTaskSh(t *testing.T) {
	requireSh(t)
}

func TestTaskRunnerLifecycle(t *testing.T) {
	requireTaskSh(t)
	r := NewTaskRunner("tasks")
	ctx := WithWorkDir(context.Background(), t.TempDir())

	got, err := r.start(ctx, "echo hello-task; sleep 2")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !strings.Contains(got, "t1") {
		t.Fatalf("start must return a task ID: %q", got)
	}

	st, err := r.status("t1")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(st, "running") {
		t.Errorf("freshly started task must be running: %q", st)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err = r.status("t1")
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if strings.Contains(st, "done") || strings.Contains(st, "failed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not finish in time: %q", st)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(st, "hello-task") {
		t.Errorf("task output must be visible: %q", st)
	}
	// File log persists for the audit trail.
	if _, err := os.Stat(filepath.Join(workDir(ctx), "tasks", "t1.log")); err != nil {
		t.Errorf("task log must be on disk: %v", err)
	}
}

func TestTaskRunnerStop(t *testing.T) {
	requireTaskSh(t)
	r := NewTaskRunner("")
	ctx := WithWorkDir(context.Background(), t.TempDir())

	if _, err := r.start(ctx, "sleep 30"); err != nil {
		t.Fatalf("start: %v", err)
	}
	got, err := r.stop("t1")
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !strings.Contains(got, "stopped") {
		t.Errorf("stop result: %q", got)
	}
	st, _ := r.status("t1")
	if !strings.Contains(st, "stopped") {
		t.Errorf("state after stop must be stopped: %q", st)
	}
}

func TestTaskRunnerUnknownID(t *testing.T) {
	r := NewTaskRunner("")
	if _, err := r.status("nope"); err == nil {
		t.Error("status on unknown ID must error")
	}
	if _, err := r.stop("nope"); err == nil {
		t.Error("stop on unknown ID must error")
	}
}

func TestTaskRunnerNoLogDirSkipsFile(t *testing.T) {
	requireTaskSh(t)
	r := NewTaskRunner("")
	ctx := WithWorkDir(context.Background(), t.TempDir())
	if _, err := r.start(ctx, "echo hi"); err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, _ := r.status("t1")
		if strings.Contains(st, "done") || strings.Contains(st, "failed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task did not finish")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(workDir(ctx), "t1.log")); err == nil {
		t.Error("empty LogDir must not write a log file")
	}
}
