package types

import (
	"testing"
	"time"
)

func TestPermissionModeStrings(t *testing.T) {
	cases := map[PermissionMode]string{
		PermissionReadOnly:         "read-only",
		PermissionWorkspaceWrite:   "workspace-write",
		PermissionDangerFullAccess: "danger-full-access",
		PermissionMode(99):         "unknown",
	}
	for mode, want := range cases {
		if got := mode.String(); got != want {
			t.Fatalf("PermissionMode(%d).String() = %q, want %q", mode, got, want)
		}
	}
}

func TestFileOpStrings(t *testing.T) {
	cases := map[FileOp]string{
		FileOpRead:  "read",
		FileOpWrite: "write",
		FileOpExec:  "exec",
		FileOp(99):  "unknown",
	}
	for op, want := range cases {
		if got := op.String(); got != want {
			t.Fatalf("FileOp(%d).String() = %q, want %q", op, got, want)
		}
	}
}

func TestSessionModeStrings(t *testing.T) {
	cases := map[SessionMode]string{
		SessionModeNormal: "normal",
		SessionModePlan:   "plan",
		SessionMode(99):   "unknown",
	}
	for mode, want := range cases {
		if got := mode.String(); got != want {
			t.Fatalf("SessionMode(%d).String() = %q, want %q", mode, got, want)
		}
	}
}

func TestPermissionMode_AtLeastLadder(t *testing.T) {
	// Strict escalation ladder: read-only < workspace-write < danger-full-access.
	if !PermissionWorkspaceWrite.AtLeast(PermissionReadOnly) {
		t.Fatal("workspace-write must be at least read-only")
	}
	if !PermissionDangerFullAccess.AtLeast(PermissionWorkspaceWrite) {
		t.Fatal("danger-full-access must be at least workspace-write")
	}
	if PermissionReadOnly.AtLeast(PermissionWorkspaceWrite) {
		t.Fatal("read-only must not be at least workspace-write")
	}
	if !PermissionReadOnly.AtLeast(PermissionReadOnly) {
		t.Fatal("a mode must be at least itself")
	}
}

func TestNoticeEvent(t *testing.T) {
	n := &Notice{Kind: NoticeKindJob, ID: "shell-3", Status: "completed", Label: "go test", At: time.Now()}
	ev := NoticeEvent(n)
	if ev.Type != EventNotice {
		t.Fatalf("NoticeEvent type = %v, want EventNotice", ev.Type)
	}
	if ev.Notice != n {
		t.Fatal("NoticeEvent must carry the notice payload")
	}
	if ev.Notice.At.IsZero() {
		t.Fatal("Notice.At must be settable for ordering/dedup")
	}
	if ev.Type.String() != "notice" {
		t.Fatalf("EventNotice.String() = %q, want notice", ev.Type.String())
	}
}

func TestRuntimeFacts_ZeroValuesFailClosed(t *testing.T) {
	var f RuntimeFacts
	// The zero permission mode is the most restrictive tier (fail-closed),
	// and the zero session mode is normal.
	if f.Permission != PermissionReadOnly {
		t.Fatalf("zero PermissionMode = %v, want read-only", f.Permission)
	}
	if f.Mode != SessionModeNormal {
		t.Fatalf("zero SessionMode = %v, want normal", f.Mode)
	}
}
