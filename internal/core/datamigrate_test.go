package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupMigrateHome 构造假的客户端数据目录：db（sessions 表）+ 记忆 + Connectors。
func setupMigrateHome(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("系统未安装 sqlite3，跳过迁移测试")
	}
	home := t.TempDir()
	t.Setenv("WORKBUDDY_DATA_DIR", home)
	db := filepath.Join(home, "workbuddy.db")
	sql := "CREATE TABLE sessions (id INTEGER PRIMARY KEY, user_id TEXT, created_at TEXT);" +
		"INSERT INTO sessions (user_id, created_at) VALUES ('old_user','2026-09-01'),('old_user','2026-09-02')," +
		"('old_user','2026-09-03'),('new_user','2026-09-04');"
	if out, err := exec.Command("sqlite3", db, sql).CombinedOutput(); err != nil {
		t.Fatalf("构造测试库失败: %v (%s)", err, out)
	}
	memDir := filepath.Join(home, "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "old_user_memory.md"),
		[]byte("旧账号记忆 A\n旧账号记忆 B\n共享行\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "new_user_memory.md"),
		[]byte("新账号记忆\n共享行\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	connSrc := filepath.Join(home, "connectors", "old_user", "mail")
	if err := os.MkdirAll(connSrc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(connSrc, "config.json"), []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	connDst := filepath.Join(home, "connectors", "new_user", "calendar")
	if err := os.MkdirAll(connDst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(connDst, "keep.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestDiagnoseDataMigration(t *testing.T) {
	home := setupMigrateHome(t)
	diag, err := DiagnoseDataMigration()
	if err != nil {
		t.Fatalf("诊断失败: %v", err)
	}
	if diag.CurrentUID != "new_user" {
		t.Fatalf("当前登录 = %q, want new_user（DB 最新 session 推断）", diag.CurrentUID)
	}
	byUID := map[string]UserFootprint{}
	for _, u := range diag.Users {
		byUID[u.UID] = u
	}
	old, ok := byUID["old_user"]
	if !ok || old.Sessions != 3 || !old.Memory || !old.Connectors {
		t.Fatalf("old_user 分布异常: %+v", old)
	}
	if _, ok := byUID["new_user"]; !ok {
		t.Fatal("new_user 未被诊断到")
	}
	_ = home
}

func TestRunDataMigrationAndRollback(t *testing.T) {
	home := setupMigrateHome(t)
	res, err := RunDataMigration("old_user", "new_user")
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if res.SessionsMoved != 3 {
		t.Fatalf("session 迁移数 = %d, want 3", res.SessionsMoved)
	}
	if res.MemoryAppended != 2 {
		t.Fatalf("记忆追加行数 = %d, want 2（共享行去重）", res.MemoryAppended)
	}
	if res.ConnectorCopied != 1 {
		t.Fatalf("Connector 拷贝数 = %d, want 1（目标已有的保留）", res.ConnectorCopied)
	}
	// 校验：源归零、记忆合并、Connector 增量存在
	out, err := exec.Command("sqlite3", filepath.Join(home, "workbuddy.db"),
		"SELECT COUNT(*) FROM sessions WHERE user_id='old_user';").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "0" {
		t.Fatalf("源账号 session 未归零: %s (%v)", out, err)
	}
	mem, _ := os.ReadFile(filepath.Join(home, "memory", "new_user_memory.md"))
	for _, want := range []string{"新账号记忆", "共享行", "旧账号记忆 A", "Migrated from old_user"} {
		if !strings.Contains(string(mem), want) {
			t.Fatalf("合并后记忆缺少 %q:\n%s", want, mem)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "connectors", "new_user", "mail", "config.json")); err != nil {
		t.Fatal("Connector 增量拷贝缺失")
	}
	if _, err := os.Stat(filepath.Join(home, "connectors", "new_user", "calendar", "keep.json")); err != nil {
		t.Fatal("目标 Connector 被误删")
	}

	// 回滚：sessions 还原为迁移前分布
	if err := RollbackDataMigration(res.BackupTag); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	out, err = exec.Command("sqlite3", filepath.Join(home, "workbuddy.db"),
		"SELECT COUNT(*) FROM sessions WHERE user_id='old_user';").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "3" {
		t.Fatalf("回滚后源账号 session 应为 3: %s (%v)", out, err)
	}
}

func TestRunDataMigrationRejectsSameAndUnsafe(t *testing.T) {
	setupMigrateHome(t)
	if _, err := RunDataMigration("same", "same"); err == nil {
		t.Fatal("同账号迁移应被拒绝")
	}
	if _, err := RunDataMigration("o'brien", "x"); err == nil {
		t.Fatal("含引号的非法 ID 应被拒绝")
	}
}
