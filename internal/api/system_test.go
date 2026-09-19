package api

import "testing"

func TestCompareVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0", "1.0.0", 0},   // 缺段补 0
		{"2.0", "1.9.9", 1},   // 逐段比较，不做数值拼接
		{"1.10.0", "1.9.0", 1},
		{"0.9", "1.0", -1},
		{"", "0.0.1", -1},     // 全按 0 处理
	}
	for _, c := range cases {
		if got := compareVersion(c.a, c.b); got != c.want {
			t.Fatalf("compareVersion(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestPickUpdateAsset(t *testing.T) {
	assets := []ghAsset{
		{Name: "workbuddy-desktop-1.1.0-linux-amd64.AppImage"},
		{Name: "workbuddy-desktop-1.1.0-darwin-arm64.dmg"},
		{Name: "workbuddy-desktop-1.1.0-darwin-amd64.dmg"},
		{Name: "workbuddy-desktop-1.1.0-windows-installer.exe"},
		{Name: "source-code.zip"},
	}
	if got := pickUpdateAsset(assets, "darwin", "arm64"); got == nil || got.Name != assets[1].Name {
		t.Fatalf("darwin/arm64 应精确匹配: %+v", got)
	}
	if got := pickUpdateAsset(assets, "windows", "amd64"); got == nil || got.Name != assets[3].Name {
		t.Fatalf("windows 应匹配安装包: %+v", got)
	}
	if got := pickUpdateAsset(assets, "linux", "amd64"); got == nil || got.Name != assets[0].Name {
		t.Fatalf("linux 应匹配 AppImage: %+v", got)
	}
	if got := pickUpdateAsset(assets, "freebsd", "amd64"); got != nil {
		t.Fatalf("未知平台不应匹配: %+v", got)
	}
}
