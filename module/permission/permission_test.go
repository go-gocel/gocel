package permission

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-gocel/gocel/core/host"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

type fakeRegistrar struct {
	toolCall  kernel.ToolCallHook
	decisions []*kernel.DecisionInfo
}

func (r *fakeRegistrar) OnAgentStart(fn kernel.AgentStartHook) func()       { return nil }
func (r *fakeRegistrar) OnAgentEnd(fn kernel.AgentEndHook) func()           { return nil }
func (r *fakeRegistrar) OnMessagesBuilt(fn kernel.MessagesHook) func()      { return nil }
func (r *fakeRegistrar) OnStepStart(fn kernel.StepHook) func()              { return nil }
func (r *fakeRegistrar) OnStepEnd(fn kernel.StepHook) func()                { return nil }
func (r *fakeRegistrar) OnModelCall(fn kernel.ModelCallHook) func()         { return nil }
func (r *fakeRegistrar) OnModelResult(fn kernel.ModelResultHookFunc) func() { return nil }
func (r *fakeRegistrar) OnToolCall(fn kernel.ToolCallHook) func() {
	r.toolCall = fn
	return func() {}
}
func (r *fakeRegistrar) OnToolResult(fn kernel.ToolResultHookFunc) func() { return nil }
func (r *fakeRegistrar) OnDecision(fn kernel.DecisionHook) func()         { return nil }

// FireDecision lets the module report decisions through the registrar.
func (r *fakeRegistrar) FireDecision(_ context.Context, info *kernel.DecisionInfo) error {
	r.decisions = append(r.decisions, info)
	return nil
}

func writeInfo(name, args string) *kernel.ToolCallInfo {
	return &kernel.ToolCallInfo{
		Name:    name,
		Args:    args,
		Effects: []kernel.ToolEffect{kernel.EffectWrite},
	}
}

type autoPolicy struct{}

func (autoPolicy) Decide(_ context.Context, _ *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) {
	return kernel.ApprovalAuto, nil
}

type denyPolicy struct{}

func (denyPolicy) Decide(_ context.Context, _ *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) {
	return kernel.ApprovalDeny, nil
}

type askPolicy struct{}

func (askPolicy) Decide(_ context.Context, _ *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) {
	return kernel.ApprovalAsk, nil
}

func nonTempOutside(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory for denial assertions")
	}
	return filepath.Join(home, "gocel-permission-outside-check.txt")
}

func TestPermission_AutoPolicyPassesInTier(t *testing.T) {
	root := t.TempDir()
	file := host.NewDefaultFilePolicy(root)
	m := New(file, autoPolicy{}, nil)
	r := &fakeRegistrar{}
	m.Register(r)

	_, _, err := r.toolCall(context.Background(), writeInfo("write", jsonMarshalArgs(t, map[string]string{"path": filepath.Join(root, "in.txt")})))
	if err != nil {
		t.Fatalf("in-tier write under auto policy = %v, want nil", err)
	}
	if len(r.decisions) != 0 {
		t.Fatalf("decisions = %+v, want none for a passing call", r.decisions)
	}
}

// TestPermission_DelegatedNeverDeniesAll: a child running under the pinned
// NeverApprovalPolicy has every call denied deterministically — the
// session tier, the host asker, and the live approval disposition never
// apply to a delegated call.
func TestPermission_DelegatedNeverDeniesAll(t *testing.T) {
	root := t.TempDir()
	// A permissive session policy that would otherwise allow the call.
	m := New(host.NewDefaultFilePolicy(root), autoPolicy{}, nil)
	r := &fakeRegistrar{}
	m.Register(r)

	// Even a read-effect call inside the workspace is denied: the pinned
	// policy is the ONLY disposition a delegated call has.
	ctx := kernel.WithDelegatedApproval(context.Background(), NeverApprovalPolicy{})
	info := &kernel.ToolCallInfo{
		Name:    "read",
		Args:    jsonMarshalArgs(t, map[string]string{"path": filepath.Join(root, "in.txt")}),
		Effects: []kernel.ToolEffect{kernel.EffectRead},
	}
	if _, _, err := r.toolCall(ctx, info); err == nil {
		t.Fatal("delegated call under NeverApprovalPolicy = nil, want denial")
	}
	if len(r.decisions) != 1 || r.decisions[0].Decision != "policy_deny" {
		t.Fatalf("decisions = %+v, want one policy_deny", r.decisions)
	}
}

// TestPermission_NoDelegationUsesSessionDisposition: without a pinned
// policy the module behaves exactly as before (session tier + approval).
func TestPermission_NoDelegationUsesSessionDisposition(t *testing.T) {
	root := t.TempDir()
	m := New(host.NewDefaultFilePolicy(root), autoPolicy{}, nil)
	r := &fakeRegistrar{}
	m.Register(r)

	_, _, err := r.toolCall(context.Background(), writeInfo("write", jsonMarshalArgs(t, map[string]string{"path": filepath.Join(root, "in.txt")})))
	if err != nil {
		t.Fatalf("non-delegated in-tier write = %v, want nil", err)
	}
}

func TestPermission_ReadOnlyTierEscalatesAndApprovePasses(t *testing.T) {
	file := host.NewDefaultFilePolicy(t.TempDir())
	file.SetMode(types.PermissionReadOnly)
	approved := make(chan struct{}, 1)
	asker := func(_ context.Context, req *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) {
		if req.Reason == "" || !strings.Contains(req.Reason, "read-only") {
			t.Errorf("escalation reason = %q, want tier denial explanation", req.Reason)
		}
		approved <- struct{}{}
		return kernel.ApprovalAllowedOnce, nil
	}
	m := New(file, askPolicy{}, asker)
	r := &fakeRegistrar{}
	m.Register(r)

	ctx := context.Background()
	_, info, err := r.toolCall(ctx, writeInfo("write", `{"path":"x.txt"}`))
	if err != nil {
		t.Fatalf("approved escalation = %v, want nil", err)
	}
	if info == nil {
		t.Fatal("allowed-once must pass the call through")
	}
	if len(r.decisions) != 1 || r.decisions[0].Decision != "approved" {
		t.Fatalf("decisions = %+v, want one approved", r.decisions)
	}
	<-approved
}

func TestPermission_ReadOnlyTierRejectBlocks(t *testing.T) {
	file := host.NewDefaultFilePolicy(t.TempDir())
	file.SetMode(types.PermissionReadOnly)
	asker := func(_ context.Context, _ *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) {
		return kernel.ApprovalDeny, nil
	}
	m := New(file, askPolicy{}, asker)
	r := &fakeRegistrar{}
	m.Register(r)

	_, _, err := r.toolCall(context.Background(), writeInfo("write", `{"path":"x.txt"}`))
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("rejected escalation = %v, want rejection error", err)
	}
	if len(r.decisions) != 1 || r.decisions[0].Decision != "rejected" {
		t.Fatalf("decisions = %+v, want one rejected", r.decisions)
	}
}

// TestPermission_DeniedWithFeedbackCarriesTheHostReason proves the human's
// denial reason reaches the tool error as a REJECTION (closed DeniedError
// vocabulary) — not misclassified as an approval-channel failure.
func TestPermission_DeniedWithFeedbackCarriesTheHostReason(t *testing.T) {
	file := host.NewDefaultFilePolicy(t.TempDir())
	file.SetMode(types.PermissionReadOnly)
	asker := func(_ context.Context, _ *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) {
		return kernel.ApprovalDeny, &kernel.DeniedError{Feedback: "hold off until the release freeze ends"}
	}
	m := New(file, askPolicy{}, asker)
	r := &fakeRegistrar{}
	m.Register(r)

	_, _, err := r.toolCall(context.Background(), writeInfo("write", `{"path":"x.txt"}`))
	if err == nil {
		t.Fatal("deny-with-feedback must reject")
	}
	if !strings.Contains(err.Error(), "rejected by the host") {
		t.Fatalf("error = %v, want a rejection (not a channel failure)", err)
	}
	if !strings.Contains(err.Error(), "hold off until the release freeze ends") {
		t.Fatalf("error = %v, want the host's reason", err)
	}
	if len(r.decisions) != 1 || r.decisions[0].Decision != "rejected" || r.decisions[0].Reason != "hold off until the release freeze ends" {
		t.Fatalf("decisions = %+v, want one rejected carrying the feedback", r.decisions)
	}
}

func TestPermission_NoAskerFailsClosed(t *testing.T) {
	file := host.NewDefaultFilePolicy(t.TempDir())
	file.SetMode(types.PermissionReadOnly)
	m := New(file, askPolicy{}, nil) // ask mode, nil asker
	r := &fakeRegistrar{}
	m.Register(r)

	_, _, err := r.toolCall(context.Background(), writeInfo("write", `{"path":"x.txt"}`))
	if err == nil || !strings.Contains(err.Error(), "no answerer") {
		t.Fatalf("nil asker = %v, want fail-closed denial", err)
	}
}

func TestPermission_AskerErrorFailsClosed(t *testing.T) {
	file := host.NewDefaultFilePolicy(t.TempDir())
	file.SetMode(types.PermissionReadOnly)
	asker := func(_ context.Context, _ *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) {
		return kernel.ApprovalAuto, errors.New("channel down")
	}
	m := New(file, askPolicy{}, asker)
	r := &fakeRegistrar{}
	m.Register(r)

	_, _, err := r.toolCall(context.Background(), writeInfo("write", `{"path":"x.txt"}`))
	if err == nil {
		t.Fatal("asker error = nil, want fail-closed denial")
	}
	if len(r.decisions) != 1 || r.decisions[0].Decision != "rejected" {
		t.Fatalf("decisions = %+v, want one rejected", r.decisions)
	}
}

func TestPermission_AutoModeBlocksOutsidePathsHard(t *testing.T) {
	root := t.TempDir()
	file := host.NewDefaultFilePolicy(root)
	outside := nonTempOutside(t)

	m := New(file, autoPolicy{}, nil) // auto mode: no asks, hard blocks
	r := &fakeRegistrar{}
	m.Register(r)

	inside := jsonMarshalArgs(t, map[string]string{"path": filepath.Join(root, "in.txt")})
	outsideArgs := jsonMarshalArgs(t, map[string]string{"path": outside})

	// Inside the root: straight through.
	if _, _, err := r.toolCall(context.Background(), writeInfo("write", inside)); err != nil {
		t.Fatalf("in-root write = %v, want nil", err)
	}
	// Outside the root: hard policy denial without any asker.
	if _, _, err := r.toolCall(context.Background(), writeInfo("write", outsideArgs)); err == nil {
		t.Fatal("out-of-root write under auto mode = nil, want hard denial")
	}
	if len(r.decisions) != 1 || r.decisions[0].Decision != "policy_deny" {
		t.Fatalf("decisions = %+v, want one policy_deny", r.decisions)
	}
}

func TestPermission_AskModeAsksEveryWrite(t *testing.T) {
	root := t.TempDir()
	file := host.NewDefaultFilePolicy(root)

	asked := 0
	asker := func(_ context.Context, _ *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) {
		asked++
		return kernel.ApprovalAllowedOnce, nil
	}
	m := New(file, askPolicy{}, asker)
	r := &fakeRegistrar{}
	m.Register(r)

	// Ask mode asks for every write-effect call, in-root included.
	inside := jsonMarshalArgs(t, map[string]string{"path": filepath.Join(root, "in.txt")})
	if _, _, err := r.toolCall(context.Background(), writeInfo("write", inside)); err != nil {
		t.Fatalf("approved in-root write = %v, want nil", err)
	}
	if asked != 1 {
		t.Fatalf("in-root write asked %d times, want 1 (ask mode asks every write)", asked)
	}
}

// jsonMarshalArgs builds a valid JSON argument string (backslashes on
// Windows must be JSON-escaped — hand-built strings would not parse).
func jsonMarshalArgs(t *testing.T, m map[string]string) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPermission_DenyPolicyBlocksEvenInTier(t *testing.T) {
	root := t.TempDir()
	file := host.NewDefaultFilePolicy(root)
	m := New(file, denyPolicy{}, nil)
	r := &fakeRegistrar{}
	m.Register(r)

	// An in-root write passes containment and reaches the approval policy,
	// whose deny disposition blocks it.
	args := jsonMarshalArgs(t, map[string]string{"path": filepath.Join(root, "in.txt")})
	_, _, err := r.toolCall(context.Background(), writeInfo("write", args))
	if err == nil || !strings.Contains(err.Error(), "approval policy") {
		t.Fatalf("deny policy = %v, want policy denial", err)
	}
	if len(r.decisions) != 1 || r.decisions[0].Decision != "policy_deny" {
		t.Fatalf("decisions = %+v, want one policy_deny", r.decisions)
	}
}

func TestPermission_ReadOnlyEffectsSkipTierChecks(t *testing.T) {
	file := host.NewDefaultFilePolicy(t.TempDir())
	file.SetMode(types.PermissionReadOnly)
	m := New(file, autoPolicy{}, nil)
	r := &fakeRegistrar{}
	m.Register(r)

	// A read-only tool call passes the tier and the auto policy, even in
	// read-only mode — no write effect, no containment check, no ask.
	ctx := context.Background()
	_, _, err := r.toolCall(ctx, &kernel.ToolCallInfo{
		Name:    "read",
		Args:    `{"path":"anything.txt"}`,
		Effects: []kernel.ToolEffect{kernel.EffectRead},
	})
	if err != nil {
		t.Fatalf("read tool under read-only tier = %v, want nil", err)
	}
}

// Regression (verified defect): the delegated-approval branch used to
// return BEFORE the FilePolicy containment check — a pinned auto policy
// widened the tier to unlimited writes. Containment must apply to
// delegated calls too.
func TestPermission_DelegatedAutoStillContained(t *testing.T) {
	root := t.TempDir()
	m := New(host.NewDefaultFilePolicy(root), autoPolicy{}, nil)
	r := &fakeRegistrar{}
	m.Register(r)

	ctx := kernel.WithDelegatedApproval(context.Background(), autoPolicy{})
	outside := nonTempOutside(t)

	// In-root write: containment passes, the pinned auto policy allows.
	inArgs := jsonMarshalArgs(t, map[string]string{"path": filepath.Join(root, "in.txt")})
	if _, _, err := r.toolCall(ctx, writeInfo("write", inArgs)); err != nil {
		t.Fatalf("delegated in-root write = %v, want nil", err)
	}
	// Out-of-root write: containment must block even under an auto policy.
	outArgs := jsonMarshalArgs(t, map[string]string{"path": outside})
	if _, _, err := r.toolCall(ctx, writeInfo("write", outArgs)); err == nil {
		t.Fatal("delegated out-of-root write = nil, want containment denial")
	}
}

// Regression (verified defect): the default path extractor returned nil on
// unparseable arguments — containment silently skipped (fail-open).
// Unparseable write arguments must deny.
func TestPermission_UnparseableArgsFailClosed(t *testing.T) {
	file := host.NewDefaultFilePolicy(t.TempDir())
	m := New(file, autoPolicy{}, nil)
	r := &fakeRegistrar{}
	m.Register(r)

	_, _, err := r.toolCall(context.Background(), writeInfo("write", `{"path": "C:\Users\broken"}`))
	if err == nil {
		t.Fatal("unparseable write args = nil, want fail-closed denial")
	}
	if len(r.decisions) != 1 || r.decisions[0].Decision != "policy_deny" {
		t.Fatalf("decisions = %+v, want one policy_deny", r.decisions)
	}
}

func TestPermission_NilPolicyFailsClosed(t *testing.T) {
	file := host.NewDefaultFilePolicy(t.TempDir())
	file.SetMode(types.PermissionReadOnly)
	m := New(file, nil, nil)
	r := &fakeRegistrar{}
	m.Register(r)

	_, _, err := r.toolCall(context.Background(), writeInfo("write", `{"path":"x.txt"}`))
	if err == nil {
		t.Fatal("nil approval policy = nil, want fail-closed denial")
	}
}

func TestPermission_EmptyEffectsFailClosed(t *testing.T) {
	file := host.NewDefaultFilePolicy(t.TempDir())
	file.SetMode(types.PermissionReadOnly)
	m := New(file, autoPolicy{}, nil)
	r := &fakeRegistrar{}
	m.Register(r)

	// An engine bypassing the runtime enrichment yields empty effects; the
	// module must treat that as write+exec (fail-closed), not as "no risk".
	_, _, err := r.toolCall(context.Background(), &kernel.ToolCallInfo{
		Name: "mystery",
		Args: `{"path":"x.txt"}`,
	})
	if err == nil {
		t.Fatal("empty effects = nil, want fail-closed denial")
	}
}
