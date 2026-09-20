package inject

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"workbuddy-desktop/internal/core"
)

// ============================================================
// WorkBuddy 换肤引擎（参考 WorkDaddy 主题系统的完整实现思路）
//
// 原理（逆向 WorkBuddy 主题机制）：
//  1. 设计 token（--wb-*、--dc-*、--vscode-*）定义在
//    `:root, body[data-vscode-theme-name="IDE Light"]` 联合选择器上，
//     部分容器还有局部硬编码覆盖 —— 只改 :root 无效；注入样式在 head 中
//     可能先于官方 CSS 加载，色板须用 html body[data-vscode-theme-name]
//     更高特异性稳赢，不能赌「同优先级后插入」。
//  2. WorkBuddy 自带深色模式（html[data-theme="dark"]/html.cb-dark/
//     body[data-vscode-theme-name="IDE Night"]），深色主题先切官方深色
//     （局部变量随之变深）再注入自定义色板；浅色主题只注入色板。
//  3. 原生外观联动：写 localStorage 'agent-ui-theme' 与
//     'workbuddy.appearance.*' 键并同步根属性，否则 React 加载后会按
//     账号旧偏好把主题「弹回」；MutationObserver 守护根属性兜底。
//  4. 壁纸铺在 #root 上（蒙版渐变 + cover），容器透明化让图透出，
//     顶栏/侧栏/输入框等局部保留毛玻璃。
// ============================================================

// themeDef 内置主题定义（default/dark 仅联动官方外观，无色板）
type themeDef struct {
	ID    string
	Name  string
	Dark  bool
	Color map[string]string
}

var builtinThemes = map[string]themeDef{
	"default": {ID: "default", Name: "官方浅色", Dark: false},
	"dark":    {ID: "dark", Name: "官方深色", Dark: true},
	"eye-care": {ID: "eye-care", Name: "护眼绿", Dark: false, Color: map[string]string{
		"--vscode-editor-background": "#f0f5ec", "--vscode-editor-foreground": "#2b3a26",
		"--vscode-sideBar-background": "#e7efe0", "--vscode-sideBar-foreground": "#3b4a36", "--vscode-sideBar-border": "#d9e3cf",
		"--vscode-activityBar-background": "#e7efe0", "--vscode-activityBar-foreground": "#2b3a26",
		"--vscode-activityBar-inactiveForeground": "rgba(43,58,38,0.5)",
		"--vscode-activityBarBadge-background":    "#3b6d11", "--vscode-activityBarBadge-foreground": "#ffffff",
		"--vscode-titleBar-activeBackground": "#f0f5ec", "--vscode-titleBar-activeForeground": "#2b3a26",
		"--vscode-tab-activeBackground": "#f0f5ec", "--vscode-tab-activeForeground": "#2b3a26",
		"--vscode-tab-inactiveBackground": "#e7efe0", "--vscode-tab-inactiveForeground": "rgba(43,58,38,0.5)",
		"--vscode-tab-border": "#d9e3cf",
		"--vscode-input-background": "#ffffff", "--vscode-input-foreground": "#2b3a26",
		"--vscode-input-border": "#c3d2b5", "--vscode-input-placeholderForeground": "rgba(43,58,38,0.45)",
		"--vscode-button-background": "#3b6d11", "--vscode-button-foreground": "#ffffff",
		"--vscode-button-hoverBackground": "#4a8517",
		"--vscode-list-activeSelectionBackground": "rgba(59,109,17,0.12)", "--vscode-list-activeSelectionForeground": "#2b3a26",
		"--vscode-list-hoverBackground": "rgba(59,109,17,0.07)", "--vscode-list-inactiveSelectionBackground": "rgba(59,109,17,0.08)",
		"--vscode-menu-background": "#ffffff", "--vscode-menu-foreground": "#2b3a26",
		"--vscode-dropdown-background": "#ffffff", "--vscode-dropdown-foreground": "#2b3a26", "--vscode-dropdown-border": "#c3d2b5",
		"--vscode-panel-background": "#f0f5ec", "--vscode-panel-border": "#d9e3cf",
		"--vscode-badge-background": "#3b6d11", "--vscode-badge-foreground": "#ffffff",
		"--vscode-foreground": "#2b3a26", "--vscode-descriptionForeground": "rgba(43,58,38,0.7)",
		"--vscode-focusBorder":  "rgba(59,109,17,0.5)",
		"--vscode-scrollbarSlider-background":     "rgba(43,58,38,0.2)", "--vscode-scrollbarSlider-hoverBackground": "rgba(43,58,38,0.3)",
		"--vscode-editorGroupHeader-tabsBackground": "#e7efe0", "--vscode-editorGroupHeader-tabsBorder": "#d9e3cf",
		"--vscode-editorGroup-border": "#d9e3cf", "--vscode-statusBar-background": "#e7efe0", "--vscode-statusBar-foreground": "#2b3a26",
		"--vscode-checkbox-background": "#ffffff", "--vscode-checkbox-border": "#c3d2b5", "--vscode-checkbox-foreground": "#2b3a26",
		"--vscode-editorWidget-background": "#ffffff", "--vscode-editorWidget-border": "#c3d2b5",
		"--wb-bg-primary": "#f0f5ec", "--wb-bg-secondary": "#e7efe0", "--wb-bg-tertiary": "#dce7d3",
		"--wb-bg-card": "#f5f9f1", "--wb-bg-content": "#f0f5ec",
		"--wb-home-bg-primary": "#f0f5ec", "--wb-home-bg-secondary": "#e7efe0",
		"--wb-bg-popover": "#f5f9f1", "--wb-bg-hover": "color-mix(in srgb,#3b6d11 6%,transparent)",
		"--wb-bg-active": "color-mix(in srgb,#3b6d11 10%,transparent)",
		"--wb-text-strong": "#2b3a26", "--wb-text-medium": "rgba(43,58,38,0.72)",
		"--wb-text-muted": "rgba(43,58,38,0.42)", "--wb-text-weak": "rgba(43,58,38,0.55)",
		"--wb-color-text-primary": "#2b3a26", "--wb-color-text-secondary": "rgba(43,58,38,0.72)",
		"--wb-color-text-tertiary": "rgba(43,58,38,0.55)", "--wb-color-text-disabled": "rgba(43,58,38,0.42)",
		"--wb-border-default": "color-mix(in srgb,#3b6d11 14%,transparent)", "--wb-border-subtle": "#d9e3cf",
		"--wb-border-strong": "#c3d2b5", "--wb-border-hover": "color-mix(in srgb,#3b6d11 24%,transparent)",
		"--wb-button-primary-bg": "#3b6d11", "--wb-button-primary-fg": "#ffffff",
		"--wb-button-primary-bg-hover": "#4a8517",
		"--wb-status-success": "#3b8c2e", "--wb-status-warning": "#b8860b",
		"--wb-status-error": "#c0392b", "--wb-status-info": "#2e8b8b",
		"--wb-card-bg": "#f5f9f1", "--wb-kb-tabs-container-bg": "#e3ebda", "--wb-kb-tabs-container-border": "#d2dec6",
		"--dc-bg-primary": "#f0f5ec", "--dc-bg-secondary": "#e7efe0", "--dc-bg-tertiary": "#dce7d3",
		"--dc-bg-hover": "#dfe9d5", "--dc-text-primary": "rgba(43,58,38,0.88)",
		"--dc-text-secondary": "rgba(43,58,38,0.62)", "--dc-border": "rgba(59,109,17,0.15)",
		"--dc-border-light": "rgba(59,109,17,0.09)", "--dc-card-bg": "#f5f9f1",
		"--dc-primary": "#3b6d11", "--dc-primary-hover": "#4a8517", "--dc-btn-text": "#ffffff",
		"--wb-icon-secondary": "rgba(43,58,38,0.65)", "--wb-color-link": "#3b6d11",
	}},
	"cyber-purple": {ID: "cyber-purple", Name: "赛博紫", Dark: true, Color: map[string]string{
		"--vscode-editor-background": "#12101e", "--vscode-editor-foreground": "#e8e5ff",
		"--vscode-sideBar-background": "#151227", "--vscode-sideBar-foreground": "#d6d2f0", "--vscode-sideBar-border": "#2a2450",
		"--vscode-activityBar-background": "#151227", "--vscode-activityBar-foreground": "#e8e5ff",
		"--vscode-activityBar-inactiveForeground": "rgba(232,229,255,0.45)",
		"--vscode-activityBarBadge-background":    "#7f77dd", "--vscode-activityBarBadge-foreground": "#ffffff",
		"--vscode-titleBar-activeBackground": "#12101e", "--vscode-titleBar-activeForeground": "#e8e5ff",
		"--vscode-tab-activeBackground": "#12101e", "--vscode-tab-activeForeground": "#e8e5ff",
		"--vscode-tab-inactiveBackground": "#1a1729", "--vscode-tab-inactiveForeground": "rgba(232,229,255,0.5)",
		"--vscode-tab-border": "#2a2450",
		"--vscode-input-background": "#1a1729", "--vscode-input-foreground": "#e8e5ff",
		"--vscode-input-border": "#3a3160", "--vscode-input-placeholderForeground": "rgba(232,229,255,0.4)",
		"--vscode-button-background": "#7f77dd", "--vscode-button-foreground": "#ffffff",
		"--vscode-button-hoverBackground": "#938ce6",
		"--vscode-list-activeSelectionBackground": "rgba(127,119,221,0.28)", "--vscode-list-activeSelectionForeground": "#ffffff",
		"--vscode-list-hoverBackground": "rgba(127,119,221,0.14)", "--vscode-list-inactiveSelectionBackground": "rgba(127,119,221,0.18)",
		"--vscode-menu-background": "#1a1729", "--vscode-menu-foreground": "#e8e5ff",
		"--vscode-dropdown-background": "#1a1729", "--vscode-dropdown-foreground": "#e8e5ff", "--vscode-dropdown-border": "#3a3160",
		"--vscode-panel-background": "#12101e", "--vscode-panel-border": "#2a2450",
		"--vscode-badge-background": "#7f77dd", "--vscode-badge-foreground": "#ffffff",
		"--vscode-foreground": "#e8e5ff", "--vscode-descriptionForeground": "rgba(232,229,255,0.7)",
		"--vscode-focusBorder":  "rgba(159,148,235,0.5)",
		"--vscode-scrollbarSlider-background":     "rgba(159,148,235,0.25)", "--vscode-scrollbarSlider-hoverBackground": "rgba(159,148,235,0.4)",
		"--vscode-editorGroupHeader-tabsBackground": "#151227", "--vscode-editorGroupHeader-tabsBorder": "#2a2450",
		"--vscode-editorGroup-border": "#2a2450", "--vscode-statusBar-background": "#151227", "--vscode-statusBar-foreground": "#e8e5ff",
		"--vscode-checkbox-background": "#1a1729", "--vscode-checkbox-border": "#3a3160", "--vscode-checkbox-foreground": "#e8e5ff",
		"--vscode-editorWidget-background": "#1a1729", "--vscode-editorWidget-border": "#3a3160",
		"--wb-bg-primary": "#12101e", "--wb-bg-secondary": "#1a1729", "--wb-bg-tertiary": "#221d35",
		"--wb-bg-card": "#1a1729", "--wb-bg-content": "#12101e",
		"--wb-home-bg-primary": "#12101e", "--wb-home-bg-secondary": "#1a1729",
		"--wb-bg-popover": "#1a1729", "--wb-bg-hover": "color-mix(in srgb,#7f77dd 10%,transparent)",
		"--wb-bg-active": "color-mix(in srgb,#7f77dd 16%,transparent)",
		"--wb-text-strong": "#e8e5ff", "--wb-text-medium": "rgba(232,229,255,0.75)",
		"--wb-text-muted": "rgba(232,229,255,0.45)", "--wb-text-weak": "rgba(232,229,255,0.58)",
		"--wb-color-text-primary": "#e8e5ff", "--wb-color-text-secondary": "rgba(232,229,255,0.75)",
		"--wb-color-text-tertiary": "rgba(232,229,255,0.58)", "--wb-color-text-disabled": "rgba(232,229,255,0.45)",
		"--wb-border-default": "color-mix(in srgb,#7f77dd 20%,transparent)", "--wb-border-subtle": "#262140",
		"--wb-border-strong": "#3a3160", "--wb-border-hover": "color-mix(in srgb,#a99ff0 30%,transparent)",
		"--wb-button-primary-bg": "#7f77dd", "--wb-button-primary-fg": "#ffffff",
		"--wb-button-primary-bg-hover": "#938ce6",
		"--wb-status-success": "#5ddfb0", "--wb-status-warning": "#f2b94d",
		"--wb-status-error": "#f27e9b", "--wb-status-info": "#7fd0e8",
		"--wb-card-bg": "#1a1729", "--wb-kb-tabs-container-bg": "#151227", "--wb-kb-tabs-container-border": "#2a2450",
		"--dc-bg-primary": "#12101e", "--dc-bg-secondary": "#1a1729", "--dc-bg-tertiary": "#221d35",
		"--dc-bg-hover": "#241f3c", "--dc-text-primary": "rgba(255,255,255,0.88)",
		"--dc-text-secondary": "rgba(255,255,255,0.62)", "--dc-text-tertiary": "rgba(255,255,255,0.42)",
		"--dc-border": "rgba(127,119,221,0.28)", "--dc-border-light": "rgba(127,119,221,0.16)",
		"--dc-card-bg": "#1a1729", "--dc-primary": "#7f77dd", "--dc-primary-hover": "#938ce6",
		"--dc-btn-text": "#ffffff",
		"--wb-icon-secondary": "rgba(232,229,255,0.7)", "--wb-color-link": "#a99ff0",
	}},
	// 毛玻璃：近黑白深色板 + 壁纸透出，层次由蒙版/局部毛玻璃呈现
	"glass": {ID: "glass", Name: "毛玻璃", Dark: true, Color: map[string]string{
		"--vscode-editor-background": "#0a0a0a", "--vscode-editor-foreground": "#f2f2f4",
		"--vscode-sideBar-background": "#0c0c0e", "--vscode-sideBar-foreground": "#d0d0d4", "--vscode-sideBar-border": "#1c1c22",
		"--vscode-activityBar-background": "#0c0c0e", "--vscode-activityBar-foreground": "#f2f2f4",
		"--vscode-activityBar-inactiveForeground": "rgba(242,242,244,0.45)",
		"--vscode-activityBarBadge-background":    "#f2f2f4", "--vscode-activityBarBadge-foreground": "#0a0a0a",
		"--vscode-titleBar-activeBackground": "#0a0a0a", "--vscode-titleBar-activeForeground": "#f2f2f4",
		"--vscode-tab-activeBackground": "#0a0a0a", "--vscode-tab-activeForeground": "#f2f2f4",
		"--vscode-tab-inactiveBackground": "#111113", "--vscode-tab-inactiveForeground": "rgba(242,242,244,0.5)",
		"--vscode-tab-border": "rgba(255,255,255,0.1)",
		"--vscode-input-background": "#111113", "--vscode-input-foreground": "#f2f2f4",
		"--vscode-input-border": "rgba(255,255,255,0.18)", "--vscode-input-placeholderForeground": "rgba(242,242,244,0.4)",
		"--vscode-button-background": "rgba(255,255,255,0.92)", "--vscode-button-foreground": "#0a0a0a",
		"--vscode-button-hoverBackground": "rgba(255,255,255,0.8)",
		"--vscode-list-activeSelectionBackground": "rgba(255,255,255,0.14)", "--vscode-list-activeSelectionForeground": "#ffffff",
		"--vscode-list-hoverBackground": "rgba(255,255,255,0.07)", "--vscode-list-inactiveSelectionBackground": "rgba(255,255,255,0.09)",
		"--vscode-menu-background": "#111113", "--vscode-menu-foreground": "#f2f2f4",
		"--vscode-dropdown-background": "#111113", "--vscode-dropdown-foreground": "#f2f2f4", "--vscode-dropdown-border": "rgba(255,255,255,0.18)",
		"--vscode-panel-background": "#0a0a0a", "--vscode-panel-border": "rgba(255,255,255,0.1)",
		"--vscode-badge-background": "#f2f2f4", "--vscode-badge-foreground": "#0a0a0a",
		"--vscode-foreground": "#f2f2f4", "--vscode-descriptionForeground": "rgba(242,242,244,0.7)",
		"--vscode-focusBorder":  "rgba(255,255,255,0.55)",
		"--vscode-scrollbarSlider-background":     "rgba(255,255,255,0.2)", "--vscode-scrollbarSlider-hoverBackground": "rgba(255,255,255,0.35)",
		"--vscode-editorGroupHeader-tabsBackground": "#0c0c0e", "--vscode-editorGroupHeader-tabsBorder": "rgba(255,255,255,0.1)",
		"--vscode-editorGroup-border": "#1c1c22", "--vscode-statusBar-background": "#0c0c0e", "--vscode-statusBar-foreground": "#f2f2f4",
		"--vscode-checkbox-background": "#111113", "--vscode-checkbox-border": "rgba(255,255,255,0.25)", "--vscode-checkbox-foreground": "#f2f2f4",
		"--vscode-editorWidget-background": "#111113", "--vscode-editorWidget-border": "rgba(255,255,255,0.18)",
		"--wb-bg-primary": "#0a0a0a", "--wb-bg-secondary": "#111113", "--wb-bg-tertiary": "#1b1b20",
		"--wb-bg-card": "#111113", "--wb-bg-content": "#0a0a0a",
		"--wb-home-bg-primary": "#0a0a0a", "--wb-home-bg-secondary": "#111113",
		"--wb-bg-popover": "#111113", "--wb-bg-hover": "color-mix(in srgb,#ffffff 7%,transparent)",
		"--wb-bg-active": "color-mix(in srgb,#ffffff 10%,transparent)", "--wb-bg-overlay": "rgba(0,0,0,0.7)",
		"--wb-text-strong": "#f2f2f4", "--wb-text-medium": "rgba(242,242,244,0.72)",
		"--wb-text-muted": "rgba(242,242,244,0.42)", "--wb-text-weak": "rgba(242,242,244,0.55)",
		"--wb-color-text-primary": "#f2f2f4", "--wb-color-text-secondary": "rgba(242,242,244,0.72)",
		"--wb-color-text-tertiary": "rgba(242,242,244,0.55)", "--wb-color-text-disabled": "rgba(242,242,244,0.42)",
		"--wb-border-default": "color-mix(in srgb,#ffffff 12%,transparent)", "--wb-border-subtle": "#202025",
		"--wb-border-strong": "#2c2c33", "--wb-border-hover": "color-mix(in srgb,#ffffff 25%,transparent)",
		"--wb-button-primary-bg": "rgba(255,255,255,0.92)", "--wb-button-primary-fg": "#0a0a0a",
		"--wb-button-primary-bg-hover": "rgba(255,255,255,0.8)",
		"--wb-status-success": "#2ee59d", "--wb-status-warning": "#ffb03a",
		"--wb-status-error": "#ff6b6b", "--wb-status-info": "#3fd6c0",
		"--wb-card-bg": "#111113", "--wb-kb-tabs-container-bg": "#101013", "--wb-kb-tabs-container-border": "#1e1e24",
		"--dc-bg-primary": "#0a0a0a", "--dc-bg-secondary": "#111113", "--dc-bg-tertiary": "#1b1b20",
		"--dc-bg-hover": "#1d1d22", "--dc-text-primary": "rgba(255,255,255,0.88)",
		"--dc-text-secondary": "rgba(255,255,255,0.62)", "--dc-text-tertiary": "rgba(255,255,255,0.42)",
		"--dc-border": "rgba(255,255,255,0.12)", "--dc-border-light": "rgba(255,255,255,0.07)",
		"--dc-card-bg": "#111113", "--dc-primary": "#ffffff", "--dc-primary-hover": "#e0e0e0",
		"--dc-btn-text": "#0a0a0a",
		"--wb-icon-secondary": "rgba(242,242,244,0.7)", "--wb-color-link": "#f2f2f4",
	}},
}

// themeOrder 面板外观分段的展示顺序
var themeOrder = []string{"default", "dark", "eye-care", "cyber-purple", "glass"}

// themeIsCustom 非 default/dark 的主题需要注入色板与补丁
func themeIsCustom(id string) bool { return id != "default" && id != "dark" }

// ---------- CSS 生成 ----------

// themeVarsCSS 色板层：html body[data-vscode-theme-name]（0,1,2）压过官方深浅色
// 规则（最高 0,1,1）。必须用更高特异性：注入 <style> 在 head 中可能先于官方
// CSS（LINK/后插 style），同优先级会按官方后加载胜出 —— 曾因此整块色板被
// 官方 5.6.x 深色规则覆盖，--wb-bg-primary 又被官方重定义为 var(--wb-palette-gray-3)，
// 与别名层 --wb-palette-gray-3:var(--wb-bg-primary) 成循环引用双双失效，
// 壁纸层 #root 背景在计算值阶段整条被丢弃（壁纸不显示）。
func themeVarsCSS(t themeDef) string {
	var b strings.Builder
	b.WriteString("html body[data-vscode-theme-name]{")
	for k, v := range t.Color {
		b.WriteString(k + ":" + v + ";")
	}
	b.WriteString("}")
	return b.String()
}

// themeAliasCSS 变量别名层（对应 WorkDaddy theme-vars.js）：
// 官方深色下漏定义/值不对的 token 重定向到主题变量；darkOnly 条目仅深色注入。
func themeAliasCSS(isDark bool) string {
	const P = `body[data-vscode-theme-name] `
	const PD = `html[data-theme="dark"] body[data-vscode-theme-name] `
	var b strings.Builder
	if isDark {
		// body 级 darkOnly：官方 .cb-* 组件变量深色下仍是浅色值
		b.WriteString(PD + `{` +
			`--cb-bg-secondary:var(--wb-bg-tertiary);` +
			`--cb-border:var(--wb-border-subtle);` +
			`--cb-text-primary:var(--wb-color-text-primary);` +
			`--cb-text-tertiary:var(--wb-color-text-secondary);` +
			`--wb-palette-gray-3:var(--wb-bg-primary);` +
			`--qad-card-bg:var(--wb-bg-secondary);` +
			`--qad-question-color:var(--wb-color-text-secondary);` +
			`--qad-answer-color:var(--wb-color-text-primary);}`)
	}
	// 组件作用域级（官方在这些组件上定义了局部硬编码，须同作用域重定向）
	b.WriteString(P + `.cb-markdown{` +
		`--cb-markdown-table-cell-bg:var(--wb-bg-primary);` +
		`--cb-markdown-table-header-bg:var(--wb-bg-secondary);` +
		`--cb-markdown-table-border-color:var(--wb-border-strong);` +
		`--cb-markdown-border-color:var(--wb-border-strong);` +
		`--cb-markdown-code-block-header-bg:var(--wb-bg-secondary);` +
		`--cb-markdown-code-block-title-fg:var(--wb-color-text-primary);` +
		`--cb-markdown-code-block-action-fg:var(--wb-color-text-secondary);` +
		`--cb-markdown-code-block-action-hover-bg:var(--wb-bg-hover);` +
		`--cb-markdown-code-block-border:var(--wb-border-subtle);` +
		`--cb-markdown-code-block-bg:var(--wb-bg-tertiary);}`)
	b.WriteString(P + `[class*="input-area-container"]::before{--cb-colleagues-dashboard-bg:var(--wb-bg-primary);}`)
	if isDark {
		b.WriteString(PD + `.cb-markdown{` +
			`--cb-markdown-table-border-color:transparent !important;` +
			`--cb-markdown-border-color:transparent !important;}`)
		b.WriteString(PD + `[class*="_questionAnswerDisplay_"],[class*="_qaQuestion_"],[class*="_qaAnswerText_"],[class*="_qaAnswer_"]{` +
			`--qad-card-bg:var(--wb-bg-secondary) !important;` +
			`--qad-question-color:var(--wb-color-text-secondary) !important;` +
			`--qad-answer-color:var(--wb-color-text-primary) !important;}`)
	}
	return b.String()
}

// themeLocalOverridesCSS 局部容器覆盖（对应 WorkDaddy LOCAL_THEME_OVERRIDES）：
// 对已知硬编码容器追加同层变量（body[data-vscode-theme-name] 提升优先级）
func themeLocalOverridesCSS(t themeDef) string {
	type loc struct {
		sel  string
		vars []string
	}
	overrides := []loc{
		{".teams-container.is-mac", []string{"--wb-home-bg-primary", "--wb-home-bg-secondary"}},
		{".project-detail-view__chat-input", []string{"--wb-bg-primary"}},
		{".project-detail-view__chat-input--task", []string{"--wb-bg-primary", "--wb-color-border-secondary"}},
		{"[class*=\"mainArea\"]", []string{"--wb-bg-hover"}},
		{".workbuddy-collab", []string{"--wb-border-info", "--wb-bg-info", "--wb-bg-action"}},
	}
	var b strings.Builder
	for _, o := range overrides {
		var parts []string
		for _, v := range o.vars {
			if c, ok := t.Color[v]; ok {
				parts = append(parts, v+":"+c+";")
			}
		}
		if len(parts) > 0 {
			b.WriteString("body[data-vscode-theme-name] " + o.sel + "{" + strings.Join(parts, "") + "}")
		}
	}
	return b.String()
}

// themeExtrasCSS 样式补丁集（theme_patches_gen.go，WorkDaddy theme-patches.js 全量移植）+
// 文字阴影补丁（对应 patch-101，按开关启用）
func themeExtrasCSS(textShadow bool) string {
	var b strings.Builder
	for _, p := range themePatches {
		// WorkDaddy 的自定义主题标记属性统一改成本项目的命名空间
		b.WriteString(strings.ReplaceAll(p.CSS, `html[data-wbs-theme="1"]`, `html[data-wbdesk-theme="1"]`))
	}
	// 修复 patch-15 的 [class*="tooltip"] 子串误伤：.cr-send-button__tooltip-wrapper
	// 是发送按钮的定位包装层（官方本身透明，tooltip 气泡在其内部另行渲染），
	// 被 patch-15 当成弹层涂上不透明 popover 底色 —— 深色主题下圆形发送按钮
	// 后面出现方形黑块。三重同名类把特异性抬到 (0,5,2)，稳压 patch-15 的
	// (0,4,2)（!important 同级，同表靠后胜出）。
	b.WriteString(`html[data-theme="dark"] body[data-vscode-theme-name] ` +
		`.cr-send-button__tooltip-wrapper.cr-send-button__tooltip-wrapper.cr-send-button__tooltip-wrapper` +
		`{background:transparent !important;}`)
	if textShadow {
		b.WriteString(`body[data-vscode-theme-name] .conversation-timeline .cr-document,` +
			`body[data-vscode-theme-name] .conversation-timeline .cr-document *{` +
			`text-shadow:0 1px 2px rgba(0,0,0,.65),0 0 6px rgba(0,0,0,.3) !important;}`)
	}
	return b.String()
}

// themeBgCSS 壁纸背景层（与 WorkDaddy daemon.js bgCssStr 逐条一致）：
// #root 铺图（全局蒙版 + 左侧菜单渐变 + 底部渐变）+ 容器透明化 + 输入框毛玻璃。
func themeBgCSS(dataURL string, mask, blur int) string {
	m := float64(clampInt(mask, 0, 100)) / 100
	blurPx := float64(clampInt(blur, 0, 100)) / 100 * 32
	blurCSS := "backdrop-filter:none;-webkit-backdrop-filter:none;"
	if blurPx > 0 {
		blurCSS = fmt.Sprintf("backdrop-filter:blur(%.1fpx) saturate(1.15);-webkit-backdrop-filter:blur(%.1fpx) saturate(1.15);", blurPx, blurPx)
	}
	const P = `body[data-vscode-theme-name] `
	var b strings.Builder
	// WBSS 背景图方案：背景图铺 #root，容器透明 + 半透明毛玻璃让底图透出
	b.WriteString(`#root{background:` +
		fmt.Sprintf("linear-gradient(rgba(0,0,0,%.3f),rgba(0,0,0,%.3f)),", m, m) +
		"linear-gradient(90deg,color-mix(in srgb,var(--wb-bg-primary) 40%,transparent) 0 18%,transparent 42%)," +
		"linear-gradient(180deg,transparent 0 58%,color-mix(in srgb,var(--wb-bg-primary) 50%,transparent) 100%)," +
		"url(" + dataURL + ") right center / cover no-repeat fixed !important;}")
	b.WriteString(P + `.teams-container,` + P + `.teams-container.is-mac{background:transparent !important;` + blurCSS + `}`)
	// 5.6.0 新增布局层：grid 滚动容器 + grid 布局容器（react-grid 式绝对定位窗格，
	// 后者为 CSS Modules 哈希类 _gridView_<hash>，用子串匹配抗哈希变动）
	b.WriteString(P + `.teams-grid-scroll-content,` + P + `[class*="_gridView_"]{background:transparent !important;` + blurCSS + `}`)
	b.WriteString(P + `[data-view-id]{background:transparent !important}`)
	b.WriteString(P + `.main-content{background:transparent !important}`)
	// 左侧菜单（会话列表）透明：连同子组件全透明，背景图直接透出
	b.WriteString(P + `.conversation-list,` + P + `[data-view-id=sidebar]{background:transparent !important;` +
		`backdrop-filter:none !important;-webkit-backdrop-filter:none !important}`)
	// 输入框区域：毛玻璃背景（半透明 + 模糊，背景图透出）
	b.WriteString(P + `[class*="chat-input"]{background:color-mix(in srgb,var(--wb-bg-primary) 40%,transparent) !important;` +
		`backdrop-filter:blur(20px) saturate(1.15);-webkit-backdrop-filter:blur(20px) saturate(1.15)}`)
	// 主内容区底部渐变保证可读
	b.WriteString(P + `[data-view-id=main-content]{` +
		`background:linear-gradient(180deg,transparent 0 38%,color-mix(in srgb,var(--wb-bg-primary) 55%,transparent) 100%) !important}`)
	// 会话外壳：深色下 patch-89 给 45% 黑（其选择器含 html[data-theme="dark"]，特异性更高，
	// 本规则不会覆盖它）；浅色（官方浅色/护眼绿等）下 patch-89 不匹配，官方规则
	// .conversation-shell{background:var(--wb-home-bg-secondary,#fafafa)} 是不透明底，
	// 会整块盖住壁纸 —— 补一条浅色半透明底让壁纸透出，色调跟随主题。
	shellTint := "color-mix(in srgb,var(--wb-home-bg-secondary,#f2f3f5) 45%,transparent)"
	b.WriteString(P + `.conversation-shell{background:` + shellTint + ` !important;background-color:` + shellTint + ` !important;` +
		`backdrop-filter:none !important;-webkit-backdrop-filter:none !important;}`)
	return b.String()
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------- 壁纸存储 ----------

var customWallNameRe = regexp.MustCompile(`^custom-\d+\.webp$`)

// wallsDir 自定义壁纸目录：<数据目录>/inject/wallpapers
func (m *Manager) wallsDir() string {
	return filepath.Join(filepath.Dir(m.svc.ConfigPath()), "inject", "wallpapers")
}

// wallpaperDataURL 解析壁纸引用为 data URL（preset: 内嵌 / custom: 磁盘文件）
func (m *Manager) wallpaperDataURL(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("%s", m.tt("壁纸引用为空", "empty wallpaper reference"))
	}
	switch {
	case strings.HasPrefix(ref, "preset:"):
		id := strings.TrimPrefix(ref, "preset:")
		if !regexp.MustCompile(`^wallpaper-\d+$`).MatchString(id) {
			return "", fmt.Errorf("%s: %s", m.tt("非法的内置壁纸 ID", "invalid preset wallpaper ID"), id)
		}
		raw, err := wallFS.ReadFile("wallpapers/" + id + ".webp")
		if err != nil {
			return "", fmt.Errorf("%s: %s", m.tt("内置壁纸不存在", "preset wallpaper not found"), id)
		}
		return "data:image/webp;base64," + base64.StdEncoding.EncodeToString(raw), nil
	case strings.HasPrefix(ref, "custom:"):
		name := strings.TrimPrefix(ref, "custom:")
		if !customWallNameRe.MatchString(name) {
			return "", fmt.Errorf("%s: %s", m.tt("非法的自定义壁纸名", "invalid custom wallpaper name"), name)
		}
		raw, err := os.ReadFile(filepath.Join(m.wallsDir(), name))
		if err != nil {
			return "", fmt.Errorf("%s: %s", m.tt("自定义壁纸不存在", "custom wallpaper not found"), name)
		}
		return "data:image/webp;base64," + base64.StdEncoding.EncodeToString(raw), nil
	}
	return "", fmt.Errorf("%s: %s", m.tt("非法的壁纸引用", "invalid wallpaper reference"), ref)
}

// addCustomWallpaper 保存自定义壁纸文件并返回引用名（custom-<时间戳>.webp）
func (m *Manager) addCustomWallpaper(dataURL string) (string, error) {
	const marker = ";base64,"
	idx := strings.Index(dataURL, marker)
	if idx < 0 {
		return "", fmt.Errorf("%s", m.tt("壁纸数据格式无效", "invalid wallpaper data format"))
	}
	raw, err := base64.StdEncoding.DecodeString(dataURL[idx+len(marker):])
	if err != nil {
		return "", fmt.Errorf("%s: %w", m.tt("壁纸数据解码失败", "failed to decode wallpaper data"), err)
	}
	if len(raw) > 8<<20 {
		return "", fmt.Errorf("%s", m.tt("壁纸过大（超过 8MB）", "wallpaper too large (over 8MB)"))
	}
	if err := os.MkdirAll(m.wallsDir(), 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("custom-%d.webp", time.Now().UnixMilli())
	if err := os.WriteFile(filepath.Join(m.wallsDir(), name), raw, 0o600); err != nil {
		return "", err
	}
	return name, nil
}

// deleteCustomWallpaper 删除自定义壁纸文件（非法名直接拒绝）
func (m *Manager) deleteCustomWallpaper(name string) error {
	if !customWallNameRe.MatchString(name) {
		return fmt.Errorf("%s: %s", m.tt("非法的自定义壁纸名", "invalid custom wallpaper name"), name)
	}
	err := os.Remove(filepath.Join(m.wallsDir(), name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ---------- 主题应用（CDP） ----------

// buildThemeCSS 组装主题完整 CSS，顺序与 WorkDaddy 一致：
// 色板 → 局部容器覆盖 → 变量别名 → 样式补丁 → 壁纸背景。
// 补丁/壁纸在官方主题配壁纸时同样注入，否则壁纸被不透明容器遮挡。
func (m *Manager) buildThemeCSS(id string, th core.ThemeConfig) (string, error) {
	t, ok := builtinThemes[id]
	if !ok {
		return "", fmt.Errorf("%s: %s", m.tt("未知主题", "unknown theme"), id)
	}
	custom := themeIsCustom(id)
	var b strings.Builder
	if custom {
		b.WriteString(themeVarsCSS(t))
		b.WriteString(themeLocalOverridesCSS(t))
		b.WriteString(themeAliasCSS(t.Dark))
		b.WriteString(themeExtrasCSS(th.TextShadow))
	} else if th.Wallpaper != "" {
		// 官方主题下补丁引用的 --wb-* 变量未定义，补一组中性兜底色
		b.WriteString(themeFallbackVars(t))
		b.WriteString(themeExtrasCSS(th.TextShadow))
	}
	if th.Wallpaper != "" {
		url, err := m.wallpaperDataURL(th.Wallpaper)
		if err == nil {
			b.WriteString(themeBgCSS(url, th.Mask, th.Blur))
		}
	}
	return b.String(), nil
}

// themeFallbackVars 官方主题配壁纸时的最小 --wb-* 兜底色板（补丁/壁纸层依赖）。
// 必须包含 --wb-home-bg-*（主内容/侧栏背景用）与卡片/内容区变量，否则壁纸模式
// 下主内容仍是不透明底色、壁纸透不出来。
func themeFallbackVars(t themeDef) string {
	if t.Dark {
		return "body[data-vscode-theme-name]{--wb-bg-primary:#101216;--wb-bg-secondary:#17191f;" +
			"--wb-bg-tertiary:#1e222a;--wb-bg-popover:#1b1e24;--wb-bg-hover:rgba(255,255,255,0.07);" +
			"--wb-bg-card:#17191f;--wb-bg-content:#101216;" +
			"--wb-home-bg-primary:#101216;--wb-home-bg-secondary:#17191f;" +
			"--wb-color-text-primary:#f2f3f5;--wb-color-text-secondary:rgba(242,243,245,0.72);" +
			"--wb-color-text-tertiary:rgba(242,243,245,0.55);--wb-border-subtle:#262a31;--wb-border-strong:#333842;" +
			"--wb-icon-secondary:rgba(242,243,245,0.7);--wb-button-primary-bg:#e8eaee;--wb-button-primary-fg:#101216;}"
	}
	return "body[data-vscode-theme-name]{--wb-bg-primary:#ffffff;--wb-bg-secondary:#f2f3f5;" +
		"--wb-bg-tertiary:#e8eaee;--wb-bg-popover:#ffffff;--wb-bg-hover:rgba(31,36,48,0.06);" +
		"--wb-bg-card:#ffffff;--wb-bg-content:#ffffff;" +
		"--wb-home-bg-primary:#ffffff;--wb-home-bg-secondary:#f2f3f5;" +
		"--wb-color-text-primary:#1f2430;--wb-color-text-secondary:rgba(31,36,48,0.72);" +
		"--wb-color-text-tertiary:rgba(31,36,48,0.55);--wb-border-subtle:#e2e5ea;--wb-border-strong:#d0d4da;" +
		"--wb-icon-secondary:rgba(31,36,48,0.65);--wb-button-primary-bg:#1f2430;--wb-button-primary-fg:#ffffff;}"
}

// themeExpr 生成注入页面的主题应用表达式：
// 同步原生深浅色（localStorage 键 + 根属性）→ 注入/移除 <style> → MutationObserver 守护防弹回。
func themeExpr(id string, css string) string {
	custom := themeIsCustom(id)
	t, ok := builtinThemes[id]
	dark := ok && t.Dark
	wanted := "light"
	if dark {
		wanted = "dark"
	}
	return `(function(){
  var h=document.documentElement,b=document.body;
  if(!h||!b) return {pending:true};
  if(window.__wbdeskThemeGuard){try{window.__wbdeskThemeGuard.disconnect()}catch(e){}}
  try{var tv=` + boolJS(custom) + `?'1':'0';h.setAttribute('data-wbdesk-theme',tv);h.setAttribute('data-wbs-theme',tv);}catch(e){}
  try{var tid=` + jsonStr(id) + `;h.setAttribute('data-wbdesk-theme-id',tid);h.setAttribute('data-wbs-theme-id',tid);}catch(e){}
  function syncNative(mode){
    var isLight=mode==='light';
    var kind=isLight?'vscode-light':'vscode-dark';
    var name=isLight?'IDE Light':'IDE Night';
    try{
      localStorage.setItem('agent-ui-theme',JSON.stringify({theme:mode,followSystem:false,vsCodeThemeName:name,vsCodeThemeKind:kind}));
      localStorage.setItem('workbuddy.appearance.lastApplied',JSON.stringify({appearance:mode}));
      for(var i=0;i<localStorage.length;i++){
        var k=localStorage.key(i);
        if(typeof k!=='string')continue;
        if(k.indexOf('workbuddy.appearance.mode::')===0)localStorage.setItem(k,mode);
        else if(k.indexOf('workbuddy.appearance.state::')===0){try{localStorage.setItem(k,JSON.stringify({currentTheme:mode}))}catch(e2){}}
      }
    }catch(e1){}
    b.setAttribute('data-vscode-theme-kind',kind);
    b.setAttribute('data-vscode-theme-name',name);
    h.setAttribute('data-theme',mode);
  }
  h.classList.toggle('cb-dark',` + boolJS(dark) + `);
  b.classList.toggle('vscode-dark',` + boolJS(dark) + `);
  syncNative(` + jsonStr(wanted) + `);
  var s=document.getElementById('wbdesk-theme-style');
  var css=` + jsonStr(css) + `;
  if(css){
    var st=s||document.createElement('style');
    st.id='wbdesk-theme-style';
    if(st.textContent!==css)st.textContent=css;
    if(!s)(document.head||document.documentElement).appendChild(st);
  }else if(s){s.remove()}
  var wantedMode=` + jsonStr(wanted) + `;
  var wantedDark=wantedMode==='dark';
  var wantedKind=wantedDark?'vscode-dark':'vscode-light';
  var wantedName=wantedDark?'IDE Night':'IDE Light';
  function keep(){
    if(h.getAttribute('data-theme')===wantedMode&&h.classList.contains('cb-dark')===wantedDark&&
       b.getAttribute('data-vscode-theme-kind')===wantedKind&&b.getAttribute('data-vscode-theme-name')===wantedName&&
       b.classList.contains('vscode-dark')===wantedDark)return;
    h.classList.toggle('cb-dark',wantedDark);
    b.classList.toggle('vscode-dark',wantedDark);
    syncNative(wantedMode);
  }
  keep();
  if(typeof MutationObserver!=='undefined'){
    var g=new MutationObserver(keep);
    g.observe(h,{attributes:true,attributeFilter:['class','data-theme']});
    g.observe(b,{attributes:true,attributeFilter:['class','data-vscode-theme-kind','data-vscode-theme-name']});
    window.__wbdeskThemeGuard=g;
  }
  return {ok:true};
})()`
}

func boolJS(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func jsonStr(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// ApplyTheme 保存并应用主题（带重试：页面导航时旧连接短暂失效是正常竞态）
func (m *Manager) ApplyTheme(th core.ThemeConfig) error {
	if !themeIsCustom(th.ID) && th.ID != "default" && th.ID != "dark" {
		return fmt.Errorf("%s: %s", m.tt("未知主题", "unknown theme"), th.ID)
	}
	// 持久化（先存后应用：刷新/重启后 restoreTheme 能恢复）
	_ = m.svc.UpdateInjectConfig(func(c *core.InjectConfig) { c.Theme = th })
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("%s", m.tt("注入未运行", "injection not running"))
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if !conn.alive() {
			return fmt.Errorf("%s", m.tt("注入连接已断开，等待自动重连后重试", "injection connection lost; wait for auto-reconnect and retry"))
		}
		css, err := m.buildThemeCSS(th.ID, th)
		if err == nil {
			_, err = conn.evaluate(themeExpr(th.ID, css), 10*time.Second)
		}
		if err == nil {
			m.pushPanelTheme(conn)
			return nil
		}
		lastErr = err
		time.Sleep(time.Duration(250*(attempt+1)) * time.Millisecond)
	}
	return fmt.Errorf("%s: %w", m.tt("应用主题失败", "failed to apply theme"), lastErr)
}

// restoreTheme 注入/重连后恢复已保存主题（ID 为空=从未设置，不注入不守护）
func (m *Manager) restoreTheme() {
	th := m.svc.GetConfig().Inject.Theme
	if th.ID == "" {
		return
	}
	if err := m.ApplyTheme(th); err != nil {
		core.EmitEvent(core.EventInjectStatus, map[string]any{"kind": "theme_restore_failed", "message": err.Error()})
	}
}

// ---------- 面板主题状态通道 ----------

// panelThemeView 面板「主题」Tab 状态视图
type panelThemeView struct {
	ID         string `json:"id"`
	Wallpaper  string `json:"wallpaper"`
	Mask       int    `json:"mask"`
	Blur       int    `json:"blur"`
	TextShadow bool   `json:"textShadow"`
}

func (m *Manager) themeView() panelThemeView {
	th := m.svc.GetConfig().Inject.Theme
	return panelThemeView{ID: th.ID, Wallpaper: th.Wallpaper, Mask: th.Mask, Blur: th.Blur, TextShadow: th.TextShadow}
}

// pushPanelTheme 把主题状态推给面板
func (m *Manager) pushPanelTheme(conn *cdpConn) {
	raw, _ := json.Marshal(m.themeView())
	_, _ = conn.evaluate(`window.__wbdeskSetTheme && window.__wbdeskSetTheme(`+string(raw)+`)`, 5*time.Second)
}

// panelWalls 内置 + 自定义壁纸列表（dataURL）
func (m *Manager) panelWalls() []map[string]string {
	list := []map[string]string{}
	if entries, err := wallFS.ReadDir("wallpapers"); err == nil {
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".webp") {
				continue
			}
			raw, err := wallFS.ReadFile("wallpapers/" + name)
			if err != nil {
				continue
			}
			id := strings.TrimSuffix(name, ".webp")
			list = append(list, map[string]string{
				"id":   "preset:" + id,
				"name": m.tt("壁纸 ", "Wallpaper ") + strings.TrimPrefix(id, "wallpaper-"),
				"url":  "data:image/webp;base64," + base64.StdEncoding.EncodeToString(raw),
			})
		}
	}
	if entries, err := os.ReadDir(m.wallsDir()); err == nil {
		names := []string{}
		for _, e := range entries {
			if !e.IsDir() && customWallNameRe.MatchString(e.Name()) {
				names = append(names, e.Name())
			}
		}
		for i := len(names) - 1; i >= 0; i-- { // 新添加的在前
			raw, err := os.ReadFile(filepath.Join(m.wallsDir(), names[i]))
			if err != nil {
				continue
			}
			list = append(list, map[string]string{
				"id":   "custom:" + names[i],
				"name": m.tt("自定义壁纸", "Custom wallpaper"),
				"url":  "data:image/webp;base64," + base64.StdEncoding.EncodeToString(raw),
			})
		}
	}
	return list
}

// pushPanelWalls 推送壁纸库（内置 + 自定义）
func (m *Manager) pushPanelWalls() {
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn == nil {
		return
	}
	raw, _ := json.Marshal(m.panelWalls())
	_, _ = conn.evaluate(`window.__wbdeskSetWalls && window.__wbdeskSetWalls(`+string(raw)+`)`, 30*time.Second)
}
