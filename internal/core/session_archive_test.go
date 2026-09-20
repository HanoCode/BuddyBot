package core

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newSessionsFixture 建一个最小 sessions 表（与 workbuddy.db 的相关列同名同语义），
// 返回库文件路径。
func newSessionsFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workbuddy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY, status TEXT NOT NULL, updated_at INTEGER,
		deleted_at INTEGER, is_background_automation INTEGER, last_activity_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	return path
}

// insertRow 插入一条会话记录（deletedAt/bg/lastActivity 用 nil 模拟 SQL NULL）。
func insertRow(t *testing.T, db *sql.DB, id, status string, deletedAt, bg, lastActivity any) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO sessions (id, status, updated_at, deleted_at, is_background_automation, last_activity_at)
		VALUES (?, ?, 0, ?, ?, ?)`, id, status, deletedAt, bg, lastActivity); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveIdleSessionsAt(t *testing.T) {
	path := newSessionsFixture(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	stale := time.Now().AddDate(0, 0, -10).UnixMilli()   // 10 天前
	fresh := time.Now().Add(-24 * time.Hour).UnixMilli() // 1 天前
	insertRow(t, db, "s1", "completed", nil, 0, stale)                   // 应归档：已结束且空闲超阈值
	insertRow(t, db, "s2", "terminated", nil, 0, stale)                  // 应归档
	insertRow(t, db, "s3", "working", nil, 0, stale)                     // 不归档：进行中
	insertRow(t, db, "s4", "completed", nil, 0, fresh)                   // 不归档：空闲不足
	insertRow(t, db, "s5", "completed", -1, 0, stale)                    // 应归档：cloud 未删除（deleted_at=-1）
	insertRow(t, db, "s6", "completed", time.Now().UnixMilli(), 0, stale) // 不归档：已软删除
	insertRow(t, db, "s7", "completed", nil, 1, stale)                   // 不归档：后台自动化
	insertRow(t, db, "s8", "completed", nil, 0, nil)                     // 不归档：无活跃时间
	insertRow(t, db, "s9", "archived", nil, 0, stale)                    // 不重复动：已是归档态

	n, err := archiveIdleSessionsAt(path, 7)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("期望归档 3 条（s1/s2/s5），实际 %d", n)
	}
	rows, err := db.Query(`SELECT id, status FROM sessions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			t.Fatal(err)
		}
		got[id] = status
	}
	for id, want := range map[string]string{
		"s1": "archived", "s2": "archived", "s5": "archived",
		"s3": "working", "s4": "completed", "s6": "completed",
		"s7": "completed", "s8": "completed", "s9": "archived",
	} {
		if got[id] != want {
			t.Errorf("s=%s 期望 status=%s，实际 %s", id, want, got[id])
		}
	}
}

func TestArchiveIdleSessionsAtMissingDB(t *testing.T) {
	// 数据库文件不存在时不隐式建库：直接报错（外层 ArchiveIdleSessions 先 stat 跳过）
	if _, err := archiveIdleSessionsAt(filepath.Join(t.TempDir(), "nope.db"), 7); err == nil {
		t.Fatal("期望对不存在的库报错")
	}
}

func TestSessionArchiveIdleDaysResolved(t *testing.T) {
	if got := (SessionArchiveConfig{}).IdleDaysResolved(); got != DefaultSessionArchiveIdleDays {
		t.Fatalf("零值配置应回落默认 %d，实际 %d", DefaultSessionArchiveIdleDays, got)
	}
	if got := (SessionArchiveConfig{IdleDays: 3}).IdleDaysResolved(); got != 3 {
		t.Fatalf("显式配置应生效，实际 %d", got)
	}
}

func TestArchiveIdleSessionsSkipsMissingDB(t *testing.T) {
	// 本机无 WorkBuddy 数据目录时应静默跳过（返回 0，nil）
	if _, err := os.Stat(workbuddyDBPath()); err == nil {
		t.Skip("本机存在真实 workbuddy.db，跳过缺库分支测试")
	}
	n, err := ArchiveIdleSessions(NewServiceAt(t.TempDir()))
	if err != nil || n != 0 {
		t.Fatalf("期望 (0, nil)，实际 (%d, %v)", n, err)
	}
}
