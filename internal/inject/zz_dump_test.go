package inject

import (
	"strings"
	"testing"

	"workbuddy-desktop/internal/core"
)

// TestThemeCSSIntegrity 回归测试：生成的主题 CSS 必须能被浏览器完整解析。
// 曾因补丁提取脚本混入多余单引号，CSS 解析器把后续规则当字符串吞掉，
// 导致壁纸规则（#root）与大部分补丁失效（页面 CSSOM 只剩 31/171 条规则）。
func TestThemeCSSIntegrity(t *testing.T) {
	m := &Manager{}
	cases := []struct {
		id        string
		wallpaper string
	}{
		{"dark", "preset:wallpaper-08"},
		{"default", "preset:wallpaper-01"},
		{"eye-care", "preset:wallpaper-01"},
		{"eye-care", ""},
		{"cyber-purple", ""},
		{"glass", ""},
	}
	for _, c := range cases {
		css, err := m.buildThemeCSS(c.id, core.ThemeConfig{ID: c.id, Wallpaper: c.wallpaper, Mask: 30, TextShadow: true})
		if err != nil {
			t.Fatalf("%s: build: %v", c.id, err)
		}
		if n := strings.Count(css, "'"); n > 0 {
			t.Errorf("%s: CSS 含 %d 个单引号（会破坏 CSS 解析）", c.id, n)
		}
		if strings.Count(css, "{") != strings.Count(css, "}") {
			t.Errorf("%s: CSS 花括号不配对 {=%d }=%d", c.id, strings.Count(css, "{"), strings.Count(css, "}"))
		}
		if !strings.Contains(css, "#root{") && c.wallpaper != "" {
			t.Errorf("%s: 壁纸模式缺少 #root 背景规则", c.id)
		}
		// 壁纸模式必须含 .conversation-shell 半透明底规则：官方该容器为
		// var(--wb-home-bg-secondary,#fafafa) 不透明底，浅色模式下会整块盖住壁纸
		// （深色由 patch-89 接管，此规则不冲突）。
		if c.wallpaper != "" && !strings.Contains(css, ".conversation-shell{") {
			t.Errorf("%s: 壁纸模式缺少 .conversation-shell 半透明底规则（浅色主内容区会盖住壁纸）", c.id)
		}
		// 自定义主题色板必须定义 --wb-home-bg-*：官方 .conversation-shell、
		// .teams-container.is-mac 等容器局部引用该变量，缺失时回落官方硬编码值
		if themeIsCustom(c.id) {
			for _, v := range []string{"--wb-home-bg-primary:", "--wb-home-bg-secondary:", "--wb-bg-card:", "--wb-bg-content:"} {
				if !strings.Contains(css, v) {
					t.Errorf("%s: 色板缺少 %s（主内容区背景变量，缺失则主题不生效）", c.id, strings.TrimSuffix(v, ":"))
				}
			}
		}
	}
}
