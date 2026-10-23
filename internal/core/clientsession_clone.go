package core

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ============================================================
// 会话克隆（切换账号时把当前账号的会话复制一份给目标账号）
//
// 思路对齐 workbuddy-switch 的 copy_session_to_user（路径 B：生成新 id）：
//   1) 正文：~/.workbuddy/projects/{workspace}/{cid}.jsonl → {newCID}.jsonl
//      （正文里引用旧 id 的地方一并替换，官方按 jsonl 内的 sessionId 校验）
//   2) 元数据：workbuddy.db 的 sessions 表复制一行（新 id + 目标 user_id + 新时间戳）
//   3) 云端映射：edge-sync-mapping 库登记新会话归属目标账号
//      （msg_channel = convmsg:{uid}，缺这条云端同步会把会话算回源账号）
//
// 为什么克隆而不是改归属：改 user_id 会把源账号的会话整批搬走，切回去就没了；
// 克隆保持源账号原样，只给目标账号一份新 id 的副本，来回切换都不丢。
//
// Claw 工作区排除：那是绑定当前账号 IM 渠道的工作区，复制行给目标账号也用不了。
// 写前必须备份：先 VACUUM INTO 出会话库快照，出错可人工还原。
// ============================================================

// workbuddyRoot 客户端数据根（projects/ 与 workbuddy.db 同在）
func workbuddyRoot() string {
	return filepath.Join(agentHome(), ".workbuddy")
}

// clientSessionCloneSupportedAt 会话克隆能力探测：projects/ 目录 + workbuddy.db 的
// sessions 表同时存在才算可用。缺 projects/ 时只能复制出行而没有正文（半成品会话），
// 宁可明确拒绝。
func clientSessionCloneSupportedAt(root string) bool {
	if st, err := os.Stat(filepath.Join(root, "projects")); err != nil || !st.IsDir() {
		return false
	}
	dbPath := filepath.Join(root, "workbuddy.db")
	if !fileExists(dbPath) {
		return false
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return false
	}
	defer db.Close()
	return tableExistsDB(db, "sessions")
}

func tableExistsDB(db *sql.DB, name string) bool {
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

func columnExistsDB(db *sql.DB, table, column string) bool {
	rows, err := db.Query(`PRAGMA table_info(` + quoteIdent(table) + `)`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    any
			pk      int
		)
		if rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk) != nil {
			return false
		}
		if name == column {
			return true
		}
	}
	return false
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// isClawWorkspace Claw 是账号绑定的 IM 渠道工作区，复制会话行过去目标账号也用不了。
func isClawWorkspace(cwd string) bool {
	clean := strings.TrimRight(strings.TrimSpace(cwd), `/\`)
	if clean == "" {
		return false
	}
	base := clean
	if i := strings.LastIndexAny(clean, `/\`); i >= 0 {
		base = clean[i+1:]
	}
	return strings.EqualFold(base, "claw")
}

// findProjectJSONL 在 projects/ 下定位会话正文（官方布局是
// projects/{workspace}/{cid}.jsonl，也兼容直接放 projects/{cid}.jsonl）。
func findProjectJSONL(root, cid string) (string, bool) {
	projects := filepath.Join(root, "projects")
	if direct := filepath.Join(projects, cid+".jsonl"); fileExists(direct) {
		return direct, true
	}
	matches, _ := filepath.Glob(filepath.Join(projects, "*", cid+".jsonl"))
	if len(matches) > 0 {
		return matches[0], true
	}
	return "", false
}

// cloneClientSessions 把 fromUID 名下的本地会话克隆给 toUID，源会话原样保留。
func (s *Service) cloneClientSessions(fromUID, toUID string) (int, error) {
	return cloneClientSessionsAt(workbuddyRoot(), s.clientSwitchBackupDir(), fromUID, toUID)
}

// cloneClientSessionsAt 克隆实现（数据根参数化，便于用临时目录做单测）。
func cloneClientSessionsAt(root, backupDir, fromUID, toUID string) (int, error) {
	if fromUID == "" || toUID == "" || fromUID == toUID {
		return 0, nil
	}
	dbPath := filepath.Join(root, "workbuddy.db")
	if !fileExists(dbPath) {
		return 0, nil // 本机没有客户端会话库：无事可做
	}
	if !clientSessionCloneSupportedAt(root) {
		return 0, fmt.Errorf("客户端数据目录缺少 projects/ 或 sessions 表，无法克隆会话")
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return 0, fmt.Errorf("创建备份目录失败: %w", err)
	}
	snapshot := filepath.Join(backupDir, fmt.Sprintf("sessions-%s.db", time.Now().Format("20060102-150405")))

	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=rw&_pragma=busy_timeout(5000)")
	if err != nil {
		return 0, fmt.Errorf("打开会话库失败: %w", err)
	}
	defer db.Close()

	// VACUUM INTO 不接受绑定参数（表达式需字面量）：转义单引号后内联
	if _, err := db.Exec("VACUUM INTO '" + strings.ReplaceAll(snapshot, "'", "''") + "'"); err != nil {
		return 0, fmt.Errorf("备份会话库失败: %w", err)
	}

	// 只克隆本地会话（云端会话归服务端，复制过去会产生两份互相打架的记录）
	selectCols := "id"
	if columnExistsDB(db, "sessions", "cwd") {
		selectCols += ", cwd"
	} else {
		selectCols += ", '' AS cwd"
	}
	q := `SELECT ` + selectCols + ` FROM sessions WHERE user_id = ? AND transport = 'local'`
	if columnExistsDB(db, "sessions", "deleted_at") {
		q += ` AND deleted_at IS NULL`
	}
	rows, err := db.Query(q, fromUID)
	if err != nil {
		return 0, fmt.Errorf("查询待克隆会话失败: %w", err)
	}
	type sessionRow struct{ id, cwd string }
	var list []sessionRow
	for rows.Next() {
		var it sessionRow
		if err := rows.Scan(&it.id, &it.cwd); err != nil {
			rows.Close()
			return 0, fmt.Errorf("读取会话行失败: %w", err)
		}
		list = append(list, it)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("读取会话行失败: %w", err)
	}
	rows.Close()

	copied := 0
	for _, it := range list {
		if it.id == "" || isClawWorkspace(it.cwd) {
			continue
		}
		newCID := uuid.NewString() // 必须带连字符的 UUID：官方只认这个格式
		copyProjectJSONL(root, it.id, newCID)
		ok, ierr := insertSessionClone(db, newCID, it.id, fromUID, toUID)
		if ierr != nil {
			return copied, fmt.Errorf("复制会话 %s 失败: %w", it.id, ierr)
		}
		if !ok {
			continue // 源行在查询后被删：跳过
		}
		registerEdgeSyncMapping(root, newCID, toUID) // 云端归属失败不致命
		copied++
	}
	return copied, nil
}

// copyProjectJSONL 复制会话正文到新 id 同名文件，正文里的旧 id 引用一并替换。
// 正文缺失不算错误（与官方一致：允许「只有元数据」的会话存在）。
func copyProjectJSONL(root, cid, newCID string) bool {
	src, ok := findProjectJSONL(root, cid)
	if !ok {
		return false
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return false
	}
	mode := os.FileMode(0o600)
	if st, serr := os.Stat(src); serr == nil {
		mode = st.Mode().Perm()
	}
	dst := filepath.Join(filepath.Dir(src), newCID+".jsonl")
	return os.WriteFile(dst, []byte(strings.ReplaceAll(string(raw), cid, newCID)), mode) == nil
}

// insertSessionClone 复制 sessions 行为新 id（动态列：列集随官方 schema 变化，
// 只覆盖 id/user_id/时间戳/deleted_at，其余原样保留）。
func insertSessionClone(db *sql.DB, newCID, cid, fromUID, toUID string) (bool, error) {
	rows, err := db.Query(`SELECT * FROM sessions WHERE id = ? AND user_id = ?`, cid, fromUID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return false, err
	}
	if !rows.Next() {
		return false, nil // 源行不存在：无操作
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return false, err
	}
	rows.Close()

	nowMs := time.Now().UnixMilli()
	names := make([]string, len(cols))
	marks := make([]string, len(cols))
	for i, c := range cols {
		names[i] = quoteIdent(c)
		marks[i] = "?"
		switch c {
		case "id":
			vals[i] = newCID
		case "user_id":
			vals[i] = toUID
		case "created_at", "updated_at":
			vals[i] = nowMs
		case "deleted_at":
			vals[i] = nil
		}
	}
	_, err = db.Exec(
		`INSERT OR REPLACE INTO sessions (`+strings.Join(names, ", ")+`) VALUES (`+strings.Join(marks, ", ")+`)`,
		vals...)
	if err != nil {
		return false, err
	}
	return true, nil
}

// registerEdgeSyncMapping 把新会话登记进云端映射库（归属目标账号）。
// 映射库文件名随版本变化（国内历史 v2 / 本机实测 v4），按 glob 探测；失败不致命。
func registerEdgeSyncMapping(root, cid, uid string) bool {
	matches, _ := filepath.Glob(filepath.Join(root, "edge-sync-mapping-*.db"))
	written := false
	for _, p := range matches {
		if !fileExists(p) {
			continue
		}
		db, err := sql.Open("sqlite", "file:"+p+"?mode=rw&_pragma=busy_timeout(3000)")
		if err != nil {
			continue
		}
		if tableExistsDB(db, "edge_sync_mapping") {
			_, err := db.Exec(
				`INSERT OR REPLACE INTO edge_sync_mapping (session_id, conversation_id, msg_channel, created_at)
				 VALUES (?, ?, ?, ?)`,
				cid, cid, "convmsg:"+uid, time.Now().Unix())
			if err == nil {
				written = true
			}
		}
		db.Close()
	}
	return written
}
