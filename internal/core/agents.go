package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 智能体一键接入：探测本机已安装的 AI 编程客户端，把本机网关写入其配置。
// 对齐 workbuddy-switch-gateway agent_import.rs 的行为：写入前备份、可回滚、
// 只动与网关相关的字段（其余配置原样保留）。

// AgentTarget 单个客户端的探测结果
type AgentTarget struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ConfigPath string `json:"configPath"`
	Installed  bool   `json:"installed"`
	Configured bool   `json:"configured"`
	Note       string `json:"note,omitempty"`
}

// AgentApplyResult 写入结果
type AgentApplyResult struct {
	Target    string   `json:"target"`
	BackupDir string   `json:"backupDir"`
	Files     []string `json:"files"`
	Models    []string `json:"models"`
}

// AgentBackup 一份备份（备份目录下的时间戳子目录）
type AgentBackup struct {
	ID    string   `json:"id"`
	Files []string `json:"files"`
}

const (
	agentProviderID   = "workbuddy"
	agentProviderName = "WorkBuddy"
	claudeSlotLimit   = 4 // Claude Code/Desktop 只有 4 个模型槽位
	agentDefaultModel = "deepseek-v4-flash"
)

// ---- 客户端配置路径（macOS 优先，env 覆盖与参考实现一致） ----

func agentHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func agentEnvDir(varName, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(varName)); v != "" {
		return v
	}
	return filepath.Join(agentHome(), fallback)
}

func claudeCodeSettingsPath() string {
	return filepath.Join(agentEnvDir("CLAUDE_CONFIG_DIR", ".claude"), "settings.json")
}

func codexConfigPath() (string, string) {
	home := agentEnvDir("CODEX_HOME", ".codex")
	return filepath.Join(home, "config.toml"), filepath.Join(home, "auth.json")
}

func opencodeConfigPath() string {
	if v := strings.TrimSpace(os.Getenv("OPENCODE_CONFIG")); v != "" {
		return v
	}
	return filepath.Join(agentHome(), ".config", "opencode", "opencode.json")
}

func piModelsPath() string {
	if v := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); v != "" {
		return filepath.Join(v, "models.json")
	}
	return filepath.Join(agentHome(), ".pi", "agent", "models.json")
}

func kimiCodeConfigPath() string {
	return filepath.Join(agentEnvDir("KIMI_CODE_HOME", ".kimi-code"), "config.toml")
}

// codebuddyModelsPath CodeBuddy 自定义模型清单（同 CodeBuddy 的 models.json 结构）
func codebuddyModelsPath() string {
	return filepath.Join(agentEnvDir("CODEBUDDY_HOME", ".codebuddy"), "models.json")
}

// workbuddyModelsPath WorkBuddy 客户端自定义模型清单（~/.workbuddy/models.json，
// 空文件为裸数组形态；与 CodeBuddy 同族 schema：id/name/vendor/url/apiKey/...）
func workbuddyModelsPath() string {
	if v := strings.TrimSpace(os.Getenv("WORKBUDDY_HOME")); v != "" {
		return filepath.Join(v, "models.json")
	}
	return filepath.Join(agentHome(), ".workbuddy", "models.json")
}

func zcodeConfigPath() string {
	return filepath.Join(agentEnvDir("ZCODE_HOME", ".zcode"), "v2", "config.json")
}

func qwenSettingsPath() string {
	return filepath.Join(agentEnvDir("QWEN_CONFIG_DIR", ".qwen"), "settings.json")
}

func minimaxConfigPath() string {
	if v := strings.TrimSpace(os.Getenv("MINIMAX_DATA_DIR")); v != "" {
		return filepath.Join(v, "config.yaml")
	}
	return filepath.Join(agentHome(), ".minimax", "config.yaml")
}

func crushConfigPath() string {
	return filepath.Join(agentEnvDir("CRUSH_HOME", filepath.Join(".config", "crush")), "crush.json")
}

func aiderConfigPath() string {
	if v := strings.TrimSpace(os.Getenv("AIDER_CONFIG")); v != "" {
		return v
	}
	return filepath.Join(agentHome(), ".aider.conf.yml")
}

func zedSettingsPath() string {
	return filepath.Join(agentEnvDir("ZED_CONFIG_DIR", filepath.Join(".config", "zed")), "settings.json")
}

func continueConfigPath() string {
	return filepath.Join(agentEnvDir("CONTINUE_CONFIG_DIR", ".continue"), "config.yaml")
}

// ---- 探测 ----

// DetectAgents 探测全部支持的客户端
func DetectAgents(baseURL, apiKey string) []AgentTarget {
	targets := []agentRef{
		{id: "claude-code", name: "Claude Code", path: claudeCodeSettingsPath()},
		{id: "codex", name: "Codex CLI", path: first(codexConfigPath())},
		{id: "opencode", name: "OpenCode", path: opencodeConfigPath()},
		{id: "pi", name: "Pi Coding Agent", path: piModelsPath()},
		{id: "kimi-code", name: "Kimi Code", path: kimiCodeConfigPath()},
		{id: "codebuddy", name: "CodeBuddy", path: codebuddyModelsPath()},
		{id: "workbuddy", name: "WorkBuddy", path: workbuddyModelsPath()},
		{id: "zcode", name: "ZCode", path: zcodeConfigPath()},
		{id: "qwen", name: "Qwen Code", path: qwenSettingsPath()},
		{id: "minimax", name: "MiniMax Code", path: minimaxConfigPath()},
		{id: "crush", name: "Crush", path: crushConfigPath()},
		{id: "aider", name: "Aider", path: aiderConfigPath()},
		{id: "zed", name: "Zed", path: zedSettingsPath()},
		{id: "continue", name: "Continue", path: continueConfigPath()},
	}
	out := make([]AgentTarget, 0, len(targets))
	for _, t := range targets {
		installed := t.path != "" && pathExists(t.path)
		if !installed {
			// 配置文件还没生成不代表没安装：目录存在即视为已安装
			installed = pathExists(filepath.Dir(t.path))
		}
		configured := false
		if installed {
			if text, err := os.ReadFile(t.path); err == nil {
				s := string(text)
				configured = strings.Contains(s, baseURL) && (apiKey == "" || strings.Contains(s, apiKey))
			}
		}
		note := ""
		if !installed {
			note = "未检测到安装"
		} else if configured {
			note = "已接入本网关"
		}
		out = append(out, AgentTarget{
			ID: t.id, Name: t.name, ConfigPath: t.path,
			Installed: installed, Configured: configured, Note: note,
		})
	}
	return out
}

type agentRef struct {
	id, name, path string
}

func first(a, _ string) string { return a }

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ---- 写入 ----

// ApplyAgent 把网关接入指定客户端（写入前自动备份）
func ApplyAgent(id, baseURL, apiKey string, models []string, backupRoot string) (*AgentApplyResult, error) {
	if len(models) == 0 {
		models = []string{agentDefaultModel}
	}
	var files []string
	switch id {
	case "claude-code":
		files = []string{claudeCodeSettingsPath()}
	case "codex":
		cfg, auth := codexConfigPath()
		files = []string{cfg, auth}
	case "opencode":
		files = []string{opencodeConfigPath()}
	case "pi":
		files = []string{piModelsPath()}
	case "kimi-code":
		files = []string{kimiCodeConfigPath()}
	case "codebuddy":
		files = []string{codebuddyModelsPath()}
	case "workbuddy":
		files = []string{workbuddyModelsPath()}
	case "zcode":
		files = []string{zcodeConfigPath()}
	case "qwen":
		files = []string{qwenSettingsPath()}
	case "minimax":
		files = []string{minimaxConfigPath()}
	case "crush":
		files = []string{crushConfigPath()}
	case "aider":
		files = []string{aiderConfigPath()}
	case "zed":
		files = []string{zedSettingsPath()}
	case "continue":
		files = []string{continueConfigPath()}
	default:
		return nil, fmt.Errorf("不支持的客户端: %s", id)
	}

	backupDir, err := backupAgentFiles(id, files, backupRoot)
	if err != nil {
		return nil, err
	}

	written := files
	switch id {
	case "claude-code":
		// Claude Code 只有 4 个槽位，超出部分截断并如实回传
		if len(models) > claudeSlotLimit {
			models = models[:claudeSlotLimit]
		}
		if err := writeAgentJSON(files[0], func(root map[string]any) {
			env, _ := root["env"].(map[string]any)
			if env == nil {
				env = map[string]any{}
			}
			env["ANTHROPIC_BASE_URL"] = baseURL
			env["ANTHROPIC_AUTH_TOKEN"] = apiKey
			primary := models[0]
			slots := []struct{ key, model string }{
				{"SONNET", primary},
				{"OPUS", at(models, 1, primary)},
				{"HAIKU", at(models, 2, primary)},
				{"FABLE", at(models, 3, primary)},
			}
			for _, s := range slots {
				env["ANTHROPIC_DEFAULT_"+s.key+"_MODEL"] = s.model
				env["ANTHROPIC_DEFAULT_"+s.key+"_MODEL_NAME"] = s.model
			}
			root["env"] = env
		}); err != nil {
			return nil, fmt.Errorf("写入 Claude Code 配置失败: %w", err)
		}
	case "codex":
		primary := models[0]
		existing := readAgentFile(files[0])
		cfg := setTOMLTopLevel(existing, "model_provider", `"`+agentProviderID+`"`)
		cfg = setTOMLTopLevel(cfg, "model", `"`+primary+`"`)
		cfg = setTOMLTopLevel(cfg, "model_reasoning_effort", `"high"`)
		cfg = setTOMLTopLevel(cfg, "disable_response_storage", "true")
		table := "[model_providers." + agentProviderID + "]"
		cfg = replaceTOMLTable(cfg, table, table+"\nname = \""+agentProviderName+"\"\nbase_url = \""+baseURL+"/v1\"\nwire_api = \"responses\"\nrequires_openai_auth = true\n")
		if err := atomicWriteFile(files[0], cfg); err != nil {
			return nil, fmt.Errorf("写入 Codex config 失败: %w", err)
		}
		if err := writeAgentJSON(files[1], func(root map[string]any) {
			root["OPENAI_API_KEY"] = apiKey
			delete(root, "tokens")
			delete(root, "last_refresh")
			root["auth_mode"] = "apikey"
		}); err != nil {
			return nil, fmt.Errorf("写入 Codex auth 失败: %w", err)
		}
	case "opencode":
		if err := writeAgentJSON(files[0], func(root map[string]any) {
			if _, ok := root["$schema"]; !ok {
				root["$schema"] = "https://opencode.ai/config.json"
			}
			provider, _ := root["provider"].(map[string]any)
			if provider == nil {
				provider = map[string]any{}
			}
			modelsMap := map[string]any{}
			for _, m := range models {
				modelsMap[m] = map[string]any{"name": m}
			}
			provider[agentProviderID] = map[string]any{
				"name": agentProviderName,
				"npm":  "@ai-sdk/openai-compatible",
				"options": map[string]any{
					"baseURL":     baseURL + "/v1",
					"apiKey":      apiKey,
					"setCacheKey": true,
				},
				"models": modelsMap,
			}
			root["provider"] = provider
		}); err != nil {
			return nil, fmt.Errorf("写入 OpenCode 配置失败: %w", err)
		}
	case "pi":
		if err := writeAgentJSON(files[0], func(root map[string]any) {
			providers, _ := root["providers"].(map[string]any)
			if providers == nil {
				providers = map[string]any{}
			}
			list := make([]map[string]any, 0, len(models))
			for _, m := range models {
				list = append(list, map[string]any{"id": m, "reasoning": true})
			}
			providers[agentProviderID] = map[string]any{
				"api":     "openai-completions",
				"baseUrl": baseURL + "/v1",
				"apiKey":  apiKey,
				"models":  list,
			}
			root["providers"] = providers
		}); err != nil {
			return nil, fmt.Errorf("写入 Pi 配置失败: %w", err)
		}
	case "kimi-code":
		primary := models[0]
		out := setTOMLTopLevel(readAgentFile(files[0]), "default_model", `"`+primary+`"`)
		providerTable := "[providers." + agentProviderID + "]"
		out = replaceTOMLTable(out, providerTable, providerTable+"\ntype = \"openai\"\nbase_url = \""+baseURL+"/v1\"\napi_key = \""+apiKey+"\"\n")
		for _, m := range models {
			header := `[models."` + m + `"]`
			out = replaceTOMLTable(out, header, header+"\nprovider = \""+agentProviderID+"\"\nmodel = \""+m+"\"\nmax_context_size = 200000\ncapabilities = [\"tool_use\"]\n")
		}
		if err := atomicWriteFile(files[0], out); err != nil {
			return nil, fmt.Errorf("写入 Kimi Code 配置失败: %w", err)
		}
	case "zcode":
		// ZCode 桌面版自定义 provider（kind=openai-compatible，Chat Completions）。
		// 注意：ZCode 运行时会把内存注册表写回本文件，运行期间的外部编辑会被丢弃，
		// 前端提示里已要求先退出 ZCode 再接入。
		if err := writeAgentJSON(files[0], func(root map[string]any) {
			provider, _ := root["provider"].(map[string]any)
			if provider == nil {
				provider = map[string]any{}
			}
			modelsMap := map[string]any{}
			for _, m := range models {
				modelsMap[m] = map[string]any{
					"limit": map[string]any{"context": 200000, "output": 32768},
				}
			}
			provider[agentProviderID] = map[string]any{
				"name": agentProviderName,
				"kind": "openai-compatible",
				"options": map[string]any{
					"apiKey":         apiKey,
					"baseURL":        baseURL + "/v1",
					"apiKeyRequired": true,
				},
				"source": "custom",
				"models": modelsMap,
			}
			root["provider"] = provider
		}); err != nil {
			return nil, fmt.Errorf("写入 ZCode 配置失败: %w", err)
		}
	case "qwen":
		// Qwen Code：modelProviders.openai[] 追加条目，key 走 env 字段（settings.json 内优先级最低但可用）
		if err := writeAgentJSON(files[0], func(root map[string]any) {
			envKey := "WORKBUDDY_GATEWAY_API_KEY"
			env, _ := root["env"].(map[string]any)
			if env == nil {
				env = map[string]any{}
			}
			env[envKey] = apiKey
			root["env"] = env

			mp, _ := root["modelProviders"].(map[string]any)
			if mp == nil {
				mp = map[string]any{}
			}
			list, _ := mp["openai"].([]any)
			kept := make([]any, 0, len(list)+len(models))
			for _, item := range list {
				if m, ok := item.(map[string]any); ok {
					if v, _ := m["envKey"].(string); v == envKey {
						continue // 清掉自家旧条目，其它条目原样保留
					}
				}
				kept = append(kept, item)
			}
			for _, m := range models {
				kept = append(kept, map[string]any{
					"id": m, "name": agentProviderName + " " + m,
					"baseUrl": baseURL + "/v1", "envKey": envKey,
				})
			}
			mp["openai"] = kept
			root["modelProviders"] = mp

			sec, _ := root["security"].(map[string]any)
			if sec == nil {
				sec = map[string]any{}
			}
			auth, _ := sec["auth"].(map[string]any)
			if auth == nil {
				auth = map[string]any{}
			}
			auth["selectedType"] = "openai"
			sec["auth"] = auth
			root["security"] = sec

			modelCfg, _ := root["model"].(map[string]any)
			if modelCfg == nil {
				modelCfg = map[string]any{}
			}
			modelCfg["name"] = models[0]
			root["model"] = modelCfg
		}); err != nil {
			return nil, fmt.Errorf("写入 Qwen Code 配置失败: %w", err)
		}
	case "minimax":
		// MiniMax Code（mcode）：config.yaml 的 provider map，同 opencode 的 @ai-sdk 家族
		if err := upsertYAMLProviderEntry(files[0], baseURL, apiKey, models); err != nil {
			return nil, fmt.Errorf("写入 MiniMax 配置失败: %w", err)
		}
	case "crush":
		if err := writeAgentJSON(files[0], func(root map[string]any) {
			provider, _ := root["provider"].(map[string]any)
			if provider == nil {
				provider = map[string]any{}
			}
			list := make([]map[string]any, 0, len(models))
			for _, m := range models {
				list = append(list, map[string]any{"id": m, "name": m})
			}
			provider[agentProviderID] = map[string]any{
				"type":     "openai",
				"base_url": baseURL + "/v1",
				"api_key":  apiKey,
				"models":   list,
			}
			root["provider"] = provider
		}); err != nil {
			return nil, fmt.Errorf("写入 Crush 配置失败: %w", err)
		}
	case "aider":
		// Aider：扁平 YAML，model 走 openai/ 前缀 + openai-api-base / openai-api-key
		cfg := setYAMLFlat(readAgentFile(files[0]), "model", fmt.Sprintf("%q", "openai/"+models[0]))
		cfg = setYAMLFlat(cfg, "openai-api-base", fmt.Sprintf("%q", baseURL+"/v1"))
		cfg = setYAMLFlat(cfg, "openai-api-key", fmt.Sprintf("%q", apiKey))
		if err := atomicWriteFile(files[0], cfg); err != nil {
			return nil, fmt.Errorf("写入 Aider 配置失败: %w", err)
		}
	case "zed":
		// Zed：language_models.openai_compatible.<provider>，api_url 指向网关
		if err := writeAgentJSON(files[0], func(root map[string]any) {
			lm, _ := root["language_models"].(map[string]any)
			if lm == nil {
				lm = map[string]any{}
			}
			oc, _ := lm["openai_compatible"].(map[string]any)
			if oc == nil {
				oc = map[string]any{}
			}
			list := make([]map[string]any, 0, len(models))
			for _, m := range models {
				list = append(list, map[string]any{
					"name": m, "display_name": m,
					"max_tokens": 128000, "max_output_tokens": 32768,
				})
			}
			oc[agentProviderName] = map[string]any{
				"api_url":          baseURL + "/v1",
				"api_key":          apiKey,
				"available_models": list,
			}
			lm["openai_compatible"] = oc
			root["language_models"] = lm
		}); err != nil {
			return nil, fmt.Errorf("写入 Zed 配置失败: %w", err)
		}
	case "continue":
		// Continue：config.yaml 的 models[] 追加条目（apiBase 指向网关）
		if err := upsertContinueModels(files[0], baseURL, apiKey, models); err != nil {
			return nil, fmt.Errorf("写入 Continue 配置失败: %w", err)
		}
	case "codebuddy", "workbuddy":
		// 自定义模型清单：{"models":[...]} 或裸数组。只增删 vendor=WorkBuddy 的条目，
		// 其余模型（含各家的真实 apiKey）原样保留；url 形态对齐本机已有条目（…/chat/completions）。
		if err := upsertModelListEntry(files[0], baseURL, apiKey, models); err != nil {
			return nil, fmt.Errorf("写入 %s 模型清单失败: %w", id, err)
		}
	}

	if written == nil {
		written = files
	}
	return &AgentApplyResult{
		Target: id, BackupDir: backupDir, Files: written, Models: append([]string(nil), models...),
	}, nil
}

func at(list []string, i int, def string) string {
	if i < len(list) {
		return list[i]
	}
	return def
}

// ---- 自定义模型清单（CodeBuddy / WorkBuddy） ----

// upsertModelListEntry 在 models.json 中写入/替换 vendor=agentProviderName 的模型条目。
// 文件支持 {"models":[...]}（CodeBuddy 形态）与裸数组（WorkBuddy 空文件形态）两种。
func upsertModelListEntry(path, baseURL, apiKey string, models []string) error {
	var (
		root      map[string]any // 对象形态时的根（含 models 键）
		list      []any
		bareArray bool
	)
	if text := readAgentFile(path); strings.TrimSpace(text) != "" {
		var probe any
		if err := json.Unmarshal([]byte(text), &probe); err != nil {
			return fmt.Errorf("现有配置不是合法 JSON（请先修复或手动备份）: %w", err)
		}
		switch v := probe.(type) {
		case []any:
			bareArray, list = true, v // WorkBuddy 裸数组形态
		case map[string]any:
			root = v
			list, _ = root["models"].([]any)
		default:
			return fmt.Errorf("models.json 顶层不是对象或数组，无法安全写入")
		}
	} else {
		bareArray = true // 空文件默认按裸数组形态生成
	}

	// 清掉自家命名空间的旧条目，保留其它厂商条目原样
	kept := make([]any, 0, len(list)+len(models))
	for _, item := range list {
		m, _ := item.(map[string]any)
		if v, _ := m["vendor"].(string); v == agentProviderName {
			continue
		}
		kept = append(kept, item)
	}
	for _, m := range models {
		kept = append(kept, map[string]any{
			"id": m, "name": m, "vendor": agentProviderName,
			"url": baseURL + "/v1/chat/completions", "apiKey": apiKey,
			"supportsToolCall": true, "supportsImages": false, "supportsReasoning": true,
		})
	}

	var out []byte
	if bareArray {
		out, _ = json.MarshalIndent(kept, "", "  ")
	} else {
		root["models"] = kept
		out, _ = json.MarshalIndent(root, "", "  ")
	}
	return atomicWriteFile(path, string(out)+"\n")
}

// ---- 备份与回滚 ----

// 备份目录结构：<backupRoot>/<target>/<ts>/<原文件名> + manifest.json（文件名→原路径）

func backupAgentFiles(target string, files []string, backupRoot string) (string, error) {
	if backupRoot == "" {
		return "", fmt.Errorf("备份根目录未配置")
	}
	var existing []string
	for _, f := range files {
		if pathExists(f) {
			existing = append(existing, f)
		}
	}
	dir := filepath.Join(backupRoot, target, time.Now().Format("20060102-150405"))
	if len(existing) == 0 {
		return dir, nil // 没有可备份的旧文件（首次生成），仍返回目录便于追踪
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建备份目录失败: %w", err)
	}
	manifest := map[string]string{}
	for _, f := range existing {
		data, err := os.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("读取待备份文件失败 %s: %w", f, err)
		}
		name := filepath.Base(f)
		if _, taken := manifest[name]; taken {
			name = fmt.Sprintf("%d-%s", len(manifest), name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return "", fmt.Errorf("写备份文件失败: %w", err)
		}
		manifest[name] = f
	}
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), mb, 0o600); err != nil {
		return "", fmt.Errorf("写备份清单失败: %w", err)
	}
	return dir, nil
}

// ListAgentBackups 列出某客户端的备份（新→旧）
func ListAgentBackups(target, backupRoot string) []AgentBackup {
	entries, err := os.ReadDir(filepath.Join(backupRoot, target))
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	out := make([]AgentBackup, 0, len(ids))
	for _, id := range ids {
		out = append(out, AgentBackup{ID: id, Files: agentBackupFiles(filepath.Join(backupRoot, target, id))})
	}
	return out
}

func agentBackupFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && e.Name() != "manifest.json" {
			out = append(out, e.Name())
		}
	}
	return out
}

// RestoreAgentBackup 回滚指定备份，返回恢复的文件数
func RestoreAgentBackup(target, backupID, backupRoot string) (int, error) {
	dir := filepath.Join(backupRoot, target, backupID)
	mb, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return 0, fmt.Errorf("备份不存在或缺清单: %w", err)
	}
	var manifest map[string]string
	if err := json.Unmarshal(mb, &manifest); err != nil {
		return 0, fmt.Errorf("备份清单损坏: %w", err)
	}
	n := 0
	for name, orig := range manifest {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return n, fmt.Errorf("读备份文件失败 %s: %w", name, err)
		}
		if err := atomicWriteFile(orig, string(data)); err != nil {
			return n, fmt.Errorf("恢复失败 %s: %w", orig, err)
		}
		n++
	}
	return n, nil
}

// ---- 文件读写工具 ----

// writeAgentJSON 读取现有 JSON（缺失/损坏时从空对象开始），交给 mutate 修改后原子写回
func writeAgentJSON(path string, mutate func(root map[string]any)) error {
	root := map[string]any{}
	if text := readAgentFile(path); strings.TrimSpace(text) != "" {
		if err := json.Unmarshal([]byte(text), &root); err != nil {
			return fmt.Errorf("现有配置不是合法 JSON（请先修复或手动备份）: %w", err)
		}
	}
	mutate(root)
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, string(out)+"\n")
}

func readAgentFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// atomicWriteFile 原子写：临时文件 + rename
func atomicWriteFile(path, content string) error {
	if parent := filepath.Dir(path); parent != "" {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".wb-agent-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// ---- TOML 行级编辑（与参考实现 set_toml_top_level / replace_toml_table 对齐） ----

// setTOMLTopLevel 设置顶层键（不含表内键）；已存在则原位替换，否则插到第一个表头之前（或文件尾）
func setTOMLTopLevel(text, key, value string) string {
	lines := strings.Split(text, "\n")
	inTable := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inTable = true
			continue
		}
		if inTable || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if k, _, ok := strings.Cut(trimmed, "="); ok && strings.TrimSpace(k) == key {
			lines[i] = key + " = " + value
			return strings.Join(lines, "\n")
		}
	}
	// 插入位置：第一个表头之前；无表头则追加
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			out := append([]string{}, lines[:i]...)
			out = append(out, key+" = "+value)
			out = append(out, lines[i:]...)
			return strings.Join(out, "\n")
		}
	}
	out := strings.TrimRight(text, "\n")
	if out != "" {
		out += "\n"
	}
	return out + key + " = " + value + "\n"
}

// replaceTOMLTable 替换 [table] 整节（从表头到下一个表头/EOF）；不存在则追加
func replaceTOMLTable(text, header, body string) string {
	lines := strings.Split(text, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			start = i
			break
		}
	}
	if start < 0 {
		out := strings.TrimRight(text, "\n")
		if out != "" {
			out += "\n"
		}
		return out + "\n" + body
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			end = i
			break
		}
	}
	out := append([]string{}, lines[:start]...)
	out = append(out, strings.Split(strings.TrimRight(body, "\n"), "\n")...)
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n")
}
