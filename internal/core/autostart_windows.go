//go:build windows

package core

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// ============================================================
// 开机自启 · Windows 注册表 HKCU Run 键（登录时由资源管理器拉起，无需提权）
// ============================================================

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

const autoStartValueName = "BuddyBot"

func setAutoStartPlatform(on bool) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("打开注册表 Run 键失败: %w", err)
	}
	defer key.Close()

	if !on {
		// 不存在的值删除会报错，视为已注销
		if err := key.DeleteValue(autoStartValueName); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := AppExecutablePath()
	if err != nil {
		return err
	}
	// 路径带空格必须加引号，否则登录启动时会被截断
	return key.SetStringValue(autoStartValueName, `"`+exe+`"`)
}

func autoStartStatePlatform() (enabled bool, note string, err error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false, "", fmt.Errorf("打开注册表 Run 键失败: %w", err)
	}
	defer key.Close()

	val, _, err := key.GetStringValue(autoStartValueName)
	if err != nil {
		if err == registry.ErrNotExist {
			return false, "", nil
		}
		return false, "", err
	}
	exe, err := AppExecutablePath()
	if err != nil {
		return false, "", err
	}
	if val != `"`+exe+`"` {
		return false, "已注册但指向的程序路径已变化，重新开启即可修复", nil
	}
	return true, "", nil
}
