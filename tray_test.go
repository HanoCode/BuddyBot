package main

import (
	"testing"

	"workbuddy-desktop/internal/core"
)

// 托盘文案必须只反映真实状态：这些用例锁住「有就显示、没有就不显示」的规则，
// 避免将来有人往托盘里塞固定文案或编造的统计数字。

func TestGatewayLabel(t *testing.T) {
	if got := gatewayLabel(true, ":7863"); got != "停止网关（监听 :7863）" {
		t.Fatalf("运行中文案 = %q", got)
	}
	if got := gatewayLabel(false, "127.0.0.1:9000"); got != "启动网关（127.0.0.1:9000）" {
		t.Fatalf("已停止文案 = %q", got)
	}
}

func TestPoolSummary(t *testing.T) {
	accounts := []core.Account{
		{UID: "a", Status: "online"},
		{UID: "b", Status: "online"},
		{UID: "c", Status: "cooldown"},
		{UID: "d", Status: "expired"},
		{UID: "e", Status: "disabled"},
		{UID: "f", Status: "unknown"},
	}
	got := poolSummary(accounts)
	want := "账号池 2/6 在线 · 冷却 1 · 过期 1 · 禁用 1"
	if got != want {
		t.Fatalf("账号池文案 = %q, want %q", got, want)
	}
}

func TestPoolSummaryOmitsZeroCategories(t *testing.T) {
	// 空账号池：不应该出现任何编造的分类数字
	if got := poolSummary(nil); got != "账号池 0/0 在线" {
		t.Fatalf("空账号池文案 = %q", got)
	}
	// 只有在线账号：不追加多余的「冷却 0 · 过期 0 · 禁用 0」
	got := poolSummary([]core.Account{{UID: "a", Status: "online"}})
	if got != "账号池 1/1 在线" {
		t.Fatalf("正常账号池文案 = %q", got)
	}
}

func TestTaskName(t *testing.T) {
	cases := map[string]string{
		core.TaskCheckin:   "每日签到",
		core.TaskTravel:    "猫猫旅行",
		core.TaskKeepalive: "保活任务",
		"unknown":          "unknown", // 未知类型如实回显，不猜
	}
	for in, want := range cases {
		if got := taskName(in); got != want {
			t.Fatalf("taskName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTriggerName(t *testing.T) {
	if got := triggerName("manual"); got != "手动" {
		t.Fatalf("triggerName(manual) = %q", got)
	}
	if got := triggerName("schedule"); got != "排程" {
		t.Fatalf("triggerName(schedule) = %q", got)
	}
}
