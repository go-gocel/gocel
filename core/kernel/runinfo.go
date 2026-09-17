package kernel

import "github.com/go-gocel/gocel/core/types"

// ❄️ FROZEN — Stable type. Fields and semantics must not change.
//
// RunInfo carries the result of a single Runner.Run call.
//
// RunInfo 携带一次 Runner.Run 调用的结果。
type RunInfo struct {
	// InvocationID is the unique invocation identifier of this run, set by
	// the Runner; audit and observability use it to correlate all records.
	InvocationID string
	// AgentName is the name of the agent that ran.
	AgentName string
	// Input is the original AgentInput passed to Run.
	Input *types.AgentInput
	// AllMsgs contains all messages accumulated during the run.
	AllMsgs []*types.Message
	// Result is the final Result from the Agent.Run (nil if errored).
	Result *Result
	// Err is the run-level error (nil on success).
	Err error
	// CheckpointID is the checkpoint identifier if checkpointing was enabled.
	CheckpointID string
}
