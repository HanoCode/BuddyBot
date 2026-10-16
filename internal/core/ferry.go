package core

import (
	"net/http"
	"path/filepath"
	"time"

	"workbuddy-desktop/internal/ferry"
)

// 免费额度摆渡接入层：把 ferry.Manager 挂到本进程，并在网关的模型清单与
// chat/completions 上开一个分流口。
//
// 与参考实现（RuiC-oc-ferry）的差别：免费模型不写进任何客户端配置文件，
// 而是直接成为 BuddyBot 网关自身的可用模型，因此所有已接入的客户端都能选到它们，
// 并复用网关现有的日志与统计口径。

// FerryManager 惰性创建免费额度管理器（首次访问时按配置决定是否启动）
func (s *Service) FerryManager() *ferry.Manager {
	s.ferryOnce.Do(func() {
		cfg := s.GetConfig()
		s.ferry = ferry.New(ferry.Config{
			DataDir:     filepath.Join(s.dataDir, "ferry"),
			Concurrency: cfg.Ferry.Concurrency,
		})
		if cfg.Ferry.Enabled {
			s.ferry.Start()
		}
	})
	return s.ferry
}

// FerryAvailableModels 当前已判定可用且未被排除的免费模型。
// 零值 Service（测试构造）与未启用时返回 nil，调用方据此直接跳过分流。
func (s *Service) FerryAvailableModels() []ferry.Model {
	if s == nil || s.config == nil {
		return nil
	}
	if !s.GetConfig().Ferry.Enabled {
		return nil
	}
	return s.FerryManager().AvailableModels()
}

// FerryHasModel 该模型是否由免费额度服务提供
func (s *Service) FerryHasModel(model string) bool {
	for _, m := range s.FerryAvailableModels() {
		if m.ID == model {
			return true
		}
	}
	return false
}

// EffectivePrices 单价表视图：在用户单价表之上叠加免费额度模型的零价。
//
// 免费额度模型是「价格判定」选出来的（四项单价全 0），它们不是「未定价」而是真的 0 元。
// 不叠加的话，统计页会把这些本来免费的调用标成「未定价」并点名，
// 反而引导用户去补一个根本不存在的价格——那是错误信息，不是缺失信息。
//
// 用户若显式给同名模型配过单价，以用户配置为准（不与用户的选择争）。
func (s *Service) EffectivePrices() map[string]ModelPrice {
	ids := []string{}
	for _, m := range s.FerryAvailableModels() {
		ids = append(ids, m.ID)
	}
	return overlayFreePrices(s.ModelPrices(), ids)
}

// overlayFreePrices 把免费模型的零价叠加到单价表上。无免费模型时原样返回入参
// （不复制：单价表是只读视图，没有改动它的调用方）；
// 一旦要叠加就先复制——绝不能就地改用户配置里那张 map。
func overlayFreePrices(prices map[string]ModelPrice, freeIDs []string) map[string]ModelPrice {
	if len(freeIDs) == 0 {
		return prices
	}
	out := make(map[string]ModelPrice, len(prices)+len(freeIDs))
	for k, v := range prices {
		out[k] = v
	}
	for _, id := range freeIDs {
		if id == "" {
			continue
		}
		if _, ok := out[id]; !ok {
			out[id] = ModelPrice{} // 四项全 0 = 该项免费
		}
	}
	return out
}

// SetFerryEnabled 启用/停用免费额度（落盘；立即启停隔离运行时）
func (s *Service) SetFerryEnabled(enabled bool) error {
	cfg := s.GetConfig()
	cfg.Ferry.Enabled = enabled
	// SaveConfigWith 会把内存态一并换成这份配置，无需再单独改 s.config
	if err := s.SaveConfigWith(cfg); err != nil {
		return err
	}
	mgr := s.FerryManager()
	if enabled {
		mgr.Start()
	} else {
		mgr.Stop()
	}
	return nil
}

// handleFerryChat 免费额度模型的 chat/completions 分流。
//
// 与上游路径的三点不同：没有账号池与轮转（本地隔离进程就是唯一后端）、
// 没有提示词策略与指纹净化（请求正文原样交给适配器）、成功时只累加密钥配额
// （没有上游账号可记）。日志与统计口径保持一致，便于在日志页里区分。
func (g *Gateway) handleFerryChat(w http.ResponseWriter, r *http.Request, auth authInfo, req chatRequest, body []byte, start time.Time, ip string) {
	result, ferr := g.svc.FerryManager().Chat(r.Context(), body)
	if ferr != nil {
		// 客户端主动取消不算一次失败调用：与上游路径同样直接返回
		if r.Context().Err() != nil || ferr.Status == 499 {
			return
		}
		status := ferr.Status
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		g.recordLog(auth, req.Model, status, 0, 0, 0, start, ip, req.SessionID, req.Stream, ferr.Message)
		writeAPIError(w, status, ferr.Code, ferr.Message)
		return
	}
	g.chargeKeyOnly(auth, req.Model, result.Usage.Total())
	// 先记账再落日志：日志落盘是本次请求的最后一笔写。
	// 口径与上游路径一致：Tokens = 未命中输入 + 输出，缓存命中单列（见 pricing.go）。
	g.recordLog(auth, req.Model, http.StatusOK,
		result.Usage.Total(), result.Usage.Output, result.Usage.Cache,
		start, ip, req.SessionID, result.Stream, "")
	if result.Stream {
		g.svc.FerryManager().WriteStream(w, result)
		return
	}
	writeJSON(w, http.StatusOK, result.Body)
}

// chargeKeyOnly 免费额度请求只累加密钥配额用量：没有上游账号，
// 也没有账号状态要刷新，绝不伪造 account 侧的数字。
func (g *Gateway) chargeKeyOnly(auth authInfo, model string, tokens int) {
	if auth.key == nil || tokens <= 0 {
		return
	}
	credits := int(float64(tokens) / 1000.0 * g.modelRate(model))
	g.svc.Store().ConsumeKey(auth.key.ID, tokens, credits)
}
