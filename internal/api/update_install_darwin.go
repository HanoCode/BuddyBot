//go:build darwin

package api

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// installMacOS 解压 .app zip 并安排退出后替换 /Applications 内的 bundle。
// 流程：主进程启动 detached helper（sh 脚本）→ 主进程退出 →
// helper 备份旧 bundle → ditto 新 bundle → 重新拉起 → 清理备份。
func installUpdate(zipPath string, _ bool) error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	bundle, ok := macAppBundle(exePath)
	if !ok {
		return fmt.Errorf("当前以开发模式运行（不在 .app 内），请在安装版中更新")
	}

	// 解压到临时目录，找到新 bundle
	tmp, err := os.MkdirTemp("", "buddybot-update-")
	if err != nil {
		return err
	}
	if out, err := exec.Command("/usr/bin/ditto", "-x", "-k", zipPath, tmp).CombinedOutput(); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("解压更新包失败: %v: %s", err, out)
	}
	newApp, err := findAppBundle(tmp)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}

	// helper 脚本：通过环境变量传路径，避免引号/空格转义问题
	script := `#!/bin/sh
sleep 2
rm -rf "$TARGET.old"
mv "$TARGET" "$TARGET.old" || exit 1
if ditto "$NEWAPP" "$TARGET"; then
  open "$TARGET"
  rm -rf "$TARGET.old" "$WORK"
else
  mv "$TARGET.old" "$TARGET"
  rm -rf "$WORK"
fi
rm -f "$SELF"
`
	scriptPath := filepath.Join(tmp, "apply-update.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	cmd := exec.Command("/bin/sh", scriptPath)
	cmd.Env = append(os.Environ(),
		"TARGET="+bundle,
		"NEWAPP="+newApp,
		"WORK="+tmp,
		"SELF="+scriptPath,
	)
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("启动更新 helper 失败: %w", err)
	}
	// helper 已独立运行，主进程退出后它继续完成替换
	quitApp()
	return nil
}

// macAppBundle 从可执行文件路径推算 .app bundle 根目录；
// 不在 .app 内（go run / 裸二进制）返回 false。
func macAppBundle(exePath string) (string, bool) {
	idx := strings.Index(exePath, ".app/")
	if idx < 0 || !strings.Contains(exePath, "/Contents/MacOS/") {
		return "", false
	}
	return exePath[:idx+len(".app")], true
}

// findAppBundle 在解压目录中定位新 .app bundle。
func findAppBundle(root string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if found == "" && d.IsDir() && strings.HasSuffix(d.Name(), ".app") {
			found = path
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("扫描更新包内容失败: %w", err)
	}
	if found == "" {
		return "", fmt.Errorf("更新包内未找到 .app bundle")
	}
	return found, nil
}
