package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ============================================================
// 账号数据迁移 / 恢复（对齐 workbuddy-account-migrate 的能力面）
//
// WorkBuddy 客户端数据按 user_id 隔离：切换登录账号后，旧账号的 Session
// 历史、长期记忆、Connectors 在新账号下不可见。本模块提供：
//   1. 诊断：列出各 user_id 的数据分布与当前登录身份（优先 DB 最新 session，
//      而非 storage.json——后者在切换后可能未同步）；
//   2. 迁移：备份 → WAL checkpoint → sessions.user_id 归并 → 记忆追加去重
//      合并 → Connectors 增量合并 → 校验；
//   3. 回滚：按备份标签整体还原。
//
// SQLite 访问走系统 sqlite3 CLI（macOS/Linux 自带）：不为此引入 CGO 或纯 Go
// 驱动的大依赖；CLI 缺失时明确报错。
// ============================================================

// dataMigrationHome 客户端数据目录（env 可覆盖，测试用）。
func dataMigrationHome() string {
	if v := strings.TrimSpace(os.Getenv("WORKBUDDY_DATA_DIR")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".workbuddy"
	}
	return filepath.Join(home, ".workbuddy")
}

// migrationSQLite3 调 sqlite3 CLI 执行 SQL；-json 模式返回行记录。
func migrationSQLite3(db, sql string) (string, error) {
	sqlite3, err := exec.LookPath("sqlite3")
	if err != nil {
		return "", fmt.Errorf("未找到 sqlite3 命令行工具（迁移功能依赖它）：请安装 sqlite3 后重试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sqlite3, "-json", db, sql)
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("sqlite3 执行失败: %w（stderr: %s）", err, stderr)
	}
	return string(out), nil
}

// UserFootprint 单个 user_id 在本机的数据分布。
type UserFootprint struct {
	UID        string `json:"uid"`
	Sessions   int    `json:"sessions"`
	Memory     bool   `json:"memory"`
	Connectors bool   `json:"connectors"`
}

// DataMigrationDiag 诊断结果。
type DataMigrationDiag struct {
	CurrentUID string          `json:"currentUid"` // 当前登录（DB 最新 session 推断，可能为空）
	Users      []UserFootprint `json:"users"`
	BackupDir  string          `json:"backupDir"`
	DBPath     string          `json:"dbPath"`
}

// DiagnoseDataMigration 诊断：当前登录身份 + 各 user_id 的数据分布。
func DiagnoseDataMigration() (*DataMigrationDiag, error) {
	home := dataMigrationHome()
	db := filepath.Join(home, "workbuddy.db")
	diag := &DataMigrationDiag{BackupDir: filepath.Join(home, "backups"), DBPath: db}

	if _, err := os.Stat(db); err != nil {
		return nil, fmt.Errorf("未找到客户端数据库 %s（未安装或未登录过客户端）", db)
	}
	// 当前登录：最新创建的 session 的 user_id（storage.json 的 genie.userId
	// 在账号切换后可能未同步，因此不作为首选依据）
	if rows, err := migrationSQLite3(db,
		"SELECT user_id FROM sessions ORDER BY created_at DESC LIMIT 1"); err == nil {
		diag.CurrentUID = jsonFirstString(rows, "user_id")
	}
	counts := map[string]int{}
	if rows, err := migrationSQLite3(db,
		"SELECT user_id, COUNT(*) AS n FROM sessions GROUP BY user_id"); err == nil {
		for _, r := range jsonRows(rows) {
			uid, _ := r["user_id"].(string)
			n, _ := r["n"].(float64)
			if uid != "" {
				counts[uid] = int(n)
			}
		}
	}
	uidSet := map[string]bool{}
	for uid := range counts {
		uidSet[uid] = true
	}
	// 记忆与 Connectors 的目录侧扫描
	memFiles, _ := filepath.Glob(filepath.Join(home, "memory", "*_memory.md"))
	for _, f := range memFiles {
		base := filepath.Base(f)
		if uid := strings.TrimSuffix(strings.TrimSuffix(base, "_memory.md"), ".md"); uid != "" {
			uidSet[uid] = true
		}
	}
	connBase := filepath.Join(home, "connectors")
	if entries, err := os.ReadDir(connBase); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				uidSet[e.Name()] = true
			}
		}
	}
	for uid := range uidSet {
		fp := UserFootprint{UID: uid, Sessions: counts[uid]}
		if _, err := os.Stat(filepath.Join(home, "memory", uid+"_memory.md")); err == nil {
			fp.Memory = true
		}
		if st, err := os.Stat(filepath.Join(connBase, uid)); err == nil && st.IsDir() {
			fp.Connectors = true
		}
		diag.Users = append(diag.Users, fp)
	}
	sort.Slice(diag.Users, func(i, j int) bool { return diag.Users[i].Sessions > diag.Users[j].Sessions })
	return diag, nil
}

// DataMigrationResult 迁移执行结果。
type DataMigrationResult struct {
	BackupTag       string `json:"backupTag"`
	SessionsMoved   int    `json:"sessionsMoved"`
	MemoryAppended  int    `json:"memoryAppended"`  // 追加的记忆行数（0 = 无新增或无源文件）
	ConnectorCopied int    `json:"connectorCopied"` // 增量拷贝的 Connector 文件数
	Note            string `json:"note,omitempty"`
}

// RunDataMigration 执行迁移：source 账号的数据并入 target（合并而非覆盖）。
// 备份目录 <data>/backups/migrate-<tag>/，可用 RollbackDataMigration 还原。
func RunDataMigration(source, target string) (*DataMigrationResult, error) {
	source, target = strings.TrimSpace(source), strings.TrimSpace(target)
	if source == "" || target == "" {
		return nil, fmt.Errorf("必须指定源账号与目标账号")
	}
	if source == target {
		return nil, fmt.Errorf("源账号与目标账号相同，无需迁移")
	}
	home := dataMigrationHome()
	db := filepath.Join(home, "workbuddy.db")
	if _, err := os.Stat(db); err != nil {
		return nil, fmt.Errorf("未找到客户端数据库 %s", db)
	}

	res := &DataMigrationResult{}
	res.BackupTag = "migrate-" + time.Now().Format("20060102-150405")
	backupDir := filepath.Join(home, "backups", res.BackupTag)

	// ---- Phase 1: 备份（db + 目标记忆 + 目标 Connectors） ----
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := db + suffix
		if _, err := os.Stat(p); err == nil {
			if err := copyFile(p, filepath.Join(backupDir, "workbuddy.db"+suffix)); err != nil {
				return nil, fmt.Errorf("备份数据库失败: %w", err)
			}
		}
	}
	dstMem := filepath.Join(home, "memory", target+"_memory.md")
	if _, err := os.Stat(dstMem); err == nil {
		_ = copyFile(dstMem, filepath.Join(backupDir, target+"_memory.md"))
	}
	dstConn := filepath.Join(home, "connectors", target)
	if st, err := os.Stat(dstConn); err == nil && st.IsDir() {
		_ = copyDir(dstConn, filepath.Join(backupDir, "connectors-"+target))
	}

	// ---- Phase 2: sessions 归并（WAL checkpoint 前置 + 迁移 + 校验） ----
	if _, err := migrationSQLite3(db, "PRAGMA wal_checkpoint(TRUNCATE);"); err != nil {
		return nil, fmt.Errorf("WAL checkpoint 失败: %w", err)
	}
	// 单引号注入防御：user_id 只允许安全字符
	for _, uid := range []string{source, target} {
		if !safeUserID(uid) {
			return nil, fmt.Errorf("非法账号 ID: %q", uid)
		}
	}
	out, err := migrationSQLite3(db,
		"UPDATE sessions SET user_id = '"+target+"' WHERE user_id = '"+source+"'; SELECT changes();")
	if err != nil {
		return nil, fmt.Errorf("session 归并失败: %w", err)
	}
	res.SessionsMoved = jsonChangesCount(out)
	// 迁移后 checkpoint 落盘 + 校验源归零
	_, _ = migrationSQLite3(db, "PRAGMA wal_checkpoint(TRUNCATE);")
	if rows, err := migrationSQLite3(db,
		"SELECT COUNT(*) AS n FROM sessions WHERE user_id = '"+source+"';"); err == nil {
		if n := jsonFirstInt(rows, "n"); n > 0 {
			res.Note = fmt.Sprintf("警告：源账号仍有 %d 个 session 未迁移", n)
		}
	}

	// ---- Phase 3: 记忆追加去重合并 ----
	srcMem := filepath.Join(home, "memory", source+"_memory.md")
	if srcRaw, err := os.ReadFile(srcMem); err == nil {
		var dstRaw []byte
		if r, err := os.ReadFile(dstMem); err == nil {
			dstRaw = r
		}
		dstLines := map[string]bool{}
		for _, line := range strings.Split(string(dstRaw), "\n") {
			dstLines[strings.TrimRight(line, "\r")] = true
		}
		var fresh []string
		for _, line := range strings.Split(string(srcRaw), "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" || dstLines[line] {
				continue
			}
			fresh = append(fresh, line)
			dstLines[line] = true
		}
		if len(fresh) > 0 {
			var sb strings.Builder
			if len(dstRaw) > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString("## Migrated from " + source + "\n\n")
			sb.WriteString(strings.Join(fresh, "\n"))
			f, err := os.OpenFile(dstMem, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				res.Note = joinNote(res.Note, "记忆合并失败: "+err.Error())
			} else {
				_, _ = f.WriteString(sb.String())
				_ = f.Close()
				res.MemoryAppended = len(fresh)
			}
		}
	}

	// ---- Phase 4: Connectors 增量合并（目标缺的才拷） ----
	srcConn := filepath.Join(home, "connectors", source)
	if st, err := os.Stat(srcConn); err == nil && st.IsDir() {
		copied, err := mergeDir(srcConn, dstConn)
		if err != nil {
			res.Note = joinNote(res.Note, "Connectors 合并失败: "+err.Error())
		}
		res.ConnectorCopied = copied
	}
	return res, nil
}

// RollbackDataMigration 按备份标签整体还原（db / 记忆 / Connectors）。
func RollbackDataMigration(tag string) error {
	tag = strings.TrimSpace(tag)
	if tag == "" || strings.ContainsAny(tag, "/\\.") || !strings.HasPrefix(tag, "migrate-") {
		return fmt.Errorf("非法备份标签")
	}
	backupDir := filepath.Join(dataMigrationHome(), "backups", tag)
	if st, err := os.Stat(backupDir); err != nil || !st.IsDir() {
		return fmt.Errorf("备份不存在: %s", backupDir)
	}
	home := dataMigrationHome()
	db := filepath.Join(home, "workbuddy.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		bak := filepath.Join(backupDir, "workbuddy.db"+suffix)
		if _, err := os.Stat(bak); err == nil {
			if err := copyFile(bak, db+suffix); err != nil {
				return fmt.Errorf("还原数据库失败: %w", err)
			}
		} else if suffix == "" {
			return fmt.Errorf("备份中缺少 workbuddy.db，拒绝半还原")
		}
	}
	// 记忆与 Connectors 还原：备份里有哪个 target 就还原哪个
	mems, _ := filepath.Glob(filepath.Join(backupDir, "*_memory.md"))
	for _, m := range mems {
		if err := copyFile(m, filepath.Join(home, "memory", filepath.Base(m))); err != nil {
			return err
		}
	}
	connBak := filepath.Join(backupDir, "connectors-")
	if matches, _ := filepath.Glob(connBak + "*"); len(matches) > 0 {
		for _, src := range matches {
			uid := strings.TrimPrefix(filepath.Base(src), "connectors-")
			if err := os.RemoveAll(filepath.Join(home, "connectors", uid)); err != nil {
				return err
			}
			if err := copyDir(src, filepath.Join(home, "connectors", uid)); err != nil {
				return err
			}
		}
	}
	return nil
}

// ListDataMigrationBackups 列出可用备份标签（迁移前展示给用户）。
func ListDataMigrationBackups() ([]string, error) {
	dir := filepath.Join(dataMigrationHome(), "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil // 无备份目录属正常态
	}
	var tags []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "migrate-") {
			tags = append(tags, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(tags)))
	return tags, nil
}

// ---- 小工具 ----

func safeUserID(uid string) bool {
	if uid == "" || len(uid) > 128 {
		return false
	}
	for _, r := range uid {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func copyFile(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, raw, 0o600)
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

// mergeDir 增量合并：仅拷贝目标中不存在的文件，返回拷贝数。
func mergeDir(src, dst string) (int, error) {
	copied := 0
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if _, err := os.Stat(target); err == nil {
			return nil // 目标已存在：保留目标版本
		}
		if err := copyFile(path, target); err != nil {
			return err
		}
		copied++
		return nil
	})
	return copied, err
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + "；" + b
}

// jsonRows 解析 sqlite3 -json 输出（空输出 → 空切片）。
func jsonRows(out string) []map[string]any {
	var rows []map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &rows) != nil {
		return nil
	}
	return rows
}

func jsonFirstString(out, key string) string {
	if rows := jsonRows(out); len(rows) > 0 {
		v, _ := rows[0][key].(string)
		return v
	}
	return ""
}

func jsonFirstInt(out, key string) int {
	if rows := jsonRows(out); len(rows) > 0 {
		if v, ok := rows[0][key].(float64); ok {
			return int(v)
		}
	}
	return 0
}

func jsonChangesCount(out string) int {
	// SELECT changes() 的 -json 输出形如 [{"changes()":N}]
	if rows := jsonRows(out); len(rows) > 0 {
		for _, v := range rows[0] {
			if n, ok := v.(float64); ok {
				return int(n)
			}
		}
	}
	return 0
}
