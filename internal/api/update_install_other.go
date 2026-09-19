//go:build !darwin && !windows

package api

import "fmt"

// installUpdate 非 macOS/Windows 平台暂不支持应用内自动安装（GUI 更新未覆盖）。
func installUpdate(string, bool) error {
	return fmt.Errorf("当前平台暂不支持自动安装，请到发布页手动下载")
}
