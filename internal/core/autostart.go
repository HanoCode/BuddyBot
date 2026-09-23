package core

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ============================================================
// 开机自启（登录自启）
//
// 状态一律由真实注册状态推导（macOS LaunchAgent 文件 / Windows 注册表 Run 键 /
// Linux XDG autostart 桌面条目），不写进 config.json——注册本身就是持久化，
// 配置里再存一份会造成双份真相。
// ============================================================

// AutoStartStatus 开机自启真实状态
type AutoStartStatus struct {
	Supported bool   `json:"supported"`      // 当前平台是否支持
	Enabled   bool   `json:"enabled"`        // 系统侧已注册且指向当前可执行文件
	Note      string `json:"note,omitempty"` // 补充说明（不支持原因等）
}

// AppExecutablePath 当前可执行文件绝对路径（解析符号链接）
func AppExecutablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Abs(exe)
}

// SetAutoStart 注册 / 注销开机自启（下次登录系统时生效）
func SetAutoStart(on bool) error {
	switch runtime.GOOS {
	case "darwin", "windows", "linux":
		return setAutoStartPlatform(on)
	default:
		return fmt.Errorf("平台 %s 暂不支持开机自启", runtime.GOOS)
	}
}

// AutoStartState 读取系统侧真实注册状态（出错按未注册处理并附说明）
func AutoStartState() AutoStartStatus {
	enabled, note, err := autoStartStatePlatform()
	if err != nil {
		return AutoStartStatus{Supported: true, Enabled: false, Note: "读取自启状态失败：" + err.Error()}
	}
	return AutoStartStatus{Supported: true, Enabled: enabled, Note: note}
}

// macOS 场景：可执行文件位于 .app 包内时返回包路径（/Applications/BuddyBot.app），否则空串
func appBundlePath(exe string) string {
	const marker = "/Contents/MacOS"
	i := strings.Index(exe, marker)
	if i <= 0 {
		return ""
	}
	bundle := exe[:i]
	if strings.EqualFold(filepath.Ext(bundle), ".app") {
		return bundle
	}
	return ""
}
