//go:build !windows

package core

import (
	"fmt"
	"os/exec"
	"runtime"
)

// --- 非 Windows 平台实现：进程派生型（macOS caffeinate / Linux systemd-inhibit） ---

func startPlatformKeepAwake() (stop func(), note string, err error) {
	switch runtime.GOOS {
	case "darwin":
		return startExecKeepAwake("/usr/bin/caffeinate", []string{"-i", "-s"})
	case "linux":
		return startExecKeepAwake("systemd-inhibit", []string{"--what=sleep", "sleep", "infinity"})
	default:
		return nil, "", fmt.Errorf("平台 %s 暂不支持防休眠", runtime.GOOS)
	}
}

// startExecKeepAwake 派生长驻进程维持唤醒；stop 终止进程即释放
func startExecKeepAwake(name string, args []string) (stop func(), note string, err error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, "", fmt.Errorf("未找到 %s（%s 平台防休眠不可用）", name, runtime.GOOS)
	}
	cmd := exec.Command(path, args...)
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("启动 %s 失败: %w", name, err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return func() {
		_ = cmd.Process.Kill()
		<-done
	}, name, nil
}
