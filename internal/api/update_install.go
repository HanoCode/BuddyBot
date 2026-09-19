package api

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"workbuddy-desktop/internal/core"
)

// InstallUpdate 安装已下载并校验通过的更新包（DownloadUpdate 返回的路径），
// 随后退出应用并由平台专属机制完成替换/重启。仅接受更新缓存目录内的文件，
// 防止任意路径执行。
func (s *SystemAPI) InstallUpdate(ctx context.Context, filePath string) error {
	if strings.TrimSpace(updateRepo) == "" {
		return fmt.Errorf("当前构建未配置更新通道，无法自动安装")
	}
	cacheDir, err := updateCacheDir()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(abs, cacheDir+string(filepath.Separator)) {
		return fmt.Errorf("更新包不在更新缓存目录内，已拒绝安装")
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("更新包不存在或已失效，请重新检查更新")
	}

	core.EmitEvent(core.EventUpdateProgress, map[string]any{"stage": "installing"})
	name := strings.ToLower(filepath.Base(abs))
	switch runtime.GOOS {
	case "darwin":
		if !strings.HasSuffix(name, ".zip") {
			return fmt.Errorf("macOS 自动更新仅支持 .app 的 zip 包，请到发布页手动下载 dmg 安装")
		}
		return installUpdate(abs, false)
	case "windows":
		if !strings.HasSuffix(name, ".exe") {
			return fmt.Errorf("Windows 自动更新仅支持 exe 安装包")
		}
		return installUpdate(abs, strings.Contains(name, "installer"))
	default:
		return installUpdate(abs, false)
	}
}

// quitApp 请求退出应用（替换流程的收尾由平台侧 helper 完成）。
func quitApp() {
	if app := application.Get(); app != nil {
		app.Quit()
	}
}

// updateCacheDir 更新包下载缓存目录（本地缓存优先，退回 data/updates）。
func updateCacheDir() (string, error) {
	dir := "data/updates"
	if base, err := os.UserCacheDir(); err == nil {
		dir = filepath.Join(base, "workbuddy-desktop", "updates")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Abs(dir)
}

// copyFile 普通文件复制（跨盘替换前的中转）。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}
