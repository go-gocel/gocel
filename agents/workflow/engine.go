package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-gocel/gocel/core/types"
)

// Run executes the spec: every item flows through the stages independently
// (DSH pipeline semantics — no barrier between stages), each stage-item on
// a fresh one-shot subagent, at most MaxConcurrent children at once.
//
// Fatal errors (closed vocabulary) kill the whole run; an individual
// child's failure or schema mismatch nulls only that item, which skips its
// remaining stages.
//
// Run 执行 spec：每个条目独立流经各阶段（DSH pipeline 语义——阶段间无
// 屏障），每个阶段-条目跑在全新的一次性子代理上，最多 MaxConcurrent 个
// 并发子代理。
//
// 致命错误（封闭词汇）终止整个运行；单个子代理失败或 schema 不匹配只把
// 该条目置 null，并跳过其剩余阶段。
func (e *Engine) Run(ctx context.Context, spec *Spec) (*Result, error) {
	runID := fmt.Sprintf("wf-%d", time.Now().UnixNano())
	result := &Result{RunID: runID, Items: make([]ItemResult, len(spec.Items))}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		started  int64
		startedM sync.Mutex
		fatalM   sync.Mutex
		fatalErr error
	)
	recordStart := func() {
		startedM.Lock()
		started++
		startedM.Unlock()
	}
	fatal := func(err *Error) {
		fatalM.Lock()
		if fatalErr == nil {
			fatalErr = err
			cancel()
		}
		fatalM.Unlock()
	}

	for i, item := range spec.Items {
		wg.Add(1)
		go func(i int, item any) {
			defer wg.Done()
			res := ItemResult{Index: i, Values: make([]any, len(spec.Stages))}
			for si, st := range spec.Stages {
				if runCtx.Err() != nil {
					result.Items[i] = res
					return
				}
				if !e.slots.acquire(runCtx) {
					result.Items[i] = res
					return
				}
				v, ok, fatalErr2 := e.runStageItem(runCtx, si, i, item, st, recordStart)
				e.slots.release()
				if fatalErr2 != nil {
					fatal(fatalErr2)
					result.Items[i] = res
					return
				}
				if !ok {
					// Ordinary outcome: this item is null from here on and
					// skips its remaining stages.
					result.Items[i] = res
					return
				}
				res.Values[si] = v
			}
			result.Items[i] = res
		}(i, item)
	}
	wg.Wait()

	if fatalErr != nil {
		return nil, fatalErr
	}
	if ctx.Err() != nil {
		return nil, fatalf(CodeCanceled, "%v", ctx.Err())
	}
	result.AgentsStarted = int(started)
	return result, nil
}

// runStageItem runs one stage-item: renders the prompt, starts a one-shot
// child, and reduces its result to a value. The bool is false for ordinary
// item failures (child error, schema mismatch); a non-nil *Error is fatal.
func (e *Engine) runStageItem(ctx context.Context, stageIdx, itemIdx int, item any, st Stage, recordStart func()) (any, bool, *Error) {
	prompt, err := renderPrompt(st.Prompt, itemIdx, item)
	if err != nil {
		return nil, false, fatalf(CodeInvalidSpec, "stage %d: %v", stageIdx, err)
	}
	agent := e.cfg.Factory(ctx)
	if agent == nil {
		return nil, false, fatalf(CodeAgentStart, "stage %d item %d: factory returned nil", stageIdx, itemIdx)
	}
	result, err := e.cfg.Registry.Run(ctx, "", fmt.Sprintf("stage %d item %d", stageIdx, itemIdx), agent, &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage(prompt)},
	})
	if err != nil {
		if ctx.Err() != nil {
			// Cancellation is reported by Run via CodeCanceled; the child
			// was disposed.
			return nil, false, nil
		}
		return nil, false, fatalf(CodeAgentStart, "stage %d item %d: %v", stageIdx, itemIdx, err)
	}
	recordStart()
	if result == nil {
		return nil, true, fatalf(CodeAgentStart, "stage %d item %d: child returned no result", stageIdx, itemIdx)
	}
	if result.Err != nil {
		return nil, false, nil // ordinary child failure → null item
	}
	if st.Schema == nil {
		return result.Content, true, nil
	}
	var v any
	if err := json.Unmarshal([]byte(result.Content), &v); err != nil {
		return nil, false, nil // not JSON → schema mismatch → null item
	}
	if err := st.Schema.validate(v); err != nil {
		return nil, false, nil // schema mismatch → null item
	}
	return v, true, nil
}

// renderPrompt substitutes {{item}} (item JSON) and {{index}} (item index).
// renderPrompt 替换 {{item}}（条目 JSON）与 {{index}}（条目序号）。
func renderPrompt(tpl string, index int, item any) (string, error) {
	itemJSON, err := json.Marshal(item)
	if err != nil {
		return "", fmt.Errorf("item %d is not JSON-serializable: %v", index, err)
	}
	out := strings.ReplaceAll(tpl, "{{item}}", string(itemJSON))
	return strings.ReplaceAll(out, "{{index}}", strconv.Itoa(index)), nil
}
