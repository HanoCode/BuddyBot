package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// seedOfficialAuthFile 往指定路径写入官方客户端形态的登录文件：
// uid 明文，token/昵称为 $wbEncrypted 加密信封（真实登录位即此形态）。
func seedOfficialAuthFile(t *testing.T, path, uid string) {
	t.Helper()
	doc := map[string]any{
		"account": map[string]any{
			"uid":      uid,
			"nickname": map[string]any{"$wbEncrypted": 1, "envelope": "fake"},
		},
		"auth": map[string]any{
			"accessToken":  map[string]any{"$wbEncrypted": 1, "envelope": "fake"},
			"refreshToken": map[string]any{"$wbEncrypted": 1, "envelope": "fake"},
			"expiresAt":    time.Now().Add(24 * time.Hour).UnixMilli(),
			"domain":       "www.workbuddy.cn",
		},
	}
	raw, _ := json.Marshal(doc)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("创建登录位目录失败: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("写入登录位失败: %v", err)
	}
}

// 端到端：登录位（加密形态）识别 uid → 发现目录找到同 uid 明文凭证 → 元信息来自凭证
func TestDetectClientSessionEndToEnd(t *testing.T) {
	svc := newTestService(t)
	dir := t.TempDir()
	cfg := svc.GetConfig()
	cfg.ClientAuthDirs = []string{dir, "~/nonexistent-xyz", dir /* 重复目录去重 */}
	if err := svc.UpdateConfig(cfg); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}

	const uid = "u-client-1"
	authFile := filepath.Join(t.TempDir(), "auth", "workbuddy-desktop.info")
	seedOfficialAuthFile(t, authFile, uid)

	// 未放凭证：Detected=true 但 CredFile 为空
	sess := svc.detectClientSessionAt(authFile)
	if !sess.Detected || sess.UID != uid || sess.CredFile != "" {
		t.Fatalf("仅登录位时应 Detected=true/uid 命中/CredFile 空，got %+v", sess)
	}

	// 放入同 uid 明文凭证：CredFile 命中，昵称/区域/有效期来自凭证
	body := []byte(`{"accessToken":"at-` + uid + `","refreshToken":"rt","expiresAt":` +
		strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) +
		`,"domain":"workbuddy.ai","realm":"global","uid":"` + uid + `","nickname":"发现号"}`)
	if _, err := ImportCredential(dir, "workbuddy-"+uid+".json", body); err != nil {
		t.Fatalf("写入发现凭证失败: %v", err)
	}
	sess = svc.detectClientSessionAt(authFile)
	if sess.CredFile != filepath.Join(dir, "workbuddy-"+uid+".json") {
		t.Fatalf("凭证未被发现: %+v", sess)
	}
	if sess.Nickname != "发现号" || sess.Realm != "global" || sess.ExpiresAt == 0 {
		t.Fatalf("凭证元信息不符: %+v", sess)
	}

	// 凭证已导入账号池：InPool=true 且不再报 CredFile
	if _, err := ImportCredential(svc.AuthDir(), "workbuddy-"+uid+".json", body); err != nil {
		t.Fatalf("导入账号池失败: %v", err)
	}
	sess = svc.detectClientSessionAt(authFile)
	if !sess.InPool {
		t.Fatalf("已导入账号池后应 InPool=true: %+v", sess)
	}
}

// clientSessionUID 兼容嵌套/扁平两种形态，且容忍加密字段与坏数据
func TestClientSessionUID(t *testing.T) {
	nested := []byte(`{"account":{"uid":"uid-nested"},"auth":{"accessToken":{"$wbEncrypted":1}}}`)
	flat := []byte(`{"uid":"uid-flat","accessToken":"at"}`)
	garbage := []byte(`not-json`)
	if got := clientSessionUID(nested); got != "uid-nested" {
		t.Fatalf("嵌套形态 uid 提取失败: %q", got)
	}
	if got := clientSessionUID(flat); got != "uid-flat" {
		t.Fatalf("扁平形态 uid 提取失败: %q", got)
	}
	if got := clientSessionUID(garbage); got != "" {
		t.Fatalf("坏数据应返回空: %q", got)
	}
}
