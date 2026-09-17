package orchestrate

import (
	"context"
	"sync"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// SpawnResult carries the result of a child Agent execution.
//
// SpawnResult 携带子 Agent 执行的结果。
type SpawnResult struct {
	AgentName string
	Result    *kernel.Result
	Err       error
}

// Spawn launches a child Agent in a new goroutine and returns a channel
// that will receive the result once execution completes.
// This is the low-level primitive for Supervisor delegation.
//
// Spawn 在新 goroutine 中启动子 Agent，返回一个 channel 接收执行结果。
// 这是 Supervisor 委派的底层原语。
func Spawn(ctx context.Context, agent kernel.Agent, input *types.AgentInput, rt kernel.Runtime) <-chan SpawnResult {
	ch := make(chan SpawnResult, 1)
	go func() {
		defer close(ch)
		result := agent.Run(ctx, input, rt)
		ch <- SpawnResult{
			AgentName: agent.Name(),
			Result:    result,
			Err:       result.Err,
		}
	}()
	return ch
}

// SpawnAndWait launches multiple child Agents concurrently and waits for all to complete.
// Uses errgroup-style error collection internally.
// Returns results in the same order as the input map (stable iteration).
//
// SpawnAndWait 并发启动多个子 Agent，等待全部完成。
// 内部使用 errgroup 风格收集错误。
// 返回结果顺序与输入 map 一致（稳定迭代）。
func SpawnAndWait(ctx context.Context, agents map[string]kernel.Agent, inputs map[string]*types.AgentInput, rt kernel.Runtime) []SpawnResult {
	n := len(agents)
	results := make([]SpawnResult, 0, n)
	resultCh := make(chan SpawnResult, n)

	var wg sync.WaitGroup
	for name, agent := range agents {
		wg.Add(1)
		name := name
		agent := agent
		input := inputs[name]
		go func() {
			defer wg.Done()
			res := Spawn(ctx, agent, input, rt)
			sr := <-res
			sr.AgentName = name
			resultCh <- sr
		}()
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	for sr := range resultCh {
		results = append(results, sr)
	}

	return results
}

// SpawnMap launches child agents from a name-to-agent map with a shared input.
// All agents receive the same input.
//
// SpawnMap 从 name→Agent 映射中并发启动子 Agent，所有 Agent 接收相同输入。
func SpawnMap(ctx context.Context, agents map[string]kernel.Agent, input *types.AgentInput, rt kernel.Runtime) []SpawnResult {
	inputs := make(map[string]*types.AgentInput, len(agents))
	for name := range agents {
		inputs[name] = input
	}
	return SpawnAndWait(ctx, agents, inputs, rt)
}
