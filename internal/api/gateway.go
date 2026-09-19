package api

import (
	"context"
	"time"

	"workbuddy-desktop/internal/core"
)

// GatewayAPI 网关控制API
type GatewayAPI struct {
	service *core.Service
}

// NewGatewayAPI 创建网关API
func NewGatewayAPI(service *core.Service) *GatewayAPI {
	return &GatewayAPI{service: service}
}

// GatewayStatus 网关状态（真实聚合：运行态 + 账号池 + 请求日志 + 任务状态）
type GatewayStatus struct {
	Running  bool   `json:"running"`
	Uptime   string `json:"uptime"`
	Listen   string `json:"listen"`
	Healthy  int    `json:"healthy"`
	Total    int    `json:"total"`
	Requests int    `json:"requests"`
	Tokens   int    `json:"tokens"`

	TodayRequests int     `json:"todayRequests"`
	TodayTokens   int     `json:"todayTokens"`
	Errors        int     `json:"errors"`
	SuccessRate   float64 `json:"successRate"`
	AvgLatency    float64 `json:"avgLatency"`
	P90           float64 `json:"p90"`
	FirstLatency  float64 `json:"firstLatency"`
	Inflight      int     `json:"inflight"`

	PoolOnline   int `json:"poolOnline"`
	PoolCooldown int `json:"poolCooldown"`
	PoolExpired  int `json:"poolExpired"`
	PoolDisabled int `json:"poolDisabled"`
	PoolUnknown  int `json:"poolUnknown"`

	Tasks *core.SchedulerStatus `json:"tasks,omitempty"`
}

// Start 启动网关
func (g *GatewayAPI) Start(ctx context.Context) error {
	if g.service.IsRunning() {
		return nil
	}
	g.service.Start(context.Background())
	return nil
}

// Stop 停止网关
func (g *GatewayAPI) Stop(ctx context.Context) error {
	g.service.Stop()
	return nil
}

// Restart 重启网关
func (g *GatewayAPI) Restart(ctx context.Context) error {
	g.service.Restart()
	return nil
}

// Status 获取网关状态（全部字段来自真实运行数据）
func (g *GatewayAPI) Status(ctx context.Context) (*GatewayStatus, error) {
	cfg := g.service.GetConfig()
	logs := g.service.Store().ListRequestLogs()
	accounts := g.service.Accounts()

	st := &GatewayStatus{
		Running: g.service.IsRunning(),
		Listen:  cfg.Listen,
		Total:   len(accounts),
	}
	if st.Running {
		st.Uptime = time.Since(g.service.StartedAt()).Round(time.Second).String()
	}

	for _, a := range accounts {
		switch a.Status {
		case "online":
			st.PoolOnline++
		case "cooldown":
			st.PoolCooldown++
		case "expired":
			st.PoolExpired++
		case "disabled":
			st.PoolDisabled++
		default:
			st.PoolUnknown++
		}
		st.Inflight += a.Inflight
	}
	st.Healthy = st.PoolOnline

	// 今日窗口（本地自然日）
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	lat := make([]float64, 0, len(logs))
	latSum, firstSum := 0.0, 0.0
	for _, l := range logs {
		st.Requests++
		st.Tokens += l.Tokens
		if l.Status >= 400 {
			st.Errors++
		}
		if l.TS >= dayStart {
			st.TodayRequests++
			st.TodayTokens += l.Tokens
		}
		if l.Latency > 0 {
			lat = append(lat, l.Latency)
			latSum += l.Latency
		}
		if l.FirstLatency > 0 {
			firstSum += l.FirstLatency
		}
	}
	if st.Requests > 0 {
		st.SuccessRate = float64(int64(float64(st.Requests-st.Errors)/float64(st.Requests)*10000+0.5)) / 100
	}
	if len(lat) > 0 {
		st.AvgLatency = float64(int64(latSum/float64(len(lat))*100+0.5)) / 100
		st.P90 = core.Percentile(lat, 90)
	}
	if firstSum > 0 {
		st.FirstLatency = float64(int64(firstSum/float64(len(lat))*100+0.5)) / 100
	}

	taskStatus := g.service.Scheduler().Status()
	st.Tasks = &taskStatus
	return st, nil
}
