//go:build unix

package core

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ============================================================
// 开机自启 · macOS LaunchAgent / Linux XDG autostart
//
// macOS：写 ~/Library/LaunchAgents/com.buddybot.desktop.plist（RunAtLoad）。
//   .app 包内运行时经 /usr/bin/open -a <bundle> 启动（走 LaunchServices，
//   Dock 图标与激活策略正常）；裸二进制（开发态）直接指向可执行文件。
// Linux：写 ~/.config/autostart/buddybot.desktop（XDG 标准入口）。
// ============================================================

const launchAgentLabel = "com.buddybot.desktop"

func launchAgentPlistPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
}

func xdgAutostartPath() string {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cfg, "autostart", "buddybot.desktop")
}

// autoStartArgs 本次可执行文件对应的启动参数（包内走 open -a，裸二进制直指）
func autoStartArgs() ([]string, error) {
	exe, err := AppExecutablePath()
	if err != nil {
		return nil, err
	}
	if bundle := appBundlePath(exe); bundle != "" {
		return []string{"/usr/bin/open", "-a", bundle}, nil
	}
	return []string{exe}, nil
}

func setAutoStartPlatform(on bool) error {
	switch runtime.GOOS {
	case "darwin":
		if launchAgentPlistPath() == "" {
			return fmt.Errorf("无法定位用户主目录")
		}
		return setAutoStartDarwin(on)
	case "linux":
		if xdgAutostartPath() == "" {
			return fmt.Errorf("无法定位用户配置目录")
		}
		return setAutoStartLinux(on)
	default:
		return fmt.Errorf("平台 %s 暂不支持开机自启", runtime.GOOS)
	}
}

func setAutoStartDarwin(on bool) error {
	path := launchAgentPlistPath()
	if !on {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	args, err := autoStartArgs()
	if err != nil {
		return err
	}
	esc := func(s string) string {
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "<string>" + esc(a) + "</string>"
	}
	content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		%s
	</array>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>
`, launchAgentLabel, strings.Join(quoted, "\n\t\t"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func setAutoStartLinux(on bool) error {
	path := xdgAutostartPath()
	if !on {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	exe, err := AppExecutablePath()
	if err != nil {
		return err
	}
	content := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=BuddyBot
Exec=%q
X-GNOME-Autostart-enabled=true
`, exe)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func autoStartStatePlatform() (enabled bool, note string, err error) {
	var path string
	switch runtime.GOOS {
	case "darwin":
		path = launchAgentPlistPath()
	case "linux":
		path = xdgAutostartPath()
	default:
		return false, "平台 " + runtime.GOOS + " 暂不支持开机自启", nil
	}
	if path == "" {
		return false, "无法定位用户主目录", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, "", nil
		}
		return false, "", err
	}
	args, err := autoStartArgs()
	if err != nil {
		return false, "", err
	}
	// 已注册但指向的可执行文件已变化（移动/重装）：视为未生效，重新开启即可修复
	want := filepath.Base(args[len(args)-1])
	if !strings.Contains(string(b), want) {
		return false, "已注册但指向的程序路径已变化，重新开启即可修复", nil
	}
	return true, "", nil
}
