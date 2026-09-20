package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppDir(t *testing.T) {
	t.Setenv("BUDDYBOT_HOME", "")
	got := AppDir()
	if !filepath.IsAbs(got) || !strings.HasSuffix(got, string(os.PathSeparator)+appDirName) {
		t.Fatalf("AppDir() = %q, want 绝对路径且以 %q 结尾", got, appDirName)
	}

	custom := t.TempDir()
	t.Setenv("BUDDYBOT_HOME", custom)
	if got := AppDir(); got != custom {
		t.Fatalf("AppDir() = %q, want %q（BUDDYBOT_HOME 覆盖）", got, custom)
	}
}

// 旧目录（<UserConfigDir>/workbuddy-desktop）应整体搬到新数据目录，
// 且 config.json 里指向旧目录的绝对 auth_dir 要改回相对值，否则登录仍会写旧目录。
func TestMigrateLegacyAppDir(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "workbuddy-desktop")
	target := filepath.Join(base, ".buddybot")

	writeTestConfig(t, legacy, filepath.Join(legacy, "auths"))
	if err := os.MkdirAll(filepath.Join(legacy, "auths"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "auths", "workbuddy-u1.json"), []byte(`{"uid":"u1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "store.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	migrateLegacyAppDir(target, []string{legacy})

	if hasAppData(legacy) {
		t.Errorf("迁移后旧目录仍有数据: %s", legacy)
	}
	if !hasAppData(target) {
		t.Fatalf("新目录没有数据: %s", target)
	}
	if _, err := os.Stat(filepath.Join(target, "auths", "workbuddy-u1.json")); err != nil {
		t.Errorf("凭证未随目录迁移: %v", err)
	}
	var cfg map[string]any
	b, err := os.ReadFile(filepath.Join(target, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["auth_dir"] != "auths" {
		t.Errorf("auth_dir = %v, want \"auths\"（迁移后需重新锚定到新目录）", cfg["auth_dir"])
	}

	// 加载后应解析到新数据目录下的 auths
	t.Setenv("BUDDYBOT_HOME", target)
	svc := NewServiceAt(target)
	if got, want := svc.AuthDir(), filepath.Join(target, "auths"); got != want {
		t.Errorf("AuthDir() = %q, want %q", got, want)
	}
}

// 新目录已有数据时不再迁移（避免覆盖正在使用的新数据）
func TestMigrateLegacyAppDirSkipsWhenTargetHasData(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "workbuddy-desktop")
	target := filepath.Join(base, ".buddybot")
	writeTestConfig(t, legacy, "./auths")
	writeTestConfig(t, target, "./auths")

	migrateLegacyAppDir(target, []string{legacy})

	if !hasAppData(legacy) {
		t.Error("目标目录已有数据时不应搬走旧目录")
	}
}

// 只落了单实例锁的空目录不算数据，不该被当成旧数据目录搬走
func TestMigrateLegacyAppDirIgnoresLockOnlyDir(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "workbuddy-desktop")
	target := filepath.Join(base, ".buddybot")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "buddybot.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	migrateLegacyAppDir(target, []string{legacy})

	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("旧目录不该被搬走: %v", err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Error("只有锁文件的旧目录不该触发迁移")
	}
}

func writeTestConfig(t *testing.T, dir, authDir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{"auth_dir": authDir, "api_key": "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}
