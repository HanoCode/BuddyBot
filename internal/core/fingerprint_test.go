package core

import (
	"net/http"
	"testing"
)

// TestBillingHeadersCarryStableFingerprint P0-2：billing/growth 域请求必须携带
// 按 UID 稳定派生的设备指纹头（与 chat 链路同源），实现「同一账号固定同一台
// 虚拟设备、多账号之间天然隔离」的防关联风控形态。
func TestBillingHeadersCarryStableFingerprint(t *testing.T) {
	c := newUpstreamClient()
	cred := &UpstreamCred{UID: "u_fp", AccessToken: "at", Realm: "cn", Domain: "codebuddy.cn"}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com/x", nil)
	c.setBillingHeaders(req, cred)

	if got := req.Header.Get("X-Machine-ID"); got == "" || got != deriveAccountStableID("u_fp", "machine") {
		t.Fatalf("X-Machine-ID = %q, want 按 UID 稳定派生", got)
	}
	if got := req.Header.Get("X-Session-ID"); got != deriveAccountStableID("u_fp", "session") {
		t.Fatalf("X-Session-ID = %q, want 按 UID 稳定派生", got)
	}
	if got := req.Header.Get("X-User-Id"); got != "u_fp" {
		t.Fatalf("X-User-Id = %q, want u_fp", got)
	}

	// 稳定性：同账号重复派生一致
	if deriveAccountStableID("u_fp", "machine") != deriveAccountStableID("u_fp", "machine") {
		t.Fatal("同账号指纹派生应幂等")
	}
	// 隔离性：不同账号指纹不同
	if deriveAccountStableID("u_fp", "machine") == deriveAccountStableID("u_other", "machine") {
		t.Fatal("不同账号的设备指纹不应相同")
	}
}
