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
		// 壁纸模式必须透明化 5.6.0 新增的两层不透明白底容器：
		// .teams-grid-scroll-content 与 grid 布局容器（哈希类 _gridView_<hash>），
		// 缺失则壁纸被白底整块盖住
		if c.wallpaper != "" && (!strings.Contains(css, ".teams-grid-scroll-content") || !strings.Contains(css, `[class*="_gridView_"]`)) {
			t.Errorf("%s: 壁纸模式缺少 grid 新布局层透明化规则（5.6.0 壁纸会被盖住）", c.id)
		}
		// 自定义主题色板必须定义 --wb-home-bg-*：官方 .conversation-shell、
		// .teams-container.is-mac 等容器局部引用该变量，缺失时回落官方硬编码值
		if themeIsCustom(c.id) {
			for _, v := range []string{"--wb-home-bg-primary:", "--wb-home-bg-secondary:", "--wb-bg-card:", "--wb-bg-content:"} {
				if !strings.Contains(css, v) {
					t.Errorf("%s: 色板缺少 %s（主内容区背景变量，缺失则主题不生效）", c.id, strings.TrimSuffix(v, ":"))
				}
			}
			// 色板规则必须用 html body[data-vscode-theme-name]（0,1,2）压过官方
			// 深浅色规则（≤0,1,1）：裸 body[data-vscode-theme-name] 同优先级会被
			// 后加载的官方规则覆盖，深色下 --wb-bg-primary 被官方重定义为
			// var(--wb-palette-gray-3) 后与别名层成循环引用，壁纸层整体失效
			if !strings.Contains(css, "html body[data-vscode-theme-name]{--") {
				t.Errorf("%s: 色板选择器未用 html body[data-vscode-theme-name] 提升特异性（壁纸/色板会被官方规则覆盖）", c.id)
			}
			// patch-15 的 [class*="tooltip"] 会误伤发送按钮包装层（黑边回归哨兵）：
			// 必须存在三重同名类恢复透明规则，且位于补丁之后
			if c.id != "default" && c.id != "dark" && !strings.Contains(css, ".cr-send-button__tooltip-wrapper.cr-send-button__tooltip-wrapper") {
				t.Errorf("%s: 缺少发送按钮包装层透明化规则（patch-15 误伤 → 发送按钮黑边）", c.id)
			}
		}
	}
}
