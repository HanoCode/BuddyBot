package core

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// fakeGrowthRoutes 盲盒/补签/礼包的假上游路由。
func fakeGrowthRoutes(t *testing.T, quota int, streakBody string) *httptest.Server {
	t.Helper()
	opens := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case buddyQuotaPath:
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"affordable":` + strconv.Itoa(quota) + `,"balance":50}}`))
		case buddyOpenPath:
			opens++
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"results":[{"instance":{"name":"橘猫","rarity":"SSR"}}]}}`))
		case growthHeatmapPath:
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"cells":[{"date":"2026-09-18","score":0},{"date":"2026-09-17","score":3}]}}`))
		case streakPath:
			_, _ = w.Write([]byte(streakBody))
		case makeupCardUsePath:
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{}}`))
		case claimGiftPath, claimCompensationPath:
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"credit":5}}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestBlindBoxRound P1-4：盲盒按能量开盒且受单轮上限约束。
func TestBlindBoxRound(t *testing.T) {
	c := newUpstreamClient()
	c.baseCN = "http://127.0.0.1:1" // 占位，下面整体替换
	up := fakeGrowthRoutes(t, 8, `{"code":0,"msg":"ok","data":{"makeup_cards":{"balance":2}}}`)
	defer up.Close()
	c.baseCN, c.baseGlobal = up.URL, up.URL
	c.billingCN, c.billingGlobal = up.URL, up.URL
	cred := &UpstreamCred{UID: "u_bb", AccessToken: "at", Realm: "cn"}

	res, err := c.BlindBoxRound(cred)
	if err != nil || res == nil {
		t.Fatalf("盲盒轮失败: %v", err)
	}
	if res.Opened != blindBoxMaxOpens {
		t.Fatalf("开启次数 = %d, want %d（能量 8 足够但受单轮上限）", res.Opened, blindBoxMaxOpens)
	}
	if len(res.Items) == 0 || res.Items[0] != "橘猫(SSR)" {
		t.Fatalf("物品解析异常: %v", res.Items)
	}
}

// TestMakeupCard P1-4：昨日漏签 + 有卡 → 补签成功；无卡 → 说明文案。
func TestMakeupCard(t *testing.T) {
	c := newUpstreamClient()
	up := fakeGrowthRoutes(t, 0, `{"code":0,"msg":"ok","data":{"makeup_cards":{"balance":2}}}`)
	defer up.Close()
	c.baseCN, c.baseGlobal = up.URL, up.URL
	c.billingCN, c.billingGlobal = up.URL, up.URL
	cred := &UpstreamCred{UID: "u_mk", AccessToken: "at", Realm: "cn"}

	note, _ := c.UseMakeupCardIfMissed(cred)
	if note == "" || !strings.Contains(note, "连签保住") {
		t.Fatalf("有卡漏签应补签成功, got %q", note)
	}

	up2 := fakeGrowthRoutes(t, 0, `{"code":0,"msg":"ok","data":{"makeup_cards":{"balance":0}}}`)
	defer up2.Close()
	c.baseCN, c.baseGlobal = up2.URL, up2.URL
	note, _ = c.UseMakeupCardIfMissed(cred)
	if !strings.Contains(note, "无补签卡") {
		t.Fatalf("无卡应提示无补签卡, got %q", note)
	}
}

// TestClaimGiftAndCompensation P1-4：礼包与补偿领取成功时返回积分说明。
func TestClaimGiftAndCompensation(t *testing.T) {
	c := newUpstreamClient()
	up := fakeGrowthRoutes(t, 0, `{}`)
	defer up.Close()
	c.billingCN, c.billingGlobal = up.URL, up.URL
	cred := &UpstreamCred{UID: "u_g", AccessToken: "at", Realm: "cn"}

	note := c.ClaimGiftAndCompensation(cred)
	if !strings.Contains(note, "新手礼包 +5分") || !strings.Contains(note, "补偿领取 +5分") {
		t.Fatalf("礼包补偿领取说明异常: %q", note)
	}
}



