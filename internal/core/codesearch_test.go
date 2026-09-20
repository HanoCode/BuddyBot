package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchProjectCode(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", "package main\n\nfunc main() {\n\tprintln(\"needle-marker\")\n}\n")
	write("sub/util.go", "package sub\n\n// needle-marker 用法说明\nfunc Util() {}\n")
	write("node_modules/dep/index.js", "needle-marker\n") // 应被跳过
	write("bin.dat", "needle-marker\x00\x01binary\n")    // 含 NUL，应被跳过

	res, err := SearchProjectCode(root, "needle-marker", 10)
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	paths := map[string]bool{}
	for _, h := range res.Hits {
		paths[h.Path] = true
		if h.Line <= 0 || h.Snippet == "" {
			t.Fatalf("命中缺少行号/片段: %+v", h)
		}
	}
	if !paths["main.go"] || !paths[filepath.Join("sub", "util.go")] {
		t.Fatalf("应命中两个源文件: %+v", res.Hits)
	}
	if paths[filepath.Join("node_modules", "dep", "index.js")] {
		t.Fatal("node_modules 应被跳过")
	}
	if paths["bin.dat"] {
		t.Fatal("二进制文件应被跳过")
	}
	if res.MatchedFiles != 2 {
		t.Fatalf("命中文件数应为 2，得到 %d", res.MatchedFiles)
	}

	// 截断
	trunc, err := SearchProjectCode(root, "needle-marker", 1)
	if err != nil || len(trunc.Hits) != 1 || trunc.StopReason != CodeSearchStopHits {
		t.Fatalf("limit=1 应截断: %+v err=%v", trunc, err)
	}
	// 空关键词
	if _, err := SearchProjectCode(root, "   ", 10); err == nil {
		t.Fatal("空关键词应报错")
	}
	// 目录不存在
	if _, err := SearchProjectCode(filepath.Join(root, "nope"), "x", 10); err == nil {
		t.Fatal("目录不存在应报错")
	}
}

// TestSearchProjectCodeBoundaries 检索边界：过宽的根拒绝、敏感目录跳过、预算可中断
func TestSearchProjectCodeBoundaries(t *testing.T) {
	// 过宽的根：系统目录与一级目录都不接受
	for _, bad := range []string{"/", "/etc", "/Users", "/var"} {
		if _, err := SearchProjectCode(bad, "x", 10); err == nil {
			t.Errorf("过宽目录应被拒绝: %s", bad)
		}
	}
	if home := agentHome(); home != "" {
		if _, err := SearchProjectCode(home, "x", 10); err == nil {
			t.Errorf("整个家目录应被拒绝: %s", home)
		}
	}

	// 敏感目录不进检索结果
	root := t.TempDir()
	for _, rel := range []string{".ssh/config", ".aws/credentials", "src/main.go"} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("needle-marker\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, err := SearchProjectCode(root, "needle-marker", 10)
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	for _, h := range res.Hits {
		if strings.Contains(h.Path, ".ssh") || strings.Contains(h.Path, ".aws") {
			t.Fatalf("敏感目录不应被检索: %+v", h)
		}
	}
	if len(res.Hits) != 1 || !strings.HasSuffix(res.Hits[0].Path, "main.go") {
		t.Fatalf("应只命中普通源文件: %+v", res.Hits)
	}
	// 扫完时没有停止原因
	if res.StopReason != "" {
		t.Fatalf("扫完不应标记停止原因: %q", res.StopReason)
	}
}

func TestSearchProjectCodeSnippetTrim(t *testing.T) {
	long := strings.Repeat("x", 400) + "needle"
	if s := trimSnippet(long); len([]rune(s)) > 202 || !strings.HasSuffix(s, "…") {
		t.Fatalf("超长行应压缩: %d", len([]rune(s)))
	}
	if s := trimSnippet("  a   b  needle  "); s != "a b needle" {
		t.Fatalf("空白应折叠: %q", s)
	}
}
