package kernel

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/go-gocel/gocel/core/types"
)

type stubTool struct{ effects []ToolEffect }

func (s stubTool) Name() string           { return "stub" }
func (s stubTool) Description() string    { return "stub" }
func (s stubTool) Schema() map[string]any { return nil }
func (s stubTool) Run(context.Context, string) (string, error) {
	return "", nil
}
func (s stubTool) ToolMeta() ToolMeta { return ToolMeta{Effects: s.effects} }

func TestEffectiveEffects_Declared(t *testing.T) {
	got := EffectiveEffects(stubTool{effects: []ToolEffect{EffectRead, EffectNetwork}})
	if len(got) != 2 || got[0] != EffectRead || got[1] != EffectNetwork {
		t.Fatalf("EffectiveEffects = %v, want [EffectRead EffectNetwork]", got)
	}
}

func TestEffectiveEffects_UndeclaredFallsBackConservatively(t *testing.T) {
	got := EffectiveEffects(stubTool{})
	if len(got) != 2 || got[0] != EffectWrite || got[1] != EffectExec {
		t.Fatalf("undeclared EffectiveEffects = %v, want [EffectWrite EffectExec]", got)
	}
}

func TestToolEffect_BitValuesAreDistinct(t *testing.T) {
	seen := map[ToolEffect]bool{}
	for _, e := range []ToolEffect{EffectRead, EffectWrite, EffectExec, EffectNetwork, EffectUserData} {
		if seen[e] {
			t.Fatalf("duplicate bit value %d", e)
		}
		seen[e] = true
	}
}

func TestApprovalDecisionStrings(t *testing.T) {
	cases := map[ApprovalDecision]string{
		ApprovalAuto:         "auto",
		ApprovalAsk:          "ask",
		ApprovalAllowedOnce:  "allowed-once",
		ApprovalDeny:         "deny",
		ApprovalDecision(99): "unknown",
	}
	for d, want := range cases {
		if got := d.String(); got != want {
			t.Fatalf("ApprovalDecision(%d).String() = %q, want %q", d, got, want)
		}
	}
}

func TestDefaultApprovalFor_PresetMapping(t *testing.T) {
	cases := map[types.PermissionMode]ApprovalDecision{
		types.PermissionReadOnly:         ApprovalAsk,
		types.PermissionWorkspaceWrite:   ApprovalAsk,
		types.PermissionDangerFullAccess: ApprovalAuto, // never asks
		types.PermissionMode(99):         ApprovalDeny, // fail-closed
	}
	for mode, want := range cases {
		if got := DefaultApprovalFor(mode); got != want {
			t.Fatalf("DefaultApprovalFor(%v) = %v, want %v", mode, got, want)
		}
	}
}

func TestPolicyDenial_Classification(t *testing.T) {
	denial := &PolicyDenial{Op: types.FileOpWrite, Path: "/x", Mode: types.PermissionReadOnly, Reason: "tier"}
	if !IsPolicyDenial(denial) {
		t.Fatal("IsPolicyDenial(*PolicyDenial) = false")
	}
	if !IsPolicyDenial(fmt.Errorf("wrapped: %w", denial)) {
		t.Fatal("IsPolicyDenial(wrapped) = false")
	}
	if IsPolicyDenial(errors.New("other")) {
		t.Fatal("IsPolicyDenial(unrelated) = true")
	}
	if denial.Error() == "" {
		t.Fatal("PolicyDenial.Error() must render op/path/mode/reason")
	}
}

type denyPolicy struct{}

func (denyPolicy) Decide(_ context.Context, _ *ApprovalRequest) (ApprovalDecision, error) {
	return ApprovalDeny, errors.New("denied by test policy")
}

func TestApprovalPolicy_IsAConsumerSideContract(t *testing.T) {
	// The contract only needs what a permission module consumes: a decision
	// plus an error. Custom policies must be freely implementable.
	var p ApprovalPolicy = denyPolicy{}
	dec, err := p.Decide(context.Background(), &ApprovalRequest{ToolName: "write", Effects: []ToolEffect{EffectWrite}})
	if dec != ApprovalDeny || err == nil {
		t.Fatalf("Decide = %v, %v; want deny, error", dec, err)
	}
}
