package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
	if (gotRefExpMs - nowMs) < oneYearMs-5000 || (gotRefExpMs-nowMs) > oneYearMs+5000 {
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

// 3. 端到端：写文件后能用 ParseCredential 解析出 accessToken + uid
//    （等于模拟官方客户端「读 → 抽出 session.auth.accessToken」走 codec 后落地）
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

// 4. 嵌套结构的源头兼容：池凭证如果是嵌套形（ImportCredential 容忍形态之一）也能流转
//    ——这是为了不让 ImportCredential 后的池文件形态把切换逻辑写碎。
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
