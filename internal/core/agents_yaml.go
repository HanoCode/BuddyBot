package core

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// YAML 编辑工具：基于 yaml.v3 Node 的原位编辑，只动与网关相关的键，
// 其余内容（含未知字段）原样保留。供 MiniMax / Continue 等 YAML 配置使用。

// yamlLoadRoot 读取 YAML 文件为文档节点；文件缺失或为空时按 create 返回空映射文档
func yamlLoadRoot(path string, create bool) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && create {
			return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		if create {
			return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}, nil
		}
		return nil, fmt.Errorf("现有配置是空文件，无法安全写入")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("现有配置不是合法 YAML（请先修复或手动备份）: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("YAML 顶层不是映射，无法安全写入")
	}
	return &doc, nil
}

// yamlSave 把文档节点原子写回文件
func yamlSave(path string, doc *yaml.Node) error {
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return atomicWriteFile(path, string(out))
}

// yamlMapGet 取映射中指定键的值节点，不存在返回 nil
func yamlMapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// yamlMapSet 设置映射中的键（已存在则原位替换，否则追加）
func yamlMapSet(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
}

// yamlMapDelete 删除映射中的键（不存在则忽略）
func yamlMapDelete(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

func yamlStr(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

// yamlFromAny 把 Go 值（map/slice/标量）转为 YAML 节点
func yamlFromAny(v any) (*yaml.Node, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("空值无法转为 YAML 节点")
	}
	return doc.Content[0], nil
}

// setYAMLFlat 设置 YAML 顶层扁平键（key: value，行首无缩进）；已存在则原位替换，否则追加。
// value 需自带引号等格式。供 Aider 的扁平配置使用。
func setYAMLFlat(text, key, value string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, key+":") {
			continue
		}
		lines[i] = key + ": " + value
		return strings.Join(lines, "\n")
	}
	out := strings.TrimRight(text, "\n")
	if out != "" {
		out += "\n"
	}
	return out + key + ": " + value + "\n"
}

// upsertYAMLProviderEntry 在 YAML 配置顶层 provider map 中写入/替换 agentProviderID 条目
// （MiniMax Code 的 config.yaml 结构与 opencode 的 provider 同族）。其余 provider 原样保留。
func upsertYAMLProviderEntry(path, baseURL, apiKey string, models []string) error {
	doc, err := yamlLoadRoot(path, true)
	if err != nil {
		return err
	}
	root := doc.Content[0]
	provider := yamlMapGet(root, "provider")
	if provider == nil || provider.Kind != yaml.MappingNode {
		provider = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	yamlMapDelete(provider, agentProviderID)

	modelsMap := map[string]any{}
	for _, m := range models {
		modelsMap[m] = map[string]any{
			"name": m, "reasoning": true, "tool_call": true,
			"limit": map[string]any{"context": 200000, "output": 32768},
		}
	}
	entry, err := yamlFromAny(map[string]any{
		"name": agentProviderName,
		"npm":  "@ai-sdk/openai-compatible",
		"options": map[string]any{
			"baseURL": baseURL + "/v1",
			"apiKey":  apiKey,
		},
		"models": modelsMap,
	})
	if err != nil {
		return err
	}
	yamlMapSet(provider, agentProviderID, entry)
	yamlMapSet(root, "provider", provider)
	return yamlSave(path, doc)
}

// upsertContinueModels 在 Continue 的 config.yaml models[] 中追加网关模型条目，
// 并清掉指向同 baseURL 的旧条目（name 带 WorkBuddy 前缀）。其余条目原样保留。
func upsertContinueModels(path, baseURL, apiKey string, models []string) error {
	doc, err := yamlLoadRoot(path, true)
	if err != nil {
		return err
	}
	root := doc.Content[0]
	if len(root.Content) == 0 {
		// 全新文件：补 Continue 要求的基本头字段
		yamlMapSet(root, "name", yamlStr("workbuddy-gateway"))
		yamlMapSet(root, "version", yamlStr("0.0.1"))
		yamlMapSet(root, "schema", yamlStr("v1"))
	}
	seq := yamlMapGet(root, "models")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	}
	kept := make([]*yaml.Node, 0, len(seq.Content)+len(models))
	for _, item := range seq.Content {
		if item.Kind == yaml.MappingNode {
			if nm := yamlMapGet(item, "name"); nm != nil && strings.HasPrefix(nm.Value, agentProviderName+" ") {
				continue // 清掉自家旧条目
			}
		}
		kept = append(kept, item)
	}
	for _, m := range models {
		node, err := yamlFromAny(map[string]any{
			"name":     agentProviderName + " " + m,
			"provider": "openai",
			"model":    m,
			"apiBase":  baseURL + "/v1",
			"apiKey":   apiKey,
			"roles":    []string{"chat", "edit"},
		})
		if err != nil {
			return err
		}
		kept = append(kept, node)
	}
	seq.Content = kept
	yamlMapSet(root, "models", seq)
	return yamlSave(path, doc)
}
