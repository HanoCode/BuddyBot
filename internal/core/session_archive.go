package core

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（无 CGO），注册 "sqlite" 方言
)

// ============================================================
// 会话自动归档：把 WorkBuddy（官方客户端）中空闲超过阈值的已结束会话
// 标记为 archived（与客户端内手动归档同一状态机，可手动取消归档）。
//
// 数据源：~/.workbuddy/workbuddy.db 的 sessions 表（SQLite，WAL 模式，
// 与 WorkBuddy 主进程并发读写安全）。UPDATE 只改 status/updated_at 两个
// 字段，避免整行覆盖与客户端内存态互相踩踏。
// ============================================================

// DefaultSessionArchiveIdleDays 默认空闲阈值（天）
const DefaultSessionArchiveIdleDays = 7

// SessionArchiveConfig 会话自动归档配置
type SessionArchiveConfig struct {
	Enabled bool `json:"enabled"`
	// IdleDays 空闲阈值（天）：last_activity_at 距今超过该天数才归档；0 = 默认 7 天
	IdleDays int `json:"idle_days"`
}

// IdleDaysResolved 空闲阈值（零值回落默认）
func (c SessionArchiveConfig) IdleDaysResolved() int {
	if c.IdleDays <= 0 {
		return DefaultSessionArchiveIdleDays
	}
	return c.IdleDays
}

// workbuddyDBPath WorkBuddy 客户端本地数据库（会话表所在）
func workbuddyDBPath() string {
	return filepath.Join(agentHome(), ".workbuddy", "workbuddy.db")
}

// ArchiveIdleSessions 执行一轮归档，返回本次归档的会话数。
// 规则：status 为 completed/terminated（不含 working 等在途状态）、
// 未删除、非后台自动化、last_activity_at 超过空闲阈值的会话置为 archived。
// 数据库不存在（本机未装 WorkBuddy 客户端）时静默跳过，返回 0 与 nil。
func ArchiveIdleSessions(svc *Service) (int64, error) {
	dbPath := workbuddyDBPath()
	if _, err := os.Stat(dbPath); err != nil {
		return 0, nil // 客户端未安装或数据目录不存在：无事可做，不算错误
	}
	idleDays := svc.GetConfig().Schedule.SessionArchive.IdleDaysResolved()
	n, err := archiveIdleSessionsAt(dbPath, idleDays)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		svc.Store().Audit("session.archive", "", fmt.Sprintf("idle_days=%d archived=%d", idleDays, n))
	}
	return n, nil
}

// archiveIdleSessionsAt 对指定会话库执行归档 UPDATE（路径参数化，测试可指向临时库）。
func archiveIdleSessionsAt(dbPath string, idleDays int) (int64, error) {
	// mode=rw：不隐式建库；busy_timeout：与 WorkBuddy 主进程并发写时等锁
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=rw&_pragma=busy_timeout(3000)")
	if err != nil {
		return 0, fmt.Errorf("打开 WorkBuddy 会话库失败: %w", err)
	}
	defer db.Close()

	cutoff := time.Now().AddDate(0, 0, -idleDays).UnixMilli()
	res, err := db.Exec(`
UPDATE sessions SET status = 'archived', updated_at = ?
WHERE status IN ('completed', 'terminated')
  AND (deleted_at IS NULL OR deleted_at = -1)
  AND (is_background_automation IS NULL OR is_background_automation = 0)
  AND last_activity_at IS NOT NULL AND last_activity_at > 0
  AND last_activity_at <= ?`, time.Now().UnixMilli(), cutoff)
	if err != nil {
		return 0, fmt.Errorf("归档会话失败: %w", err)
	}
	return res.RowsAffected()
}
