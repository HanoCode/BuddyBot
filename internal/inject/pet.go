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

	"embed"

	"workbuddy-desktop/internal/core"
)

// ============================================================
// 宠物引擎（参考 workbuddy/pets 项目的 Codex 宠物精灵图格式）
//
// 素材格式（petdex / codex-pets v2 图集，逆向自 ChatGPT.app）：
//   - 每只宠物一个目录：pet.json + preview.webp + spritesheet.webp
//   - spritesheet：8 列 × N 行，每帧 192×208（内置素材 11 行）
//   - 行 = 宠物状态：0 idle / 3 waving / 4 jumping / 5 failed /
//     6 waiting / 7 running（与 petdex-desktop sprite.zig 帧表一致）
// 面板「主题」Tab 选择宠物后，注入的悬浮机器人（fab）替换为精灵图逐帧动画。
// 内置宠物随二进制内嵌（pets/）；自定义宠物由面板上传（pet_add），
// 落盘 <数据目录>/inject/pets-custom/<custom-<ts>>/，引用 ID 为 custom:<目录名>。
// ============================================================

//go:embed pets
var petFS embed.FS

var (
	petIDRe       = regexp.MustCompile(`^[a-z0-9-]+$`)
	customPetRe   = regexp.MustCompile(`^custom-\d+$`)
	customPetName = regexp.MustCompile(`^custom:\d+$`) // 面板引用前缀 custom: + 目录名（历史遗留含前缀形式）
)

// petMeta 面板宠物列表项（spritesheet 以 dataURL 按需推送）
type petMeta struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Preview string `json:"preview"` // dataURL（192×208）
	Sprite  string `json:"sprite"`  // dataURL（spritesheet.webp，体积较大）
	Custom  bool   `json:"custom"`  // 自定义宠物（面板显示删除按钮）
}

// petDef pet.json 结构（内置与自定义通用）
type petDef struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// petsRoot 自定义宠物目录：<数据目录>/inject/pets-custom
func (m *Manager) petsRoot() string {
	return filepath.Join(filepath.Dir(m.svc.ConfigPath()), "inject", "pets-custom")
}

// builtinPetDataURL 内嵌宠物文件 → dataURL
func builtinPetDataURL(id, file string) (string, error) {
	if !petIDRe.MatchString(id) {
		return "", fmt.Errorf("非法宠物 ID: %s", id)
	}
	if file != "preview.webp" && file != "spritesheet.webp" {
		return "", fmt.Errorf("非法宠物文件: %s", file)
	}
	raw, err := petFS.ReadFile("pets/" + id + "/" + file)
	if err != nil {
		return "", err
	}
	return "data:image/webp;base64," + base64.StdEncoding.EncodeToString(raw), nil
}

// diskPetDataURL 磁盘宠物文件 → dataURL（目录名已校验）
func diskPetDataURL(dir, file string) (string, error) {
	if file != "preview.webp" && file != "spritesheet.webp" {
		return "", fmt.Errorf("非法宠物文件: %s", file)
	}
	raw, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return "", err
	}
	return "data:image/webp;base64," + base64.StdEncoding.EncodeToString(raw), nil
}

// petFromDef 由 pet.json + 素材组装列表项；任一文件缺失返回 nil
func petFromDef(dir string, readDef func() (*petDef, error), custom bool, file func(string) (string, error)) *petMeta {
	def, err := readDef()
	if err != nil || def == nil {
		return nil
	}
	preview, err1 := file("preview.webp")
	sprite, err2 := file("spritesheet.webp")
	if err1 != nil || err2 != nil {
		return nil
	}
	return &petMeta{ID: def.ID, Name: def.DisplayName, Preview: preview, Sprite: sprite, Custom: custom}
}

// listPets 枚举宠物库：内置（目录名与 pet.json 的 id 双重校验）+ 自定义
func (m *Manager) listPets() []petMeta {
	out := []petMeta{}
	if entries, err := petFS.ReadDir("pets"); err == nil {
		for _, e := range entries {
			if !e.IsDir() || !petIDRe.MatchString(e.Name()) {
				continue
			}
			p := petFromDef("pets/"+e.Name(),
				func() (*petDef, error) {
					raw, err := petFS.ReadFile("pets/" + e.Name() + "/pet.json")
					if err != nil {
						return nil, err
					}
					var d petDef
					if json.Unmarshal(raw, &d) != nil || d.ID != e.Name() {
						return nil, fmt.Errorf("pet.json 不匹配")
					}
					return &d, nil
				},
				false,
				func(f string) (string, error) { return builtinPetDataURL(e.Name(), f) })
			if p != nil {
				out = append(out, *p)
			}
		}
	}
	// 自定义宠物（新添加的在前）
	if entries, err := os.ReadDir(m.petsRoot()); err == nil {
		names := []string{}
		for _, e := range entries {
			if e.IsDir() && customPetRe.MatchString(e.Name()) {
				names = append(names, e.Name())
			}
		}
		for i := len(names) - 1; i >= 0; i-- {
			name := names[i]
			dir := filepath.Join(m.petsRoot(), name)
			p := petFromDef(dir,
				func() (*petDef, error) {
					raw, err := os.ReadFile(filepath.Join(dir, "pet.json"))
					if err != nil {
						return nil, err
					}
					var d petDef
					if json.Unmarshal(raw, &d) != nil {
						return nil, err
					}
					return &d, nil
				},
				true,
				func(f string) (string, error) { return diskPetDataURL(dir, f) })
			if p != nil {
				out = append(out, *p)
			}
		}
	}
	return out
}

// petExists 校验宠物 ID：内置 id 或 custom:<目录名>
func (m *Manager) petExists(id string) bool {
	if id == "" {
		return true
	}
	if name, ok := strings.CutPrefix(id, "custom:"); ok {
		if !customPetRe.MatchString(name) {
			return false
		}
		raw, err := os.ReadFile(filepath.Join(m.petsRoot(), name, "pet.json"))
		return err == nil && json.Valid(raw)
	}
	if !petIDRe.MatchString(id) {
		return false
	}
	_, err := petFS.ReadFile("pets/" + id + "/pet.json")
	return err == nil
}

// AddCustomPet 保存自定义宠物（面板裁好 preview）并返回引用 ID
func (m *Manager) AddCustomPet(name, sprite, preview string) (string, error) {
	decode := func(dataURL string) ([]byte, error) {
		const marker = ";base64,"
		idx := strings.Index(dataURL, marker)
		if idx < 0 {
			return nil, fmt.Errorf("图片数据格式无效")
		}
		raw, err := base64.StdEncoding.DecodeString(dataURL[idx+len(marker):])
		if err != nil {
			return nil, fmt.Errorf("图片解码失败: %w", err)
		}
		if len(raw) > 16<<20 {
			return nil, fmt.Errorf("图片过大（超过 16MB）")
		}
		return raw, nil
	}
	spriteRaw, err := decode(sprite)
	if err != nil {
		return "", err
	}
	previewRaw, err := decode(preview)
	if err != nil {
		return "", err
	}
	display := strings.TrimSpace(name)
	if display == "" {
		display = "自定义宠物"
	}
	if len([]rune(display)) > 24 {
		display = string([]rune(display)[:24])
	}
	dir := filepath.Join(m.petsRoot(), fmt.Sprintf("custom-%d", time.Now().UnixMilli()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	def := petDef{ID: filepath.Base(dir), DisplayName: display}
	rawDef, _ := json.MarshalIndent(def, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "pet.json"), rawDef, 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "spritesheet.webp"), spriteRaw, 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "preview.webp"), previewRaw, 0o600); err != nil {
		return "", err
	}
	return "custom:" + filepath.Base(dir), nil
}

// DeleteCustomPet 删除自定义宠物；若正在使用则重置为经典机器人
func (m *Manager) DeleteCustomPet(id string) error {
	name := strings.TrimPrefix(id, "custom:")
	if !customPetRe.MatchString(name) {
		return fmt.Errorf("非法的自定义宠物: %s", id)
	}
	if m.svc.GetConfig().Inject.Pet == "custom:"+name {
		_ = m.svc.UpdateInjectConfig(func(c *core.InjectConfig) { c.Pet = "" })
	}
	err := os.RemoveAll(filepath.Join(m.petsRoot(), name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ApplyPet 保存宠物选择（"" = 恢复经典 CSS 机器人）并推送面板
func (m *Manager) ApplyPet(id string) error {
	if !m.petExists(id) {
		return fmt.Errorf("未知宠物: %s", id)
	}
	_ = m.svc.UpdateInjectConfig(func(c *core.InjectConfig) { c.Pet = id })
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn == nil {
		return nil
	}
	m.pushPanelPet(conn)
	return nil
}

// ---------- 面板宠物通道 ----------

// pushPanelPet 推送当前宠物选择（"" = 经典机器人）
func (m *Manager) pushPanelPet(conn *cdpConn) {
	id := m.svc.GetConfig().Inject.Pet
	raw, _ := json.Marshal(map[string]string{"id": id})
	_, _ = conn.evaluate(`window.__wbdeskSetPet && window.__wbdeskSetPet(`+string(raw)+`)`, 5*time.Second)
}

// pushPanelPets 推送宠物库（内置 + 自定义，含 spritesheet dataURL，按需触发）
func (m *Manager) pushPanelPets() {
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn == nil {
		return
	}
	raw, _ := json.Marshal(m.listPets())
	_, _ = conn.evaluate(`window.__wbdeskSetPets && window.__wbdeskSetPets(`+string(raw)+`)`, 30*time.Second)
}
