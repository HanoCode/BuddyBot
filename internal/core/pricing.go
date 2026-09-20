package core

// ============================================================
// 模型单价表 → 金额换算
//
// 口径：单价单位为「元 / 百万 token」，按 token 分类分别计价（输入 / 输出 /
// 缓存读 / 缓存写）。单价表由用户在「设置 → 提示词与模型 → 模型单价」维护。
//
// 无默认价：未配置单价的模型金额返回 0 且 priced=false，由前端如实标注
// 「未定价」，不用任何猜来的价格兜底。
//
// 两条数据流的 input 语义不同，各自传入互不重叠的四项计数：
//   网关（RequestLog）：InputTokens 不含缓存命中，缓存单列 CacheTokens，
//     缓存写未观测 → 传 0；
//   客户端（JSONL）：in 已含 cacheRead，调用方需先扣除得到未命中输入。
// ============================================================

// ModelPriceTokensPerUnit 单价计价单位：每 100 万 token 报一个价（业界通行写法）。
const ModelPriceTokensPerUnit = 1_000_000

// ModelPrice 单模型单价（元 / 百万 token）。字段为 0 表示该项免费；
// 模型未出现在单价表里表示未定价。
type ModelPrice struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// PriceTable 单价表快照：一次聚合周期内只构建一次，避免逐条记录读配置锁。
type PriceTable struct {
	prices map[string]ModelPrice
}

// NewPriceTable 构建单价表快照（prices 可为 nil，等价于空表 = 全部未定价）。
func NewPriceTable(prices map[string]ModelPrice) PriceTable {
	return PriceTable{prices: prices}
}

// Cost 按单价表换算一次用量的金额（元，未四舍五入——聚合结束时统一 round4）。
// in / out / cacheRead / cacheWrite 必须是互不重叠的四项 token 计数。
// 第二个返回值表示该模型是否配了单价：false 时金额恒为 0，不代表「免费」。
func (t PriceTable) Cost(model string, in, out, cacheRead, cacheWrite int) (float64, bool) {
	p, ok := t.prices[model]
	if !ok {
		return 0, false
	}
	return (float64(in)*p.Input +
		float64(out)*p.Output +
		float64(cacheRead)*p.CacheRead +
		float64(cacheWrite)*p.CacheWrite) / ModelPriceTokensPerUnit, true
}

// round4 金额四舍五入到 0.0001 元（输出边界统一调用；聚合过程保留原始精度，
// 避免逐条抹零导致小用量总额失真）。金额非负，与既有 round2 同构。
func round4(v float64) float64 {
	return float64(int64(v*10000+0.5)) / 10000
}

// DefaultModelPrices 内置初始单价表（元 / 百万 token），让「金额」开箱就有数。
//
// 生效规则：配置里没有单价表（或表为空）时用它初始化；只要文件里给了非空表，
// 就完全以文件为准——不做键级合并，用户删掉的行不会在下次启动复活。
//
// 数据来源：2026-09-20 核对各厂官方定价页（智谱 BigModel、DeepSeek API Docs、
// 腾讯云 TokenHub 价格页、Moonshot 开放平台、MiniMax 定价页），单位已统一换算为
// 元 / 百万 token，字段顺序即 ModelPrice 的 输入未命中 / 输出 / 缓存读 / 缓存写。
//
// 已知取舍（单值表无法表达的地方，一律取「上界」，宁可高估不低估）：
//   - 分时定价：DeepSeek 高峰（工作日 9-12、14-18）价为空闲价两倍，此处取高峰价；
//     要按空闲价可自行减半。
//   - 分档定价：GLM-5.1 / GLM-5 / GLM-5-Turbo / GLM-5V-Turbo 按输入长度分档（32K 为界），
//     单值表表达不了，故不预置，需要时按自己的实际输入长度填。
//   - 缓存写：各厂对缓存存储多为限时免费或未单独报价，一律记 0。
//   - miniMax-m3 的缓存读 0.42 = 官方原价 0.84 按「永久五折」同比例推算（当前定价页未单列缓存读）。
//   - auto 是客户端路由别名（背后模型不定），kimi-k2.8-preview / hy3-preview 未见公开价，
//     海外系列（GPT / Gemini）为美元报价且与国内人民币定价非同价换算，均不预置——
//     这些模型会如实显示「未定价」，而不是拿猜来的价糊上去。
//   - 单价随官方调价变化，这里只是让功能开箱有数的起点，长期请以官方定价页为准。
func DefaultModelPrices() map[string]ModelPrice {
	return map[string]ModelPrice{
		// 腾讯混元（腾讯云 TokenHub）
		"hy3":          {Input: 1, Output: 4, CacheRead: 0.25},
		"hy4-preview":  {Input: 6, Output: 18, CacheRead: 0.3},
		// 智谱 GLM（BigModel 原价；GLM-5.3-Flash 的限时五折 2026-09-09 已截止）
		"glm-5.3":       {Input: 8, Output: 28, CacheRead: 2},
		"glm-5.2":       {Input: 8, Output: 28, CacheRead: 2},
		"glm-5.3-flash": {Input: 0.8, Output: 2.8, CacheRead: 0.23},
		// DeepSeek 官方（高峰价；旧名 deepseek-v4-flash 仍由 V4.1-Flash 服务并按 Flash 价计费）
		"deepseek-flash":      {Input: 2, Output: 8, CacheRead: 0.04},
		"deepseek-v4-flash":   {Input: 2, Output: 8, CacheRead: 0.04},
		"deepseek-v4.1-flash": {Input: 2, Output: 8, CacheRead: 0.04},
		"deepseek-v4-pro":     {Input: 9, Output: 27, CacheRead: 0.3},
		// Moonshot Kimi（kimi-k2.7 即官方 K2.7 Code 档）
		"kimi-k3":   {Input: 20, Output: 100, CacheRead: 2},
		"kimi-k2.7": {Input: 6.5, Output: 27, CacheRead: 1.3},
		"kimi-k2.6": {Input: 6.5, Output: 27, CacheRead: 1.1},
		// MiniMax（永久五折后价）
		"minimax-m3": {Input: 2.1, Output: 8.4, CacheRead: 0.42},
	}
}
