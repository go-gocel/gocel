// Command reactagent 演示一个跑真实模型的 ReactAgent，工具与模块全部使用框架现成实现。
//
// 要点：
//   - agents.NewReactAgent 内置 ReAct 循环（模型调用 → 工具执行 → 观察 → 重复）；
//   - 工具来自 tools/* 内置提供者：glob（找文件）、grep（搜内容）、read（读文件），
//     各自用 AllTools() 直接拿到 []kernel.Tool；
//   - 模块来自 module/*：timecontext 注入当前时间、guard 管上下文窗口、
//     repeattool 在重复调用同一工具时给出提醒；
//   - runner.NewRunner 把 Agent、模块与 llm 模型绑定，Runner.Stream 流式执行。
//
// 模型配置读自 example/.env。在仓库根目录运行：
//
//	go run ./example/reactagent
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-gocel/gocel/agents"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runner"
	"github.com/go-gocel/gocel/core/types"
	"github.com/go-gocel/gocel/llm"
	"github.com/go-gocel/gocel/module/guard"
	"github.com/go-gocel/gocel/module/repeattool"
	"github.com/go-gocel/gocel/module/timecontext"
	"github.com/go-gocel/gocel/tools/glob"
	"github.com/go-gocel/gocel/tools/grep"
	"github.com/go-gocel/gocel/tools/read"
)

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

	agent := agents.NewReactAgent(
		agents.WithName("repo-explorer"),
		agents.WithDescription("用内置文件工具探索代码仓库的 ReAct 助手"),
		agents.WithTools(builtinTools()...),
		agents.WithModules(
			timecontext.New(),      // 每步把当前时间注入消息流
			guard.NewGuardModule(), // 上下文窗口超预算时裁剪
			repeattool.New(),       // 连续重复调用同一工具时提醒模型
		),
		agents.WithMaxSteps(8),
	)

	r := runner.NewRunner(agent, model)

	prompt := "用工具探索本仓库的 example 目录：先用 glob 列出 example 下所有 .go 文件，" +
		"再用 grep 找出 NewReactAgent 出现在哪些文件、第几行，最后用两句话汇总。"
	fmt.Printf("用户: %s\n\n", prompt)

	handle, err := r.Stream(context.Background(), &types.AgentInput{
		Messages: []*types.Message{types.NewUserMessage(prompt)},
	})
	if err != nil {
		return err
	}
	printEvents(handle)

	result := handle.Result()
	if result.Err != nil {
		return result.Err
	}
	fmt.Printf("\n终止原因: %s\n", result.Reason)
	return nil
}

// builtinTools 收集框架内置的文件工具。
func builtinTools() []kernel.Tool {
	var tools []kernel.Tool
	tools = append(tools, glob.AllTools()...)
	tools = append(tools, grep.AllTools()...)
	tools = append(tools, read.AllTools()...)
	return tools
}

// printEvents 消费流式事件：工具调用与结果即时打印，最终答案逐 token 输出。
func printEvents(handle *runner.StreamHandle) {
	printed := false
	for {
		ev, ok := handle.Next()
		if !ok {
			break
		}
		switch ev.Type {
		case types.EventToken:
			printed = true
			fmt.Print(ev.Content)
		case types.EventToolCall:
			fmt.Printf("\n[工具调用] %s %s\n", ev.ToolName, ev.ToolArgs)
		case types.EventToolResult:
			fmt.Printf("[工具结果] %s -> %s\n", ev.ToolName, preview(ev.Content))
		case types.EventError:
			fmt.Printf("\n[错误] %s\n", ev.Content)
		case types.EventFinish:
			if !printed {
				fmt.Print(ev.Content)
			}
		}
	}
	fmt.Println()
}

// preview 截断工具结果，避免刷屏。
func preview(s string) string {
	const max = 200
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
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
