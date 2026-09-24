package api

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// saveExportDialog 弹出系统原生「存储」对话框，让用户选择导出位置。
// 返回所选绝对路径；用户取消时返回 ""（调用方静默结束，不报错）。
func saveExportDialog(defaultName, filterName, filterPattern string) (string, error) {
	app := application.Get()
	if app == nil {
		return "", fmt.Errorf("应用未就绪")
	}
	d := app.Dialog.SaveFile().
		SetFilename(defaultName).
		CanCreateDirectories(true)
	if filterPattern != "" {
		d.AddFilter(filterName, filterPattern)
	}
	path, err := d.PromptForSingleSelection()
	if err != nil {
		return "", fmt.Errorf("打开保存对话框失败: %w", err)
	}
	return path, nil // "" = 用户取消
}

// revealInFileManager 在系统文件管理器中定位刚导出的文件（保存完成后自动反馈位置）。
func revealInFileManager(path string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", "-R", path).Start()
	case "windows":
		_ = exec.Command("explorer", "/select,", path).Start()
	default:
		_ = exec.Command("xdg-open", filepath.Dir(path)).Start()
	}
}
