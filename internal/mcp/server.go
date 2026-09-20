// Package mcp 实现最小化的 MCP (Model Context Protocol) stdio server。
//
// 目的：把 BuddyBot 的本机数据（账号池状态 / 单价表 / 会话日志）暴露为
// 编程智能体（Claude Code / Codex / OpenCode 等）可调用的 MCP 工具，
// 让 agent 在工作中自助查询资源与历史，而不需要 BuddyBot GUI 在前台。
//
// 实现说明：MCP stdio 传输是「每行一条 JSON-RPC 2.0 消息」，方法集很小
// （initialize / tools/list / tools/call / ping），按规范手写即可，
// 不引入任何第三方依赖（与仓库「零额外依赖」约定一致）。
//
// 运行方式：`BuddyBot mcp`（见 main.go 子命令分支）。
// 该模式下不启动 GUI / 网关 / 单实例锁，只读 ~/.buddybot 数据，
// 与运行中的 BuddyBot 主程序互不干扰。
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"workbuddy-desktop/internal/core"
)

const (
	serverName    = "buddybot"
	serverVersion = "0.1.0"
	// defaultProtocolVersion 客户端未声明时的兜底协议版本
	defaultProtocolVersion = "2025-06-18"
	// maxLineBytes 单条请求上限：超长行丢弃并回 parse error，不终止进程
	maxLineBytes = 64 << 20
)

// rpcRequest JSON-RPC 2.0 请求（ID 缺失 = notification，不回包）
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// rpcError JSON-RPC 2.0 错误对象
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// nullID 无法关联请求 ID 时（解析失败）的占位值
var nullID = json.RawMessage("null")

// Run 启动 stdio MCP server：从 stdin 逐行读请求，向 stdout 逐行写响应。
// 进程生命周期即 server 生命周期（stdin EOF 退出），由接入的客户端托管。
func Run(svc *core.Service) error {
	// MCP server 常驻整个 agent 会话：不应持有防休眠断言
	svc.StopAwake()

	r := bufio.NewReaderSize(os.Stdin, 1<<20)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	for {
		line, tooLong, err := readLine(r, maxLineBytes)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if tooLong {
			writeResult(out, nullID, nil, &rpcError{Code: -32700, Message: "parse error: line too long"})
			continue
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			// 无法解析的行无法关联 ID，按规范回 parse error（ID 为 null）
			writeResult(out, nullID, nil, &rpcError{Code: -32700, Message: "parse error"})
			continue
		}
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue // notification：不回包
		}
		handleRequest(out, svc, &req)
	}
}

// readLine 读一行（不含换行）。超过 max 时丢弃该行其余内容并返回 tooLong——
// 不用 bufio.Scanner：它在超长行上会让后续读取永久失败（等于把 MCP 进程打死）。
func readLine(r *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	for {
		chunk, e := r.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > max {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if e == bufio.ErrBufferFull {
			continue // 行太长：继续吃缓冲直到换行
		}
		if e != nil {
			return line, tooLong, e
		}
		return line, tooLong, nil
	}
}

// handleRequest 分发单条请求并写出响应
func handleRequest(out *bufio.Writer, svc *core.Service, req *rpcRequest) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := p.ProtocolVersion
		if version == "" {
			version = defaultProtocolVersion
		}
		writeResult(out, req.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
		}, nil)
	case "ping":
		writeResult(out, req.ID, map[string]any{}, nil)
	case "tools/list":
		writeResult(out, req.ID, map[string]any{"tools": toolDefs()}, nil)
	case "tools/call":
		handleToolCall(out, svc, req.ID, req.Params)
	default:
		writeResult(out, req.ID, nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method})
	}
}

// toolCallParams tools/call 的请求参数
type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// handleToolCall 执行工具调用；工具内错误按 MCP 规范以 isError 结果回传，
// 只有协议层错误才回 JSON-RPC error。
func handleToolCall(out *bufio.Writer, svc *core.Service, id json.RawMessage, rawParams json.RawMessage) {
	var p toolCallParams
	if err := json.Unmarshal(rawParams, &p); err != nil || p.Name == "" {
		writeResult(out, id, nil, &rpcError{Code: -32602, Message: "invalid params"})
		return
	}
	text, toolErr := callTool(svc, p.Name, p.Arguments)
	result := map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	}
	if toolErr != nil {
		result["isError"] = true
		text = fmt.Sprintf("工具执行失败：%v", toolErr)
		result["content"] = []map[string]any{{"type": "text", "text": text}}
	}
	writeResult(out, id, result, nil)
}

// writeResult 写出响应。id 用 json.RawMessage 原样回显，
// 不经 float64 中转——超大整数 ID 会因此失真，客户端会等不到自己的响应。
func writeResult(out io.Writer, id json.RawMessage, result any, rpcErr *rpcError) {
	if len(id) == 0 {
		id = nullID
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = out.Write(append(b, '\n'))
	if f, ok := out.(*bufio.Writer); ok {
		_ = f.Flush()
	}
}
