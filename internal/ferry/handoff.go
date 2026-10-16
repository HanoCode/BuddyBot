package ferry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// 执行权在外部客户端手里。模型若在本机动手，被拦下的那个动作应该直接转交成外部调用，
// 而不是让它复述一遍——「复述」恰好是模型最容易做错的一步，还多花一整轮上游调用。
//
// 每次请求里客户端附带的工具表就是权威：只填目标 schema 里存在的字段，
// 必填项凑不齐就不转交，退回给模型一条带原因的纠正反馈。

type handoffCategory struct {
	native  []string
	targets []string
	fields  map[string][]string
}

var handoffCategories = []handoffCategory{
	{
		native:  []string{"bash", "shell"},
		targets: []string{"Bash", "PowerShell"},
		fields:  map[string][]string{"command": {"command"}, "description": {"description"}, "timeout": {"timeout"}},
	},
	{
		native:  []string{"read"},
		targets: []string{"Read"},
		fields: map[string][]string{
			"filePath": {"file_path"}, "file_path": {"file_path"}, "path": {"file_path"},
			"offset": {"offset"}, "limit": {"limit"},
		},
	},
	{
		native:  []string{"write"},
		targets: []string{"Write"},
		fields: map[string][]string{
			"filePath": {"file_path"}, "file_path": {"file_path"}, "path": {"file_path"}, "content": {"content"},
		},
	},
	{
		native:  []string{"edit", "multiedit", "multi_edit", "patch", "apply_patch"},
		targets: []string{"Edit", "MultiEdit"},
		fields: map[string][]string{
			"filePath": {"file_path"}, "file_path": {"file_path"}, "path": {"file_path"},
			"oldString": {"old_string"}, "old_string": {"old_string"},
			"newString": {"new_string"}, "new_string": {"new_string"},
			"replaceAll": {"replace_all"}, "replace_all": {"replace_all"}, "edits": {"edits"},
		},
	},
	{
		// 同一个字段名可能有多个可接受的目标写法：外部工具之间拼写并不统一
		native:  []string{"glob"},
		targets: []string{"Glob", "LS"},
		fields:  map[string][]string{"pattern": {"pattern"}, "path": {"path", "directory"}, "include": {"include"}},
	},
	{
		native:  []string{"grep"},
		targets: []string{"Grep", "Search"},
		fields: map[string][]string{
			"pattern": {"pattern"}, "path": {"path", "directory"}, "include": {"include"}, "output_mode": {"output_mode"},
		},
	},
	{
		native:  []string{"skill"},
		targets: []string{"Skill"},
		fields: map[string][]string{
			"name": {"name", "skill"}, "skill": {"skill", "name"},
			"args": {"args", "arguments"}, "arguments": {"arguments", "args"},
		},
	},
}

// handoffResult 一次成功的转交
type handoffResult struct {
	Name      string
	Arguments map[string]any
}

// buildHandoff 把被拦下的本机动作映射成本次请求工具表里存在的外部工具
func buildHandoff(native string, input map[string]any, tools []Tool) *handoffResult {
	name := strings.ToLower(native)
	var category *handoffCategory
	for i := range handoffCategories {
		if containsString(handoffCategories[i].native, name) {
			category = &handoffCategories[i]
			break
		}
	}
	if category == nil {
		return nil
	}
	for _, target := range category.targets {
		var spec *Tool
		for i := range tools {
			if strings.EqualFold(tools[i].Function.Name, target) {
				spec = &tools[i]
				break
			}
		}
		if spec == nil {
			continue
		}
		properties, _ := spec.Function.Parameters["properties"].(map[string]any)
		if properties == nil {
			continue
		}
		args := map[string]any{}
		for key, value := range input {
			if value == nil {
				continue
			}
			candidates, ok := category.fields[key]
			if !ok {
				continue
			}
			for _, mapped := range candidates {
				if _, taken := args[mapped]; taken {
					continue
				}
				if _, declared := properties[mapped]; !declared {
					continue
				}
				args[mapped] = value
				break
			}
		}
		if len(args) == 0 {
			continue
		}
		if !requiredSatisfied(spec.Function.Parameters, args) {
			continue
		}
		return &handoffResult{Name: spec.Function.Name, Arguments: args}
	}
	return nil
}

// requiredNames 归一化 schema 里的 required。
// 客户端传来的 JSON 解出来是 []any，而本项目内部构造的工具（探测请求、测试）直接给 []string；
// 只认 []any 会让类型断言落空、必填校验被静默跳过——「缺参数也放行」是最危险的那种漏。
func requiredNames(parameters map[string]any) []string {
	switch v := parameters["required"].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if key, ok := item.(string); ok && key != "" {
				out = append(out, key)
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(v))
		for _, key := range v {
			if key != "" {
				out = append(out, key)
			}
		}
		return out
	case string:
		if v != "" {
			return []string{v}
		}
	}
	return nil
}

func requiredSatisfied(parameters map[string]any, args map[string]any) bool {
	for _, key := range requiredNames(parameters) {
		value, ok := args[key]
		if !ok || value == nil {
			return false
		}
		if s, isString := value.(string); isString && s == "" {
			return false
		}
	}
	return true
}

// handoffInput 调用还在等审批时，OpenCode 把参数放在审批元数据里，
// 工具 part 的 input 要等执行才有值。有 part 的时候以 part 为准，元数据只补它缺的那些字段。
func handoffInput(actionInput, metadata map[string]any) map[string]any {
	input := map[string]any{}
	for k, v := range actionInput {
		input[k] = v
	}
	fallback := map[string]any{
		"command":  metadata["command"],
		"filePath": firstAny(metadata["filepath"], metadata["filePath"], metadata["path"]),
		"pattern":  firstAny(metadata["pattern"], firstOfSlice(metadata["patterns"])),
	}
	for key, value := range fallback {
		if _, exists := input[key]; exists {
			continue
		}
		if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
			input[key] = s
		}
	}
	return input
}

func firstAny(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

func firstOfSlice(value any) any {
	if list, ok := value.([]any); ok && len(list) > 0 {
		return list[0]
	}
	return nil
}

// rejectFeedback 拒绝必须点名工具和原因：含糊的拒绝会让模型继续瞎猜，
// 而「反复尝试本地执行」正是含糊拒绝逼出来的行为。
func rejectFeedback(native, reason string) string {
	return fmt.Sprintf("Native tool %q was blocked: %s. Native execution is forbidden; the external client owns execution. "+
		"Return the requested external action inside the calls array using StructuredOutput. The external client will execute it and supply results. "+
		"Do not call any other native tools.", native, reason)
}

// validateAction 翻译出来的动作不享受任何信任：由接收方自己的 schema 裁决，
// 和静态映射表走同一套规则，这样「翻译」这条路径永远不可能放宽可执行范围。
func validateAction(candidate map[string]any, tools []Tool) (*handoffResult, *BridgeError) {
	if candidate == nil {
		return nil, newBridgeError(http.StatusBadGateway, "invalid_tool_call", "翻译结果不是对象")
	}
	name, _ := candidate["name"].(string)
	var spec *Tool
	for i := range tools {
		if strings.EqualFold(tools[i].Function.Name, name) {
			spec = &tools[i]
			break
		}
	}
	if spec == nil {
		return nil, newBridgeError(http.StatusBadGateway, "invalid_tool_call", "翻译结果点名了接收方没有提供的工具")
	}
	args := map[string]any{}
	if raw, ok := candidate["arguments"]; ok && raw != nil {
		parsed, ok := raw.(map[string]any)
		if !ok {
			return nil, newBridgeError(http.StatusBadGateway, "invalid_tool_call", "翻译结果的参数不是对象")
		}
		args = parsed
	}
	properties, _ := spec.Function.Parameters["properties"].(map[string]any)
	for key := range args {
		if properties == nil {
			return nil, newBridgeError(http.StatusBadGateway, "invalid_tool_call",
				"翻译结果设置了接收方未声明的参数：%s", key)
		}
		if _, declared := properties[key]; !declared {
			return nil, newBridgeError(http.StatusBadGateway, "invalid_tool_call",
				"翻译结果设置了接收方未声明的参数：%s", key)
		}
	}
	if !requiredSatisfied(spec.Function.Parameters, args) {
		return nil, newBridgeError(http.StatusBadGateway, "invalid_tool_call", "翻译结果缺少必填参数")
	}
	return &handoffResult{Name: spec.Function.Name, Arguments: args}, nil
}

// shrinkPermission 审批载荷里可能带着整个文件内容：形状保留，长字符串截断
func shrinkPermission(value any, limit int) any {
	switch v := value.(type) {
	case string:
		if len(v) > limit {
			return fmt.Sprintf("%s…[%d chars]", v[:limit], len(v))
		}
		return v
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, shrinkPermission(item, limit))
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for key, item := range v {
			out[key] = shrinkPermission(item, limit)
		}
		return out
	default:
		return value
	}
}

// allowedTools 本次请求真正允许模型选择的工具
func allowedTools(request *Request) []Tool {
	if request.Choice == "none" {
		return nil
	}
	if request.Forced == "" {
		return request.Tools
	}
	out := make([]Tool, 0, 1)
	for _, tool := range request.Tools {
		if tool.Function.Name == request.Forced {
			out = append(out, tool)
		}
	}
	return out
}

func toolCatalog(tools []Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		item := map[string]any{"name": tool.Function.Name}
		if tool.Function.Description != "" {
			item["description"] = tool.Function.Description
		}
		if tool.Function.Parameters != nil {
			item["parameters"] = tool.Function.Parameters
		}
		out = append(out, item)
	}
	return out
}

func mustJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(raw)
}
