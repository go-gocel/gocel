//go:build windows

package toolutil

import (
	"os/exec"
	"syscall"
)

// HideWindow prevents a spawned console child process from opening a new
// console window. Windows GUI apps (e.g. Wails desktop apps) must set this on
// every child process, otherwise each `sh`/`git`/MCP invocation pops a cmd
// window on the user's desktop.
//
// HideWindow 阻止派生的控制台子进程打开新的控制台窗口。Windows GUI 应用
// （如 Wails 桌面应用）必须在每个子进程上设置此项，否则每次 `sh`/`git`/MCP
// 调用都会在用户桌面上弹出一个 cmd 窗口。
func HideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
