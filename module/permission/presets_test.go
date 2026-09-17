package permission

import (
	"context"
	"testing"

	"github.com/go-gocel/gocel/core/host"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// TestFindPreset: the shipped table resolves by name; unknown names are
// reported absent.
func TestFindPreset(t *testing.T) {
	p, ok := FindPreset(DefaultPresets, "workspace-write")
	if !ok || p.Mode != types.PermissionWorkspaceWrite || p.Approval != "ask" {
		t.Fatalf("workspace-write preset = %+v", p)
	}
	p, ok = FindPreset(DefaultPresets, "danger-full-access")
	if !ok || p.Mode != types.PermissionDangerFullAccess || p.Approval != "never" {
		t.Fatalf("danger-full-access preset = %+v", p)
	}
	if _, ok := FindPreset(DefaultPresets, "ghost"); ok {
		t.Fatal("unknown preset must be absent")
	}
}

// TestPreset_ApplyWiresTierAndApproval: applying a preset sets the policy
// tier and returns the matching approval policy.
func TestPreset_ApplyWiresTierAndApproval(t *testing.T) {
	file := host.NewDefaultFilePolicy(t.TempDir())

	// workspace-write + ask: tier is set, ask disposition asks.
	ap, found := FindPreset(DefaultPresets, "workspace-write")
	if !found {
		t.Fatal("workspace-write preset missing")
	}
	askPolicy, applyErr := ap.Apply(file)
	if applyErr != nil {
		t.Fatal(applyErr)
	}
	if file.Mode() != types.PermissionWorkspaceWrite {
		t.Fatalf("mode = %v, want workspace-write", file.Mode())
	}
	if dec, _ := askPolicy.Decide(context.Background(), &kernel.ApprovalRequest{}); dec != kernel.ApprovalAsk {
		t.Fatalf("approval = %v, want ask", dec)
	}

	// danger-full-access + never: tier is set, disposition passes without
	// asking ("never" = never prompt the host; the old deny-everything
	// mapping made the flagship CI preset reject every call).
	dp, found := FindPreset(DefaultPresets, "danger-full-access")
	if !found {
		t.Fatal("danger-full-access preset missing")
	}
	neverPolicy, applyErr := dp.Apply(file)
	if applyErr != nil {
		t.Fatal(applyErr)
	}
	if file.Mode() != types.PermissionDangerFullAccess {
		t.Fatalf("mode = %v, want danger-full-access", file.Mode())
	}
	if dec, _ := neverPolicy.Decide(context.Background(), &kernel.ApprovalRequest{}); dec != kernel.ApprovalAuto {
		t.Fatalf("approval = %v, want auto (never prompts, in-tier calls pass)", dec)
	}
}

// TestPreset_ApplyFailsClosed: a nil policy or an unsettable policy fails
// loudly; an unknown approval disposition is rejected.
func TestPreset_ApplyFailsClosed(t *testing.T) {
	p, _ := FindPreset(DefaultPresets, "workspace-write")
	if _, err := p.Apply(nil); err == nil {
		t.Fatal("nil policy must fail")
	}
	if _, err := p.Apply(host.NewDefaultFilePolicy(t.TempDir())); err != nil {
		t.Fatalf("settable policy = %v, want nil", err)
	}
	bad := Preset{Name: "bad", Mode: types.PermissionWorkspaceWrite, Approval: "bogus"}
	if _, err := bad.Apply(host.NewDefaultFilePolicy(t.TempDir())); err == nil {
		t.Fatal("unknown approval disposition must fail")
	}
}

// TestPreset_ApplyNoPartialApplication: an invalid preset must fail WITHOUT
// touching the file policy — the mode is applied only after the approval
// disposition has been validated (fail-closed, no half-applied preset).
func TestPreset_ApplyNoPartialApplication(t *testing.T) {
	file := host.NewDefaultFilePolicy(t.TempDir())
	before := file.Mode()

	bad := Preset{Name: "bad", Mode: types.PermissionDangerFullAccess, Approval: "bogus"}
	if _, err := bad.Apply(file); err == nil {
		t.Fatal("unknown approval disposition must fail")
	}
	if file.Mode() != before {
		t.Fatalf("mode changed to %v on failed apply, want %v (no partial application)", file.Mode(), before)
	}
}
