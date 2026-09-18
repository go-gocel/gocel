// Command customagent 演示不使用内置装配器，直接实现 kernel.Agent 契约。
//
// 自定义 Agent 自己掌握"怎么跑"：本示例是一个单次调用的翻译 Agent ——
// 构建消息 → 调一次模型 → 返回带终止原因的 Result，没有工具、没有循环。
// 它同样交给 runner 托管：Runner 为它注入 Runtime（模型访问、钩子、状态），
// 并保证事件流与运行契约和内置 Agent 完全一致；模块也用同一套 API 挂载
// （这里是 module/audit，把运行、模型调用、工具调用记成审计轨迹）。
//
// 模型配置读自 example/.env。在仓库根目录运行：
//
//	go run ./example/customagent
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runner"
	"github.com/go-gocel/gocel/core/types"
	"github.com/go-gocel/gocel/llm"
	"github.com/go-gocel/gocel/module/audit"
	"github.com/go-gocel/gocel/module/guard"
	"github.com/go-gocel/gocel/module/memory"
	"github.com/go-gocel/gocel/module/msgcheck"
	"github.com/go-gocel/gocel/module/repeattool"
	"github.com/go-gocel/gocel/module/timecontext"
)

// translatorAgent 是手写的 Agent：自己构建消息、自己决定调用策略。
type translatorAgent struct {
	name         string
	description  string
	systemPrompt string
}

var _ kernel.Agent = (*translatorAgent)(nil)

func (a *translatorAgent) Name() string        { return a.name }
func (a *translatorAgent) Description() string { return a.description }

// Run 实现 kernel.Agent 的全部契约。
func (a *translatorAgent) Run(ctx context.Context, input *types.AgentInput, rt kernel.Runtime) *kernel.Result {
	msgs := []*types.Message{types.NewSystemMessage(a.systemPrompt)}
	if input != nil {
		msgs = append(msgs, input.Messages...)
	}

	var err error
	ctx, msgs, err = rt.FireMessagesBuilt(ctx, msgs)
	if err != nil {
		return &kernel.Result{Reason: kernel.TerminateError, Err: err}
	}

	// 自定义策略：只调一次模型，并带上自己的生成参数。
	resp, usage, err := rt.CallModel(ctx, msgs, kernel.WithTemperature(0.2))
	if err != nil {
		return &kernel.Result{Messages: msgs, Reason: kernel.TerminateError, Err: err}
	}
	if resp == nil {
		return &kernel.Result{Messages: msgs, Reason: kernel.TerminateError, Err: errors.New("模型返回空响应")}
	}

	msgs = append(msgs, resp)
	return &kernel.Result{
		Content:    resp.Content,
		Messages:   msgs,
		TokenUsage: usage,
		Reason:     kernel.TerminateFinished,
	}
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	loadEnv()

	model := llm.NewOpenAICompatibleModel(
		os.Getenv("MODLE_NAME"),
		os.Getenv("MODLE_KEY"),
		os.Getenv("MODLE_URL"),
	)

	agent := &translatorAgent{
		name:         "translator",
		description:  "把用户输入翻译成英文的单次调用 Agent",
		systemPrompt: "你是一名中英翻译。只输出译文本身，不要解释，不要加引号。",
	}

	// 自定义 Agent 不需要 agents 装配器，模块在 Runner 层挂载即可。
	// 全部工具与全部模块的完整装配见 example/reactagent。
	r := runner.NewRunner(agent, model,
		runner.WithModule(timecontext.New()),       // 注入当前时间
		runner.WithModule(guard.NewGuardModule()),  // 上下文窗口裁剪
		runner.WithModule(repeattool.New()),        // 重复工具调用提醒
		runner.WithModule(msgcheck.New()),          // 模型调用前消息校验
		runner.WithModule(memory.NewMemoryModule()),// 跨步记忆
		runner.WithModule(audit.New()),             // 审计轨迹默认写到 stderr
	)

	prompt := "把这句话翻译成英文：优雅的软件源于纪律。"
	fmt.Printf("用户: %s\n\n", prompt)

	info := r.Run(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage(prompt)},
	})
	if info.Err != nil {
		return info.Err
	}

	fmt.Printf("Agent: %s\n", info.Result.Content)
	fmt.Printf("终止原因: %s\n", info.Result.Reason)
	if u := info.Result.TokenUsage; u != nil {
		fmt.Printf("Token: prompt=%d completion=%d total=%d\n", u.PromptTokens, u.CompletionTokens, u.TotalTokens)
	}
	fmt.Printf("消息条数: %d\n", len(info.AllMsgs))
	return nil
}

// loadEnv 把 example/.env（或当前目录的 .env）读进进程环境。
func loadEnv() {
	data, err := os.ReadFile(filepath.Join("example", ".env"))
	if err != nil {
		data, err = os.ReadFile(".env")
	}
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			os.Setenv(strings.TrimSpace(key), strings.TrimSpace(value))
		}
	}
}
