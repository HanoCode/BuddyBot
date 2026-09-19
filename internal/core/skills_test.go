package core

import (
	"fmt"
	"testing"
	"time"
)

func TestParseSkillFrontmatter(t *testing.T) {
	md := `---
name: 编程专家
description: P8级全栈编程专家，支持 API 设计与 Bug 诊断。
version: 1.21.7
---

# 正文`
	name, desc := parseSkillFrontmatter(md)
	if name != "编程专家" {
		t.Fatalf("name = %q", name)
	}
	if desc != "P8级全栈编程专家，支持 API 设计与 Bug 诊断。" {
		t.Fatalf("desc = %q", desc)
	}
	// 无 frontmatter
	if n, d := parseSkillFrontmatter("# 纯正文\n内容"); n != "" || d != "" {
		t.Fatalf("期望空值，got %q %q", n, d)
	}
}

func TestSortSkillHubSkills(t *testing.T) {
	skills := []SkillHubSkill{
		{Name: "a", Downloads: 1, UpdatedAt: 100, Score: 0.1},
		{Name: "b", Downloads: 30, UpdatedAt: 300, Score: 0.3},
		{Name: "c", Downloads: 20, UpdatedAt: 200, Score: 0.2},
	}
	sortSkillHubSkills(skills, "hot")
	if skills[0].Name != "b" {
		t.Fatalf("hot 排序错误: %s", skills[0].Name)
	}
	sortSkillHubSkills(skills, "newest")
	if skills[0].Name != "b" {
		t.Fatalf("newest 排序错误: %s", skills[0].Name)
	}
	sortSkillHubSkills(skills, "score")
	if skills[0].Name != "b" {
		t.Fatalf("score 排序错误: %s", skills[0].Name)
	}
}

func TestUninstallSkillGuard(t *testing.T) {
	if err := UninstallSkill("claude-code", "../escape"); err == nil {
		t.Fatal("路径穿越应被拒绝")
	}
	if err := UninstallSkill("no-such-target", "abc"); err == nil {
		t.Fatal("未知目标应报错")
	}
}

func TestDetectSkillHubCLI(t *testing.T) {
	// 本机已安装 CLI 时应能探测到；未安装环境跳过
	s := DetectSkillHubCLI()
	if s.Installed && s.Version == "" {
		t.Fatal("已安装但版本为空")
	}
	t.Logf("skillhub: %+v", s)
}

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.0.0", "1.0.1", true},
		{"1.0.0", "1.1.0", true},
		{"1.9.16", "1.10.0", true}, // 数值比较而非字典序
		{"2.0.0", "1.9.9", false},
		{"1.0.0", "1.0.0", false},
		{"v1.0.0", "1.0.1", true},
		{"", "1.0.0", false}, // 未知版本不误报
		{"1.0", "1.0.1", true},
	}
	for _, c := range cases {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestSkillsCacheGet(t *testing.T) {
	calls := 0
	fetch := func() (string, error) { calls++; return fmt.Sprintf("v%d", calls), nil }

	// 首次：拉取
	v, _, fromCache, err := skillsCacheGet("test:k1", time.Hour, false, fetch)
	if err != nil || fromCache || v != "v1" {
		t.Fatalf("首次应拉取: v=%s fromCache=%v err=%v", v, fromCache, err)
	}
	// TTL 内：命中缓存
	v, _, fromCache, err = skillsCacheGet("test:k1", time.Hour, false, fetch)
	if err != nil || !fromCache || v != "v1" {
		t.Fatalf("TTL 内应命中缓存: v=%s fromCache=%v err=%v", v, fromCache, err)
	}
	if calls != 1 {
		t.Fatalf("fetch 不应被再次调用，calls=%d", calls)
	}
	// force：绕过缓存
	v, _, fromCache, _ = skillsCacheGet("test:k1", time.Hour, true, fetch)
	if fromCache || v != "v2" {
		t.Fatalf("force 应重新拉取: v=%s fromCache=%v", v, fromCache)
	}
	// TTL 过期：重新拉取
	skillsCacheMu.Lock()
	skillsCacheData["test:k2"] = skillsCacheEntry{value: "old", fetchedAt: time.Now().Add(-2 * time.Hour)}
	skillsCacheMu.Unlock()
	v, _, fromCache, _ = skillsCacheGet("test:k2", time.Hour, false, fetch)
	if fromCache || v != "v3" {
		t.Fatalf("过期应重新拉取: v=%s fromCache=%v", v, fromCache)
	}
	// fetch 失败：回退过期旧值
	v, _, fromCache, err = skillsCacheGet("test:k2", time.Hour, false, func() (string, error) { return "", fmt.Errorf("boom") })
	if err != nil || !fromCache || v != "v3" {
		t.Fatalf("拉取失败应回退旧值: v=%s fromCache=%v err=%v", v, fromCache, err)
	}
	// 失效
	skillsCacheInvalidate("test:")
	if _, _, fromCache, _ = skillsCacheGet("test:k1", time.Hour, false, fetch); fromCache {
		t.Fatal("失效后不应再命中缓存")
	}
	skillsCacheInvalidate("test:")
}
