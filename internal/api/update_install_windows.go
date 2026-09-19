//go:build windows

package api

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const createNoWindow = 0x08000000

// 残留清理：上一次自动更新被中断时，运行目录里会留下备份与新包，
// 此时旧进程已退出、文件不再被占用，启动时直接清掉。
func init() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	dir := filepath.Dir(exePath)
	_ = os.Remove(exePath + ".old.exe")
	_ = os.Remove(filepath.Join(dir, "BuddyBot.new.exe"))
}

// installWindows 安装更新。便携 exe：rename 自替换后重启；
// NSIS 安装器：静默模式重装（安装器自带 UAC 提权），装完退出。
func installUpdate(updateExe string, isNSIS bool) error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	if isNSIS {
		// 静默安装：安装器进程独立运行并自行提权；当前应用退出让出文件
		cmd := exec.Command(updateExe, "/S")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("启动安装器失败: %w", err)
		}
		quitApp()
		return nil
	}

	// 便携 exe 自替换：Windows 允许重命名运行中的 exe，但不允许覆盖/删除
	dir := filepath.Dir(exePath)
	newExe := filepath.Join(dir, "BuddyBot.new.exe")
	oldExe := exePath + ".old.exe"
	if err := copyFile(updateExe, newExe); err != nil {
		return fmt.Errorf("准备新版本失败: %w", err)
	}
	if err := os.Rename(exePath, oldExe); err != nil {
		_ = os.Remove(newExe)
		return fmt.Errorf("备份当前版本失败: %w", err)
	}
	if err := os.Rename(newExe, exePath); err != nil {
		_ = os.Rename(oldExe, exePath) // 回滚
		return fmt.Errorf("替换主程序失败: %w", err)
	}

	// 新进程延迟 2s 拉起（等旧进程完全退出），失败时旧版仍在，可手动重启
	cmd := exec.Command("cmd", "/C", fmt.Sprintf(`timeout /t 2 /nobreak >nul & start "" "%s"`, exePath))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("计划重启失败（新版已就位，重启应用即生效）: %w", err)
	}
	quitApp()
	return nil
}
