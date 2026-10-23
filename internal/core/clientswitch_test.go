package core

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// clientswitch_test.go 覆盖「设为客户端登录」的核心改动——
// buildOfficialAuthFile 把扁平池凭证重组为 WorkBuddy v5.6+ 嵌套 session 后写入官方登录位。
//
// 关键不变量（与官方 codec 的 AUTH_CREDENTIAL_FIELDS + 真机 backup 字段对齐）：
//   - 顶层必须是嵌套 {auth:{accessToken,refreshToken,domain,expiresAt,refreshExpiresAt,...}, account:{uid,nickname,...}}
//   - auth.expiresAt / auth.refreshExpiresAt / auth.lastRefreshTime 单位是毫秒，
//     与池里 Pool 凭证的 expiresAt（秒）口径相差 ×1000；
//   - auth.expiresIn / auth.refreshExpiresIn 单位是秒；
//   - sessionState 是 UUID（v4 派生），不是随便字符串；
//   - 写文件是原子写（先 .tmp 再 rename），并且原备份还在——失败时仍能回滚。

// 1. 嵌套结构 + 字段名 + 单位（核心：错一个数（ms vs s）就接不上官方 codec）
func TestBuildOfficialAuthFile_Shape(t *testing.T) {
	expiresSec := time.Now().Add(48 * time.Hour).Unix()
	cred := &UpstreamCred{
		UID:           "u-shape-001",
		AccessToken:   "at-shape-001",
		RefreshToken:  "rt-shape-001",
		Domain:        "www.codebuddy.cn",
		Realm:         "cn",
		EnterpriseID:  "ent-1",
		ExpiresAtUnix: expiresSec,
	}
	raw, err := buildOfficialAuthFile(cred, "昵称-测试")
	if err != nil {
		t.Fatalf("buildOfficialAuthFile 失败: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("产物非合法 JSON: %v\n%s", err, raw)
	}
	auth, ok := doc["auth"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 auth 嵌套对象: %+v", doc)
	}
	account, ok := doc["account"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 account 嵌套对象: %+v", doc)
	}
	// token 值直接透传，不做任何编辑
	if auth["accessToken"] != "at-shape-001" || auth["refreshToken"] != "rt-shape-001" {
		t.Fatalf("token 透传失败: %+v", auth)
	}
	if auth["tokenType"] != "Bearer" {
		t.Fatalf("tokenType 应为 Bearer, got %v", auth["tokenType"])
	}
	if auth["domain"] != "www.codebuddy.cn" {
		t.Fatalf("domain 透传失败: %v", auth["domain"])
	}
	// 单位是 ms：池里 expiresAt 是秒，写出要 ×1000
	gotExpMs, ok := auth["expiresAt"].(float64)
	if !ok {
		t.Fatalf("auth.expiresAt 应为数字: %T", auth["expiresAt"])
	}
	wantExpMs := float64(expiresSec) * 1000
	if gotExpMs != wantExpMs {
		t.Fatalf("auth.expiresAt 单位错误，期望 ms=%v，实际 %v（差值 %v）", wantExpMs, gotExpMs, gotExpMs-wantExpMs)
	}
	// 单位是 sec：剩余秒
	gotExpIn, ok := auth["expiresIn"].(float64)
	if !ok {
		t.Fatalf("auth.expiresIn 应为数字: %T", auth["expiresIn"])
	}
	nowSec := time.Now().Unix()
	wantExpIn := float64(expiresSec - nowSec)
	if gotExpIn < wantExpIn-2 || gotExpIn > wantExpIn+2 { // 容忍 2s 测试抖动
		t.Fatalf("auth.expiresIn 应接近 %v，实际 %v", wantExpIn, gotExpIn)
	}
	// refreshExpiresAt 是 ms 的一年以后；lastRefreshTime 是当前 ms
	gotRefExpMs, ok := auth["refreshExpiresAt"].(float64)
	if !ok {
		t.Fatalf("auth.refreshExpiresAt 应为数字: %T", auth["refreshExpiresAt"])
	}
	nowMs := float64(time.Now().UnixMilli())
	if gotRefExpMs <= nowMs {
		t.Fatalf("refreshExpiresAt 必须在 now 之后：%v <= now %v", gotRefExpMs, nowMs)
	}
	oneYearMs := float64(365 * 24 * 3600 * 1000)
	if (gotRefExpMs-nowMs) < oneYearMs-5000 || (gotRefExpMs-nowMs) > oneYearMs+5000 {
		t.Fatalf("refreshExpiresAt 与 now 之差应接近 365d：差值 %v", gotRefExpMs-nowMs)
	}
	gotLR, ok := auth["lastRefreshTime"].(float64)
	if !ok {
		t.Fatalf("auth.lastRefreshTime 应为数字: %T", auth["lastRefreshTime"])
	}
	if gotLR < nowMs-5000 || gotLR > nowMs+5000 {
		t.Fatalf("auth.lastRefreshTime 应接近 now：%v vs %v", gotLR, nowMs)
	}
	// sessionState 是 v4 UUID
	if _, perr := uuid.Parse(asString(auth["sessionState"])); perr != nil {
		t.Fatalf("sessionState 不是有效 UUID：%v", auth["sessionState"])
	}
	// account 必须含 uid/nickname + lastLogin=true
	if account["uid"] != "u-shape-001" {
		t.Fatalf("account.uid 透传失败: %v", account["uid"])
	}
	if account["nickname"] != "昵称-测试" {
		t.Fatalf("account.nickname 应为传入值: %v", account["nickname"])
	}
	if account["lastLogin"] != true {
		t.Fatalf("account.lastLogin 应为 true：%v", account["lastLogin"])
	}
	// accounts 列表至少有当前一个
	if accounts, ok := doc["accounts"].([]any); !ok || len(accounts) == 0 {
		t.Fatalf("accounts 列表缺失或为空：%v", doc["accounts"])
	}
}

// 2. 防御：nil/空 accessToken 必须早失败，绝不写出残文件
func TestBuildOfficialAuthFile_RejectBadInput(t *testing.T) {
	if _, err := buildOfficialAuthFile(nil, "x"); err == nil {
		t.Fatal("nil 凭证应报错")
	}
	if _, err := buildOfficialAuthFile(&UpstreamCred{}, "x"); err == nil {
		t.Fatal("空 accessToken 应报错")
	}
	if _, err := buildOfficialAuthFile(&UpstreamCred{AccessToken: " "}, "x"); err == nil {
		t.Fatal("空白 accessToken 应报错")
	}
}

//  3. 端到端：写文件后能用 ParseCredential 解析出 accessToken + uid
//     （等于模拟官方客户端「读 → 抽出 session.auth.accessToken」走 codec 后落地）
//
// 注：官方嵌套文件的 auth.expiresAt 单位是毫秒（OAuth Provider 的原口径），与池
// 凭证的秒口径相差 ×1000；ParseCredential 不做单位换算，读出来就是 ms 形态。
// 这条通路仅用于切换前的 precheck 校验，不会被 LoadUpstreamCred 误用。
func TestBuildOfficialAuthFile_WritableAndParsable(t *testing.T) {
	expiresSec := time.Now().Add(72 * time.Hour).Unix()
	cred := &UpstreamCred{
		UID:           "u-e2e-007",
		AccessToken:   "at-e2e-007",
		RefreshToken:  "rt-e2e-007",
		Domain:        "www.codebuddy.cn",
		ExpiresAtUnix: expiresSec,
	}
	raw, err := buildOfficialAuthFile(cred, "E2E")
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "workbuddy-desktop.info")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	got, perr := ParseCredential(path, gotBytes)
	if perr != nil {
		t.Fatalf("官方客户端风格文件未能被 ParseCredential 解析: %v\n%s", perr, gotBytes)
	}
	if got.UID != "u-e2e-007" {
		t.Fatalf("uid 不匹配：%s", got.UID)
	}
	// 单位差：池里是秒，官方嵌套是毫秒
	if got.ExpiresAt != expiresSec*1000 {
		t.Fatalf("expiresAt 应是池值 ×1000 ms：池 %d，解析回 %d", expiresSec, got.ExpiresAt)
	}
	if got.Nickname != "E2E" {
		t.Fatalf("nickname 不匹配：%s", got.Nickname)
	}
	if !got.HasToken || !got.HasRefresh {
		t.Fatalf("token 应被识别为存在：%+v", got)
	}
}

//  4. 嵌套结构的源头兼容：池凭证如果是嵌套形（ImportCredential 容忍形态之一）也能流转
//     ——这是为了不让 ImportCredential 后的池文件形态把切换逻辑写碎。
func TestBuildOfficialAuthFile_AcceptsPoolViaUpstreamCred(t *testing.T) {
	expiresSec := time.Now().Add(72 * time.Hour).Unix()
	// 模拟「池里就是嵌套形态」（来自外部导入或 v5.6+ 兼容的备份）
	body := []byte(`{"auth":{"accessToken":"at-nested","refreshToken":"rt-nested","expiresAt":` +
		strconv.FormatInt(expiresSec, 10) +
		`,"domain":"workbuddy.ai","realm":"global"},"account":{"uid":"u-nest-1","nickname":"嵌套昵称","enterpriseId":"e2"}}`)
	authDir := t.TempDir()
	name := "workbuddy-nest.json"
	if err := os.WriteFile(filepath.Join(authDir, name), body, 0o600); err != nil {
		t.Fatalf("写入池文件失败: %v", err)
	}
	cred, err := LoadUpstreamCred(authDir, name)
	if err != nil {
		t.Fatalf("解析嵌套池凭证失败: %v", err)
	}
	if cred.UID != "u-nest-1" || cred.AccessToken != "at-nested" || cred.Realm != "global" {
		t.Fatalf("池字段未透出: %+v", cred)
	}
	out, err := buildOfficialAuthFile(cred, cred.UID)
	if err != nil {
		t.Fatalf("重组失败: %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(out, &doc)
	if doc["account"].(map[string]any)["uid"] != "u-nest-1" {
		t.Fatalf("重组后 uid 丢失: %s", out)
	}
	if doc["auth"].(map[string]any)["domain"] != "workbuddy.ai" {
		t.Fatalf("重组后 domain 丢失: %s", out)
	}
}

// asString 把 json.Unmarshal 出的 any 还原成字符串；JSON 数字 / null 都视作空串
func asString(v any) string {
	s, _ := v.(string)
	return s
}

//  5. 登出标记：路径口径必须与官方 asar 的 getLogoutMarkerPath 一致（`${authFile}.logged-out`），
//     清理要同时干掉 retire 残留，且绝不能碰登录文件本身。
//     回归背景：客户端 401 登出后写标记，启动时「Auth logout marker found, ignoring stored session」，
//     切换流水线不清理 → 写入的凭证永远不生效（实测 5 次切换全部 userId=<empty>）。
func TestClearLogoutMarker(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "workbuddy-desktop.info")
	if err := os.WriteFile(authFile, []byte(`{"account":{"uid":"u-1"}}`), 0o600); err != nil {
		t.Fatalf("写入登录文件失败: %v", err)
	}
	marker := officialAuthLogoutMarker(authFile)
	if marker != authFile+".logged-out" {
		t.Fatalf("登出标记路径与官方不一致: %s", marker)
	}
	retired := marker + ".retired.1234.abc"
	for _, p := range []string{marker, retired} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("写入标记失败: %v", err)
		}
	}
	if !fileExists(marker) {
		t.Fatal("前置条件失败：标记应存在")
	}
	if err := clearLogoutMarker(authFile); err != nil {
		t.Fatalf("清理登出标记失败: %v", err)
	}
	if fileExists(marker) || fileExists(retired) {
		t.Fatal("登出标记或 retire 残留未被清理")
	}
	if !fileExists(authFile) {
		t.Fatal("登录文件被误删")
	}
	// 幂等：没有标记时也算成功
	if err := clearLogoutMarker(authFile); err != nil {
		t.Fatalf("重复清理应幂等: %v", err)
	}
}

// newSessionTestRoot 造一个最小客户端数据根：projects/{ws}/*.jsonl + workbuddy.db
// （列对齐真实 schema 的核心子集）+ edge-sync-mapping-v4.db。
func newSessionTestRoot(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	ws := filepath.Join(root, "projects", "Users-me-work")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatalf("建 projects 目录失败: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "workbuddy.db"))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY, cwd TEXT NOT NULL, user_id TEXT NOT NULL, title TEXT,
		transport TEXT NOT NULL DEFAULT 'local',
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER)`); err != nil {
		t.Fatalf("建 sessions 表失败: %v", err)
	}
	rows := []struct {
		id, cwd, uid, transport string
	}{
		{"s-a1", "/Users/me/work", "uid-a", "local"},
		{"s-a2", "/Users/me/work", "uid-a", "local"},
		{"s-claw", "/Users/me/WorkBuddy/Claw", "uid-a", "local"}, // Claw 工作区不克隆
		{"s-a3", "/Users/me/work", "uid-a", "cloud"},             // 云端会话归服务端
		{"s-b1", "/Users/me/work", "uid-b", "local"},
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO sessions(id, cwd, user_id, transport, created_at, updated_at)
			VALUES(?,?,?,?,1000,2000)`, r.id, r.cwd, r.uid, r.transport); err != nil {
			t.Fatalf("插数据失败: %v", err)
		}
		body := `{"sessionId":"` + r.id + `","role":"user","text":"hi ` + r.id + `"}`
		if err := os.WriteFile(filepath.Join(ws, r.id+".jsonl"), []byte(body), 0o600); err != nil {
			t.Fatalf("写正文失败: %v", err)
		}
	}
	edge, err := sql.Open("sqlite", filepath.Join(root, "edge-sync-mapping-v4.db"))
	if err != nil {
		t.Fatalf("建映射库失败: %v", err)
	}
	defer edge.Close()
	if _, err := edge.Exec(`CREATE TABLE edge_sync_mapping (
		session_id TEXT NOT NULL PRIMARY KEY, conversation_id TEXT NOT NULL UNIQUE,
		msg_channel TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL)`); err != nil {
		t.Fatalf("建映射表失败: %v", err)
	}
	return root
}

func countSessions(t *testing.T, dbPath, uid, transport string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id=? AND transport=?`,
		uid, transport).Scan(&n); err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	return n
}

// 6. 会话克隆：源账号会话原样保留，目标账号拿到新 id 的副本
//（正文 jsonl + sessions 行 + edge-sync 云端映射三件套齐备，云端/Claw 不复制）
func TestCloneClientSessionsAt(t *testing.T) {
	root := newSessionTestRoot(t)
	dbPath := filepath.Join(root, "workbuddy.db")
	backupDir := filepath.Join(root, "clientswitch")

	n, err := cloneClientSessionsAt(root, backupDir, "uid-a", "uid-b")
	if err != nil {
		t.Fatalf("克隆失败: %v", err)
	}
	if n != 2 { // s-a1 / s-a2；Claw 与云端会话不克隆
		t.Fatalf("应克隆 2 条，实际 %d", n)
	}
	// 源账号一条不少（这是与「改归属」迁移的本质差别）
	if left := countSessions(t, dbPath, "uid-a", "local"); left != 3 {
		t.Fatalf("源账号本地会话应保持 3 条（含 Claw），实际 %d", left)
	}
	if got := countSessions(t, dbPath, "uid-b", "local"); got != 3 {
		t.Fatalf("目标账号应有 3 条本地会话（1 原有 + 2 克隆），实际 %d", got)
	}
	// 云端行归属不变
	if got := countSessions(t, dbPath, "uid-a", "cloud"); got != 1 {
		t.Fatalf("云端会话不应被复制，uid-a 云端行应为 1，实际 %d", got)
	}

	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("重开库失败: %v", err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, deleted_at FROM sessions WHERE user_id='uid-b' AND id LIKE '%-%-%-%-%'`)
	if err != nil {
		t.Fatalf("查询克隆行失败: %v", err)
	}
	var newIDs []string
	for rows.Next() {
		var id string
		var deleted sql.NullInt64
		if err := rows.Scan(&id, &deleted); err != nil {
			t.Fatalf("扫描失败: %v", err)
		}
		if _, perr := uuid.Parse(id); perr != nil {
			t.Fatalf("克隆 id 必须是带连字符的 UUID: %s", id)
		}
		if deleted.Valid {
			t.Fatalf("克隆行 deleted_at 应为空: %s", id)
		}
		newIDs = append(newIDs, id)
	}
	rows.Close()
	if len(newIDs) != 2 {
		t.Fatalf("应有 2 条 UUID 克隆行，实际 %d", len(newIDs))
	}

	// 正文：新 jsonl 存在且内部 id 已替换；源正文未动
	for _, nid := range newIDs {
		p := filepath.Join(root, "projects", "Users-me-work", nid+".jsonl")
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			t.Fatalf("克隆正文缺失 %s: %v", nid, rerr)
		}
		if !strings.Contains(string(raw), nid) || !strings.Contains(string(raw), `"sessionId":"`+nid+`"`) {
			t.Fatalf("克隆正文应已替换为新 id: %s", string(raw))
		}
	}
	srcRaw, _ := os.ReadFile(filepath.Join(root, "projects", "Users-me-work", "s-a1.jsonl"))
	if !strings.Contains(string(srcRaw), "s-a1") {
		t.Fatalf("源正文不应被改写: %s", string(srcRaw))
	}

	// 云端映射：两条新会话都归属目标账号
	edge, err := sql.Open("sqlite", "file:"+filepath.Join(root, "edge-sync-mapping-v4.db")+"?mode=ro")
	if err != nil {
		t.Fatalf("打开映射库失败: %v", err)
	}
	defer edge.Close()
	for _, nid := range newIDs {
		var channel string
		if err := edge.QueryRow(`SELECT msg_channel FROM edge_sync_mapping WHERE session_id=?`, nid).Scan(&channel); err != nil {
			t.Fatalf("新会话未登记云端映射 %s: %v", nid, err)
		}
		if channel != "convmsg:uid-b" {
			t.Fatalf("云端映射渠道应为 convmsg:uid-b，实际 %s", channel)
		}
	}

	// 快照存在且可打开（克隆前状态：uid-a 本地 3 条）
	snaps, _ := filepath.Glob(filepath.Join(backupDir, "sessions-*.db"))
	if len(snaps) != 1 {
		t.Fatalf("应生成 1 份会话库快照，实际 %d", len(snaps))
	}
	snap, err := sql.Open("sqlite", snaps[0])
	if err != nil {
		t.Fatalf("快照打不开: %v", err)
	}
	defer snap.Close()
	var c int
	if err := snap.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id='uid-a'`).Scan(&c); err != nil {
		t.Fatalf("读快照失败: %v", err)
	}
	if c != 4 { // 3 local + 1 cloud
		t.Fatalf("快照应是克隆前状态（uid-a 4 条），实际 %d", c)
	}
}

// 7. 克隆边界：同账号 / 空 uid / 库不存在 都应静默成功；缺 projects/ 明确报错
func TestCloneClientSessionsAt_Noop(t *testing.T) {
	root := newSessionTestRoot(t)
	if n, err := cloneClientSessionsAt(root, t.TempDir(), "uid-a", "uid-a"); err != nil || n != 0 {
		t.Fatalf("同账号应为空操作: n=%d err=%v", n, err)
	}
	if n, err := cloneClientSessionsAt(root, t.TempDir(), "", "uid-b"); err != nil || n != 0 {
		t.Fatalf("空来源应为空操作: n=%d err=%v", n, err)
	}
	missing := t.TempDir()
	if n, err := cloneClientSessionsAt(missing, t.TempDir(), "uid-a", "uid-b"); err != nil || n != 0 {
		t.Fatalf("库不存在应为空操作: n=%d err=%v", n, err)
	}

	// 有库但缺 projects/：拒绝（不写只有元数据没有正文的半成品会话）
	half := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(half, "workbuddy.db"))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, cwd TEXT, user_id TEXT, transport TEXT)`); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	db.Close()
	if _, err := cloneClientSessionsAt(half, t.TempDir(), "uid-a", "uid-b"); err == nil {
		t.Fatal("缺 projects/ 时应明确报错")
	}
}
