package core

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// redisMirror 可选的 Upstash REST 状态镜像（对齐 workbuddy2api redisstore 的最小子集）。
//
// 同步两类数据：
//   - 会话粘性绑定：wbdesk:bind:<sessionID> → uid（TTL 7 天）；
//   - 状态快照：wbdesk:state → store.json 同构 JSON（恢复备份用）。
//
// 写全部 fire-and-forget（后台 goroutine，失败静默）；未配置或请求失败时零影响主流程。
type redisMirror struct {
	mu      sync.Mutex
	url     string
	token   string
	http    *http.Client
	inFlight chan struct{} // 有界并发
}

const (
	mirrorBindPrefix = "wbdesk:bind:"
	mirrorStateKey   = "wbdesk:state"
	mirrorBindTTL    = 7 * 24 * 3600
	mirrorMaxConc    = 4
)

func newRedisMirror() *redisMirror {
	return &redisMirror{http: &http.Client{Timeout: 5 * time.Second}, inFlight: make(chan struct{}, mirrorMaxConc)}
}

// configure 按配置重建镜像目标（未启用时置空 = Noop）。
func (m *redisMirror) configure(cfg RedisConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.url, m.token = "", ""
	if cfg.Enabled {
		if u := strings.TrimRight(strings.TrimSpace(cfg.URL), "/"); u != "" && strings.TrimSpace(cfg.Token) != "" {
			m.url, m.token = u, strings.TrimSpace(cfg.Token)
		}
	}
}

func (m *redisMirror) enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.url != ""
}

// restCall 调 Upstash REST 命令（路径段即命令与参数）。
func (m *redisMirror) restCall(path string) error {
	m.mu.Lock()
	url, token := m.url, m.token
	m.mu.Unlock()
	req, err := http.NewRequest(http.MethodGet, url+"/"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("upstash 返回 %d", resp.StatusCode)
	}
	return nil
}

// goAsync fire-and-forget 执行（有界并发，避免镜像拖垮主流程；队列满时直接
// 丢弃并提示一次，而不是让 goroutine 无界堆积）。
func (m *redisMirror) goAsync(fn func()) {
	if !m.enabled() {
		return
	}
	select {
	case m.inFlight <- struct{}{}:
	default:
		EmitEvent(EventAppError, map[string]any{"scope": "redis", "message": "镜像队列已满，本次同步已丢弃"})
		return
	}
	go func() {
		defer func() { <-m.inFlight }()
		fn()
	}()
}

// SetBind 写会话绑定（SET key val EX ttl）
func (m *redisMirror) SetBind(sessionID, uid string) {
	if !m.enabled() || sessionID == "" || uid == "" {
		return
	}
	path := fmt.Sprintf("set/%s%s/%s/ex/%d", mirrorBindPrefix, escapePath(sessionID), escapePath(uid), mirrorBindTTL)
	m.goAsync(func() { _ = m.restCall(path) })
}

// PushState 推送状态快照（store.json 原文）
func (m *redisMirror) PushState(storePath string) {
	m.goAsync(func() {
		raw, err := os.ReadFile(storePath)
		if err != nil {
			return
		}
		_ = m.restCall("set/"+mirrorStateKey+"/"+escapePath(string(raw)))
	})
}

// escapePath 把值转为单个 REST 路径段（Upstash 接受 URL 编码）。
func escapePath(v string) string {
	return strings.ReplaceAll(urlQueryEscape(v), "%2F", "%2F")
}

// urlQueryEscape 标准百分号编码（不引入 net/url 的 query 语义差异，保持单段）
func urlQueryEscape(v string) string {
	const hex = "0123456789ABCDEF"
	var b bytes.Buffer
	for _, c := range []byte(v) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xF])
		}
	}
	return b.String()
}
