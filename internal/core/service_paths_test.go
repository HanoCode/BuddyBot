package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 打包成 App 后双击启动时 CWD 是 /（只读），相对 auth_dir 必须锚定数据目录而不是 CWD，
// 否则会解析成 /auths 并在 mkdir 时报 "read-only file system"。
func TestAuthDirRelativeAnchorsToDataDir(t *testing.T) {
	dir := t.TempDir()
	svc := NewServiceAt(dir)
	if got, want := svc.AuthDir(), filepath.Join(dir, "auths"); got != want {
		t.Fatalf("AuthDir() = %q, want %q", got, want)
	}
}

func TestAbsDataPath(t *testing.T) {
	svc := NewServiceAt(t.TempDir())
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	cases := []struct{ in, want string }{
		{"./auths", filepath.Join(svc.dataDir, "auths")},
		{"~/x/auths", filepath.Join(home, "x", "auths")},
		{"/tmp/abs/auths", "/tmp/abs/auths"},
		{"", ""},
	}
	for _, c := range cases {
		if got := svc.absDataPath(c.in); got != c.want {
			t.Errorf("absDataPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 存量配置里的相对路径加载后应被归一化为数据目录下的绝对路径
func TestLoadConfigNormalizesRelativeAuthDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"auth_dir":"./auths"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := NewServiceAt(dir)
	got := svc.GetConfig().AuthDir
	if !filepath.IsAbs(got) || !strings.HasPrefix(got, dir) {
		t.Fatalf("AuthDir = %q, want absolute path under %q", got, dir)
	}
}
