package inject

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ============================================================
// 官方客户端探测与带 CDP 启动
//
// 对齐 WorkDaddy 的探测策略：扫描名称带 WorkBuddy 前缀且包含
// Electron 主程序的客户端（覆盖企业定制版）；用户在设置里显式
// 指定路径时优先使用显式值。
// ============================================================

// findClientBin 探测官方客户端可执行文件（explicit 非空时直接校验返回）
func findClientBin(explicit string) (string, error) {
	if p := strings.TrimSpace(explicit); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
		return "", fmt.Errorf("指定的客户端路径不存在: %s", p)
	}

	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		apps, _ := filepath.Glob("/Applications/*.app")
		for _, app := range apps {
			base := filepath.Base(app)
			if strings.HasPrefix(base, "WorkBuddy") {
				if exe := firstExisting(
					filepath.Join(app, "Contents/MacOS/Electron"),
				); exe != "" {
					candidates = append(candidates, exe)
				}
			}
		}
		// 用户级 /Applications 兜底
		home, _ := os.UserHomeDir()
		userApps, _ := filepath.Glob(filepath.Join(home, "Applications/*.app"))
		for _, app := range userApps {
			base := filepath.Base(app)
			if strings.HasPrefix(base, "WorkBuddy") {
				if exe := firstExisting(filepath.Join(app, "Contents/MacOS/Electron")); exe != "" {
					candidates = append(candidates, exe)
				}
			}
		}
	case "windows":
		local := os.Getenv("LocalAppData")
		patterns := []string{
			filepath.Join(local, "Programs", "*", "WorkBuddy*.exe"),
			filepath.Join(local, "Programs", "WorkBuddy*", "*.exe"),
		}
		for _, p := range patterns {
			matches, _ := filepath.Glob(p)
			for _, m := range matches {
				lower := strings.ToLower(m)
				if !strings.Contains(lower, "uninstall") && !strings.Contains(lower, "setup") {
					candidates = append(candidates, m)
				}
			}
		}
	case "linux":
		for _, dir := range []string{"/opt", "/usr/share"} {
			matches, _ := filepath.Glob(filepath.Join(dir, "workbuddy*", "workbuddy"))
			candidates = append(candidates, matches...)
		}
		home, _ := os.UserHomeDir()
		candidates = append(candidates, filepath.Join(home, ".local/share/workbuddy/workbuddy"))
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("未自动找到官方客户端，请在设置中手动指定客户端路径")
	}
	return candidates[0], nil
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// ensureCDPPort 确保调试端口可达：已可达直接返回；否则参考 WorkDaddy
// 的做法两步走——先优雅退出正在普通模式运行的客户端（Electron 单实例
// 锁下直接拉第二个实例会把当前客户端顶退出，这正是「注入导致程序退出」
// 的根因），再带 --remote-debugging-port 重新启动并等待端口就绪。
func ensureCDPPort(port int, clientBin string) error {
	if cdpAlive(port) {
		return nil
	}
	if clientBin == "" {
		return fmt.Errorf("客户端未以调试模式运行，且未配置客户端路径")
	}
	// 先关闭正在运行的普通模式客户端
	if clientRunning(clientBin) {
		if err := quitClient(clientBin); err != nil {
			return fmt.Errorf("关闭正在运行的官方客户端失败，请手动退出后重试: %w", err)
		}
		// 退干净后稍等片刻，避免单实例锁/端口尚未释放
		time.Sleep(800 * time.Millisecond)
	}
	cmd := exec.Command(clientBin, fmt.Sprintf("--remote-debugging-port=%d", port))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动客户端失败: %w", err)
	}
	// 不 Wait：客户端是长驻进程，由用户自己管理；避免僵尸进程
	go func() { _ = cmd.Process.Release() }()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cdpAlive(port) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("客户端启动超时（调试端口 %d 未就绪），请重试", port)
}

// clientRunning 客户端是否正在运行（匹配完整命令行中的可执行文件路径）
func clientRunning(bin string) bool {
	switch runtime.GOOS {
	case "windows":
		out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq "+filepath.Base(bin)).Output()
		return err == nil && strings.Contains(strings.ToLower(string(out)), strings.ToLower(filepath.Base(bin)))
	default:
		return exec.Command("pgrep", "-f", "--", bin).Run() == nil
	}
}

// quitClient 优雅退出客户端。
// 关键：macOS 先用 AppleScript `quit app` 走正常退出流程——Electron 会保存
// 会话/草稿等状态；直接 SIGTERM 会立即终止、跳过保存，导致 WorkBuddy
// 最新会话丢失（对齐 WorkDaddy relaunch-with-cdp.sh 的退出顺序）。
// pkill -TERM 仅作兜底，-9 是最后手段。
func quitClient(bin string) error {
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/F", "/IM", filepath.Base(bin)).Run()
		time.Sleep(500 * time.Millisecond)
		if clientRunning(bin) {
			return fmt.Errorf("客户端进程未能退出")
		}
		return nil
	}
	// 应用名：/Applications/X.app/Contents/MacOS/Electron → X
	appName := strings.TrimSuffix(filepath.Base(filepath.Dir(filepath.Dir(bin))), ".app")
	if appName != "" && appName != "MacOS" {
		_ = exec.Command("osascript", "-e", "quit app \""+appName+"\"").Run()
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !clientRunning(bin) {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	_ = exec.Command("pkill", "-TERM", "-f", "--", bin).Run()
	time.Sleep(2 * time.Second)
	if !clientRunning(bin) {
		return nil
	}
	_ = exec.Command("pkill", "-KILL", "-f", "--", bin).Run()
	time.Sleep(500 * time.Millisecond)
	if clientRunning(bin) {
		return fmt.Errorf("客户端进程未能退出")
	}
	return nil
}

// cdpAlive 探测调试端口
func cdpAlive(port int) bool {
	_, err := listTargets(port)
	return err == nil
}
