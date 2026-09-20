//go:build !windows

package core

import (
	"os"
	"path/filepath"
	"syscall"
)

// 单实例保护（macOS/Linux）：数据目录锁文件 + flock。
// macOS 上关窗口隐藏到托盘，重复启动同样会造成网关端口冲突，行为与 Windows 一致。
func AcquireSingleInstance() (ok bool) {
	// 锁文件必须与 NewService 用同一个数据目录（EnsureAppDir 同一入口，顺带完成旧目录迁移）
	path := filepath.Join(EnsureAppDir(), "buddybot.lock")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return true
	}
	// LOCK_EX|LOCK_NB：非阻塞独占；进程退出时内核自动释放，无需显式解锁
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return false
	}
	return true // 持有锁文件引用不关闭，进程存活期间锁一直生效
}
