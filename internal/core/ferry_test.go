package core

import "testing"

// 免费额度模型按「0 元」计入金额口径，不能落进「未定价」——
// 后者会让统计页提示用户去补一个根本不存在的价格。
func TestOverlayFreePrices(t *testing.T) {
	user := map[string]ModelPrice{"glm-5.2": {Input: 1, Output: 4}}

	// 没有免费模型：原样返回（不复制，也不动用户的表）
	if got := overlayFreePrices(user, nil); &got == nil || len(got) != len(user) || got["glm-5.2"].Input != 1 {
		t.Fatalf("无免费模型时应原样返回单价表: %+v", got)
	}

	got := overlayFreePrices(user, []string{"opencode/big-pickle", ""})
	if len(got) != 2 {
		t.Fatalf("空 id 不应进表: %+v", got)
	}
	// 免费模型：priced=true 且金额 0（与「未定价」严格区分）
	cost, priced := NewPriceTable(got).Cost("opencode/big-pickle", 1_000_000, 1_000_000, 0, 0)
	if !priced || cost != 0 {
		t.Fatalf("免费模型应 (0,true)，得到 (%v,%v)", cost, priced)
	}
	// 用户自己的单价不受影响
	if c, p := NewPriceTable(got).Cost("glm-5.2", 1_000_000, 0, 0, 0); !p || !almost(c, 1) {
		t.Fatalf("用户单价被改动了: cost=%v priced=%v", c, p)
	}
	// 表里没有的模型依然是「未定价」——叠加逻辑不得放宽口径
	if c, p := NewPriceTable(got).Cost("no-such-model", 1_000_000, 0, 0, 0); p || c != 0 {
		t.Fatalf("未定价口径被放宽了: cost=%v priced=%v", c, p)
	}
	// 输入 map 绝不能被就地修改
	if len(user) != 1 {
		t.Fatalf("入参单价表被污染: %+v", user)
	}
}

// 用户显式给同名模型配过单价时以用户配置为准（叠加不覆盖用户的选择）
func TestOverlayFreePricesRespectsUserEntry(t *testing.T) {
	user := map[string]ModelPrice{"opencode/x": {Input: 3}}
	got := overlayFreePrices(user, []string{"opencode/x"})
	if c, p := NewPriceTable(got).Cost("opencode/x", 1_000_000, 0, 0, 0); !p || !almost(c, 3) {
		t.Fatalf("用户显式单价被免费零价覆盖: cost=%v priced=%v", c, p)
	}
}

// 未启用免费额度时 EffectivePrices 必须原样落回用户单价表
func TestEffectivePricesWithoutFerry(t *testing.T) {
	svc := newTestService(t)
	cfg := svc.GetConfig()
	cfg.Models.Prices = map[string]ModelPrice{"glm-5.2": {Input: 1, Output: 4}}
	if err := svc.UpdateConfig(cfg); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	got := svc.EffectivePrices()
	if got["glm-5.2"].Input != 1 || len(got) != 1 {
		t.Fatalf("未启用免费额度时单价表应原样: %+v", got)
	}
	// 关掉的免费额度不能凭空造出模型（Enabled=false → 不启动隔离运行时）
	if _, ok := got["opencode/big-pickle"]; ok {
		t.Fatal("未启用免费额度时不应出现免费模型")
	}
}
