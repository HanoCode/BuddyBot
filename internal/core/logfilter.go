package core

import (
	"io"
	"log"
	"os"
	"strings"
	"sync"
)

// stdLogFilter 标准库 log 输出过滤器：丢弃已知良性噪音。
//
// net/http 的 Transport 在「空闲 keep-alive 连接上收到对端主动推来的响应」时
// 会打 "Unsolicited response received on idle HTTP channel" 日志。本项目网关
// 的触发路径：下游客户端断开/取消 → 上游请求随 r.Context() 一并取消 → 上游
// 迟到的响应字节落在已被 transport 回收的连接上。err=<nil>，不影响任何真实
// 请求，属于纯噪音，故按行过滤。
type stdLogFilter struct {
	mu  sync.Mutex
	dst io.Writer
}

func (w *stdLogFilter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "Unsolicited response received on idle HTTP channel") {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dst.Write(p)
}

// InstallStdLogFilter 把标准库 log 的默认输出接到过滤器上（main 启动时调用一次）。
func InstallStdLogFilter() {
	log.SetOutput(&stdLogFilter{dst: os.Stderr})
}
