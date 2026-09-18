// Command reactagent 演示一个跑真实模型的 ReactAgent，把框架现成的
// 全部工具（tools/*）与全部模块（module/*）都装配进来。
//
// 装配分两层：
//  1. 工具：tools/* 每个包都接进来。零依赖的用 AllTools()；需要依赖的
//     （goal/jobs/skill/subagent/workflow/shell 后台任务）
//     在 assembly 里共享同一份 manager / registry / task runner。
//  2. 模块：module/* 里实现 kernel.Module 的全部挂到 agents.WithModules。
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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-gocel/gocel/agents"
	workflowengine "github.com/go-gocel/gocel/agents/workflow"
	coregoal "github.com/go-gocel/gocel/core/goal"
	corejobs "github.com/go-gocel/gocel/core/jobs"
	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runner"
	coresession "github.com/go-gocel/gocel/core/session"
	skillcore "github.com/go-gocel/gocel/core/skill"
	"github.com/go-gocel/gocel/core/types"
	"github.com/go-gocel/gocel/llm"
	"github.com/go-gocel/gocel/module/audit"
	"github.com/go-gocel/gocel/module/checkpoint"
	"github.com/go-gocel/gocel/module/contextfilters"
	"github.com/go-gocel/gocel/module/goalround"
	"github.com/go-gocel/gocel/module/guard"
	"github.com/go-gocel/gocel/module/hitl"
	"github.com/go-gocel/gocel/module/memory"
	"github.com/go-gocel/gocel/module/msgcheck"
	"github.com/go-gocel/gocel/module/observability"
	"github.com/go-gocel/gocel/module/permission"
	"github.com/go-gocel/gocel/module/planmode"
	"github.com/go-gocel/gocel/module/repeattool"
	"github.com/go-gocel/gocel/module/schedule"
	"github.com/go-gocel/gocel/module/session"
	"github.com/go-gocel/gocel/module/state"
	"github.com/go-gocel/gocel/module/timecontext"
	globtool "github.com/go-gocel/gocel/tools/glob"
	goaltool "github.com/go-gocel/gocel/tools/goal"
	greptool "github.com/go-gocel/gocel/tools/grep"
	jobstool "github.com/go-gocel/gocel/tools/jobs"
	multiedittool "github.com/go-gocel/gocel/tools/multiedit"
	readtool "github.com/go-gocel/gocel/tools/read"
	renametool "github.com/go-gocel/gocel/tools/rename"
	scanstructure "github.com/go-gocel/gocel/tools/scan_structure"
	shelltool "github.com/go-gocel/gocel/tools/shell"
	skilltool "github.com/go-gocel/gocel/tools/skill"
	todotool "github.com/go-gocel/gocel/tools/todo"
	trashtool "github.com/go-gocel/gocel/tools/trash"
	webtool "github.com/go-gocel/gocel/tools/web"
	workflowtool "github.com/go-gocel/gocel/tools/workflow"
	writetool "github.com/go-gocel/gocel/tools/write"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	loadEnv()
	ctx := context.Background()

	a := newAssembly()

	// HITL 的审批与对话工具需要人工答复，这里由命令行侧自动应答。
	interrupt := make(chan string, 1)

	tools, err := a.tools()
	if err != nil {
		return err
	}
	modules, err := a.modules()
	if err != nil {
		return err
	}

	agent := agents.NewReactAgent(
		agents.WithName("full-agent"),
		agents.WithDescription("装配了全部内置工具与模块的 ReAct 助手"),
		agents.WithTools(tools...),
		agents.WithModules(modules...),
		agents.WithMaxSteps(8),
	)

	// module/state 提供共享状态管理器，挂到 Runtime 供模块与工具共用。
	r := runner.NewRunner(agent, newModel(),
		runner.WithStateManager(state.NewLocalState()),
	)

	prompt := "帮我把这个仓库的 example 目录整理一下：列出其中所有 .go 文件，" +
		"找出 NewReactAgent 是在哪个文件的第几行被调用的，把结论记进待办清单，然后用两句话汇报。"
	fmt.Printf("用户: %s\n\n", prompt)

	// 通过 StreamSender 实时观察 Agent 的工作过程。
	info := r.Run(ctx, &types.AgentInput{
		Messages:       []*types.Message{types.NewUserMessage(prompt)},
		InterruptInput: interrupt,
		StreamSender:   func(ev *types.Event) bool { return printEvent(ev, interrupt) },
		Meta:           map[string]any{types.SessionIDKey: a.sessionID},
	})
	if info.Err != nil {
		return info.Err
	}

	fmt.Printf("\n助手: %s\n\n", info.Result.Content)
	fmt.Printf("终止原因: %s\n", info.Result.Reason)
	if u := info.Result.TokenUsage; u != nil {
		fmt.Printf("Token: prompt=%d completion=%d total=%d\n", u.PromptTokens, u.CompletionTokens, u.TotalTokens)
	}
	return nil
}

// newModel 按 example/.env 构建真实模型。
func newModel() kernel.Model {
	return llm.NewOpenAICompatibleModel(
		os.Getenv("MODLE_NAME"),
		os.Getenv("MODLE_KEY"),
		os.Getenv("MODLE_URL"),
	)
}

// ── 装配 ──────────────────────────────────────────────────────────────────

// assembly 汇总装配期需要共享的依赖，避免各处重复创建。
type assembly struct {
	sessionID  string
	sessionLog *coresession.Log
	tasks      *shelltool.TaskRunner
	goalMgr    *coregoal.Manager
	delegation *agents.Delegation
	hitlModule *hitl.Module
	shellName  string
	shellArgs  []string
	skillRoot  string
}

func newAssembly() *assembly {
	sessionID := types.SessionID()
	shellName, shellArgs := resolveShell()
	tasks := shelltool.NewTaskRunnerWithRegistry(corejobs.NewRegistry(), filepath.Join(os.TempDir(), "gocel-example-tasks"))
	// 后台任务运行在与工具调用 ctx 无关的注册表上下文上，shell 必须显式设置。
	tasks.ShellName, tasks.ShellArgs = shellName, shellArgs

	return &assembly{
		sessionID:  sessionID,
		sessionLog: coresession.NewLog(sessionID),
		tasks:      tasks,
		goalMgr:    coregoal.NewManager(coregoal.NewMemoryStore()),
		delegation: delegation(),
		shellName:  shellName,
		shellArgs:  shellArgs,
		skillRoot:  seedSkillsDir(),
		hitlModule: hitl.New(
			hitl.WithUserDialogue("ask_user", "向用户提问并等待人工回答"),
			hitl.WithTimeout(30*time.Second),
		),
	}
}

// resolveShell 探测本机可用的执行 shell。shell 工具默认 "sh -c"（POSIX 基线），
// 无 sh 的环境（无 Git Bash 的 Windows）必须显式注入，否则 terminal / task_run
// 一律失败。
func resolveShell() (name string, args []string) {
	for _, candidate := range [][]string{{"sh", "-c"}, {"bash", "-c"}} {
		if _, err := exec.LookPath(candidate[0]); err == nil {
			return candidate[0], candidate[1:]
		}
	}
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c"}
	}
	return "sh", []string{"-c"}
}

// seedSkillsDir 在临时目录里落一个示例技能，让 skill_list / skill_load 有真实内容。
func seedSkillsDir() string {
	root := filepath.Join(os.TempDir(), "gocel-example-skills")
	dir := filepath.Join(root, "release-notes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[skill] 创建示例技能目录: %v", err)
		return root
	}
	const skillMD = `---
name: release-notes
description: 按约定格式从提交记录生成发布说明
version: 1.0.0
tags: [docs, release]
---

# 发布说明生成
1. 收集本次发布的提交标题。
2. 按 特性 / 修复 / 其它 分组。
3. 每条一行，动词开头，不写实现细节。
`
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		log.Printf("[skill] 写入示例技能: %v", err)
	}
	return root
}

// tools 装配 tools/* 下全部工具包。
func (a *assembly) tools() ([]kernel.Tool, error) {
	var tools []kernel.Tool

	// ── 零依赖内置提供者 ──
	// 注：tools/git 目录当前为空（无实现），故不在此列出。
	tools = append(tools, globtool.AllTools()...)
	tools = append(tools, greptool.AllTools()...)
	tools = append(tools, readtool.AllTools()...)
	tools = append(tools, writetool.AllTools()...)
	tools = append(tools, multiedittool.AllTools()...)
	tools = append(tools, renametool.AllTools()...)
	tools = append(tools, scanstructure.AllTools()...)
	tools = append(tools, trashtool.AllTools()...)
	tools = append(tools, webtool.AllTools()...)

	// 待办：挂上会话日志后，任务列表会持久化为会话事件。
	tools = append(tools, todotool.New().WithLog(a.sessionLog).ListTools()...)

	// 终端与后台任务。
	tools = append(tools, shelltool.AllTools(shelltool.WithShell(a.shellName, a.shellArgs...))...)
	tools = append(tools,
		shelltool.TaskRunTool(a.tasks),
		shelltool.TaskStatusTool(a.tasks),
		shelltool.TaskStopTool(a.tasks),
	)
	jobTools, err := jobstool.Tools(jobstool.Config{Registry: a.tasks.Registry()})
	if err != nil {
		return nil, err
	}
	tools = append(tools, jobTools...)

	// 目标：与 module/goalround 共用同一个 Manager。
	goalTools, err := goaltool.Tools(goaltool.Config{Manager: a.goalMgr})
	if err != nil {
		return nil, err
	}
	tools = append(tools, goalTools...)

	// 技能：注册示例技能扫描根（目录不存在只是没有候选，不会报错）。
	skillRegistry := skillcore.New()
	if err := skillRegistry.AddRoot(a.skillRoot, 500, 0); err != nil {
		return nil, err
	}
	skillTools, err := skilltool.Tools(skilltool.Config{Registry: skillRegistry})
	if err != nil {
		return nil, err
	}
	tools = append(tools, skillTools...)

	// 子代理：一次委派装配同时给出子代理工厂、共享注册表与模型可见的委派工具。
	delegationTools, err := a.delegation.Tools()
	if err != nil {
		return nil, err
	}
	tools = append(tools, delegationTools...)

	// 工作流：复用同一套子代理机制（同一个注册表与工厂）。
	engine, err := workflowengine.New(workflowengine.Config{
		Registry: a.delegation.Registry(),
		Factory:  a.delegation.Agent,
	})
	if err != nil {
		return nil, err
	}
	workflowTools, err := workflowtool.Tools(workflowtool.Config{Engine: engine})
	if err != nil {
		return nil, err
	}
	tools = append(tools, workflowTools...)

	// 定时提醒：schedule 不是 kernel.Module，它自己产出工具。
	scheduler, err := schedule.New(a.sessionID, schedule.Config{
		Log:     a.sessionLog,
		Deliver: func(context.Context, string, schedule.Reminder) {},
	})
	if err != nil {
		return nil, err
	}
	scheduleTools, err := scheduler.Tools()
	if err != nil {
		return nil, err
	}
	tools = append(tools, scheduleTools...)

	// HITL 模块暴露的对话工具。
	tools = append(tools, a.hitlModule.AsTools()...)

	return tools, nil
}

// modules 装配 module/* 下全部实现 kernel.Module 的模块。
func (a *assembly) modules() ([]kernel.Module, error) {
	// plan 模式：日志即状态；未进入 plan 模式时门禁空转。
	planMode, err := planmode.New(planmode.Config{
		Log:       a.sessionLog,
		Ask:       func(context.Context, *kernel.ApprovalRequest) (kernel.ApprovalDecision, error) { return kernel.ApprovalAuto, nil },
		OnApprove: func(string) {},
	})
	if err != nil {
		return nil, err
	}

	return []kernel.Module{
		timecontext.New(),                                                      // 注入当前时间
		guard.NewGuardModule(),                                                 // 上下文窗口裁剪
		repeattool.New(),                                                       // 重复工具调用提醒
		msgcheck.New(),                                                         // 模型调用前消息校验
		memory.NewMemoryModule(),                                               // 跨步记忆
		audit.New(),                                                            // 审计轨迹（stderr）
		contextfilters.MustNew(contextfilters.DefaultConfig()),                 // 工具结果剪裁
		goalroundModule(a),                                                     // 目标轮次驱动
		checkpoint.NewAutoSaveModule(checkpoint.NewInMemoryCheckpointStore()),  // 自动检查点
		observability.NewObservabilityModule(observability.WithNoopExporter()), // OTel 埋点
		session.NewLogWriter(a.sessionLog),                                     // 会话事件日志
		session.NewSessionObserver(session.NewInMemorySessionService()),        // 会话持久化
		permissionModule("danger-full-access"),                                 // 权限档位 + 审批
		planMode,                                                               // plan 模式门禁
		a.hitlModule,                                                           // 人工审批与对话
	}, nil
}

// permissionModule 按预设名装配一个 permission 模块：预设把权限档位与审批处置
// 成对接到示例自带的放行文件策略上。
func permissionModule(presetName string) kernel.Module {
	filePolicy := &permissiveFilePolicy{}
	preset, ok := permission.FindPreset(permission.DefaultPresets, presetName)
	if !ok {
		log.Fatalf("permission: 预设 %s 不存在", presetName)
	}
	approval, err := preset.Apply(filePolicy)
	if err != nil {
		log.Fatalf("permission: 应用预设 %s: %v", presetName, err)
	}
	return permission.New(filePolicy, approval, nil)
}

// goalroundModule 把目标轮次模块接到装配共享的目标管理器上。
func goalroundModule(a *assembly) kernel.Module {
	return goalround.New(a.goalMgr)
}

// permissiveFilePolicy 是示例自带的文件策略：配合 danger-full-access 档位
// 放行一切操作，让示例把注意力放在装配而不是权限调优上。
type permissiveFilePolicy struct {
	mode types.PermissionMode
}

func (p *permissiveFilePolicy) Mode() types.PermissionMode { return p.mode }

func (p *permissiveFilePolicy) Check(types.FileOp, string) error { return nil }

func (p *permissiveFilePolicy) SetMode(m types.PermissionMode) { p.mode = m }

// delegation 装配一次委派：子代理用只读工具、单轮收尾。
func delegation() *agents.Delegation {
	return agents.NewDelegation(
		agents.WithSubAgentFactory(func(context.Context) kernel.Agent {
			return agents.NewSubAgent(
				agents.WithSystemPrompt("你是子代理，用只读工具完成任务，然后用一句话作答。"),
				agents.WithTools(readtool.AllTools()...),
			)
		}),
	)
}

// ── 运行 ──────────────────────────────────────────────────────────────────

// printEvent 把 Agent 的工作过程实时打印出来：模型调了哪个工具、工具返回了什么。
func printEvent(ev *types.Event, interrupt chan<- string) bool {
	switch ev.Type {
	case types.EventToolCall:
		fmt.Printf("[工具调用] %s %s\n", ev.ToolName, preview(ev.ToolArgs))
	case types.EventToolResult:
		fmt.Printf("[工具结果] %s -> %s\n", ev.ToolName, preview(ev.Content))
	case types.EventInterrupt:
		fmt.Printf("[需要人工确认] %s\n", preview(ev.Content))
		interrupt <- "同意，继续。"
	case types.EventError:
		fmt.Printf("[错误] %s\n", ev.Content)
	}
	return true
}

// preview 截断长文本，避免刷屏。
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
