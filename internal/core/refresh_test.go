package core

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRefreshInterval 验证定时刷新间隔解析：0/负数回落默认值，正数原样生效
func TestRefreshInterval(t *testing.T) {
	if got := refreshInterval(0); got != DefaultRefreshIntervalMinutes*time.Minute {
		t.Fatalf("0 应回落默认间隔，得到 %v", got)
	}
	if got := refreshInterval(-5); got != DefaultRefreshIntervalMinutes*time.Minute {
		t.Fatalf("负数应回落默认间隔，得到 %v", got)
	}
	if got := refreshInterval(15); got != 15*time.Minute {
		t.Fatalf("15 分钟应原样生效，得到 %v", got)
	}
}

// TestRefreshTickDisabled 验证关闭定时刷新时不触发刷新
func TestRefreshTickDisabled(t *testing.T) {
	svc := NewServiceAt(t.TempDir())
	svc.config.Schedule.Refresh = RefreshConfig{Enabled: false, IntervalMinutes: 30}
	s := svc.scheduler
	s.refreshTick() // 不应 panic，也不应置 lastRefresh
	s.mu.Lock()
	fired := !s.lastRefresh.IsZero()
	s.mu.Unlock()
	if fired {
		t.Fatal("刷新关闭时不应记录刷新时刻")
	}
}

// TestRefreshTickDue 验证开启定时刷新后到期会触发刷新（账号池为空，刷新空跑）
func TestRefreshTickDue(t *testing.T) {
	svc := NewServiceAt(t.TempDir())
	svc.config.Schedule.Refresh = RefreshConfig{Enabled: true, IntervalMinutes: 30}
	s := svc.scheduler
	s.refreshTick()
	s.mu.Lock()
	fired := !s.lastRefresh.IsZero()
	s.mu.Unlock()
	if !fired {
		t.Fatal("首次 tick 应视为到期并记录刷新时刻")
	}
	// 间隔内不重复触发
	s.mu.Lock()
	s.lastRefresh = time.Now().Add(-time.Minute) // 仅 1 分钟前刷新过
	s.mu.Unlock()
	s.refreshTick()
	s.mu.Lock()
	defer s.mu.Unlock()
	if elapsed := time.Since(s.lastRefresh); elapsed > 2*time.Minute {
		t.Fatalf("间隔内不应重复触发刷新（lastRefresh=%v）", s.lastRefresh)
	}
}

// TestRefreshTokenResponseShape 验证 token/refresh 的响应解析：
// 线上该端点在 HTTP 200 下返回业务信封 {code,msg,data}，token 在 data 里，
// 直接从顶层取 accessToken 会恒判「缺少 accessToken」（扫码登录 7 天 token 必踩）。
func TestRefreshTokenResponseShape(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantErr     bool
		wantRelogin bool
		wantAT      string
	}{
		{
			name:   "业务信封（线上实测形态）",
			body:   `{"code":0,"msg":"OK","requestId":"r1","data":{"accessToken":"at-new","refreshToken":"rt-new","expiresIn":2592000,"domain":"www.codebuddy.cn"}}`,
			wantAT: "at-new",
		},
		{
			name:   "扁平响应（历史兼容）",
			body:   `{"accessToken":"at-flat","refreshToken":"rt-flat","expiresIn":5184000}`,
			wantAT: "at-flat",
		},
		{
			name:        "HTTP200 + code=12153 应判定为需重新授权",
			body:        `{"code":12153,"msg":"Offline user session not found","requestId":"r2","data":null}`,
			wantErr:     true,
			wantRelogin: true,
		},
		{
			name:    "信封内确实缺 accessToken 才报缺失",
			body:    `{"code":0,"msg":"OK","data":{"refreshToken":"rt-only"}}`,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			svc := NewServiceAt(t.TempDir())
			svc.gateway.up.baseCN, svc.gateway.up.baseGlobal = srv.URL, srv.URL
			out, err := svc.gateway.up.refreshToken(&UpstreamCred{
				UID: "u1", Realm: "cn", AccessToken: "at-old", RefreshToken: "rt-old",
			})
			if tc.wantErr {
				if err == nil {
					t.Fatal("期望失败但刷新成功了")
				}
				var relogin *reloginRequiredError
				if got := errors.As(err, &relogin); got != tc.wantRelogin {
					t.Fatalf("需重新授权判定不符（relogin=%v）: %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("期望成功但失败: %v", err)
			}
			if out.AccessToken != tc.wantAT {
				t.Fatalf("accessToken 应为 %q，得到 %q", tc.wantAT, out.AccessToken)
			}
			if out.ExpiresAtUnix <= time.Now().Unix() {
				t.Fatalf("expiresIn 应顺延过期时间，得到 %d", out.ExpiresAtUnix)
			}
		})
	}
}
