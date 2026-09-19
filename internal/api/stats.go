package api

import (
	"context"

	"workbuddy-desktop/internal/core"
)

// StatsAPI 统计聚合API。
//
// 所有返回值都由真实请求日志 / 任务日志聚合而来，前后端都不做兜底填充：
// 没有数据就是 0，能拿到什么算什么。
type StatsAPI struct {
	service *core.Service
}

// NewStatsAPI 创建统计API
func NewStatsAPI(service *core.Service) *StatsAPI {
	return &StatsAPI{service: service}
}

// Dashboard 返回近 days 天的整页统计（总览 + 逐日 + 按模型 + 按密钥 + 时段 + 任务）
func (s *StatsAPI) Dashboard(ctx context.Context, days int) (*core.Dashboard, error) {
	d := s.service.Stats().Dashboard(days)
	return &d, nil
}

// Overview 仅取总览（仪表盘 KPI 用，轻量）
func (s *StatsAPI) Overview(ctx context.Context, days int) (*core.Overview, error) {
	d := s.service.Stats().Dashboard(days)
	return &d.Overview, nil
}

// OfficialUsage 官方口径用量对账：拉取各在线账号最近 days 天的官方请求用量
// （带缓存；force=true 跳过缓存）。单账号失败不影响整体，由 Source 字段标识
// official / partial / unavailable，前端据此决定是否回退本地统计。
func (s *StatsAPI) OfficialUsage(ctx context.Context, days int, force bool) (*core.OfficialUsageReport, error) {
	return s.service.OfficialUsage(days, force)
}

// SessionDrilldown 会话下钻：缓存命中率 KPI + 会话 Top 聚合（按 token 降序）。
func (s *StatsAPI) SessionDrilldown(ctx context.Context, days int) (*core.SessionDrilldown, error) {
	d := s.service.Stats().SessionDrilldown(days)
	return &d, nil
}

// SessionRequests 单个会话的请求明细（时间升序），下钻第三级。
func (s *StatsAPI) SessionRequests(ctx context.Context, sessionID string, days int) ([]core.RequestLog, error) {
	return s.service.Stats().SessionRequests(sessionID, days), nil
}
