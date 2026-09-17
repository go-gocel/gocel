//go:build !windows

package toolutil

import "os/exec"

// HideWindow is a no-op on non-Windows platforms: console children of GUI
// parents are only a Windows concern.
//
// HideWindow 在非 Windows 平台上是空操作：只有 Windows 才需要考虑 GUI
// 父进程的控制台子进程问题。
func HideWindow(cmd *exec.Cmd) {}
