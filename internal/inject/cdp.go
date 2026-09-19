package inject

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// ============================================================
// 最小 CDP（Chrome DevTools Protocol）客户端
//
// 只实现注入面板所需的三件事：
//   1. 列出调试目标（GET /json/list）并挑选页面
//   2. 通过 WebSocket 调用 Runtime.evaluate 注入/执行 JS
//   3. Runtime.addBinding 接收面板回传的动作（免打扰开关、账号切换等）
//
// 有意不引入 chromedp：这里只附着到已存在的页面做单向控制，
// 不需要标签页生命周期管理等重依赖能力。
// ============================================================

// Target 调试目标（/json/list 条目的子集）
type Target struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
	Type  string `json:"type"`
}

// listTargets 拉取调试端口上的全部目标
func listTargets(port int) ([]Target, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", port))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var ts []Target
	if err := json.NewDecoder(resp.Body).Decode(&ts); err != nil {
		return nil, err
	}
	return ts, nil
}

// pickPageTarget 选第一个 page 类型目标（官方客户端主窗口）
func pickPageTarget(port int) (Target, error) {
	ts, err := listTargets(port)
	if err != nil {
		return Target{}, fmt.Errorf("CDP 端口 %d 不可达（客户端未以调试模式运行）: %w", port, err)
	}
	for _, t := range ts {
		if t.Type == "page" {
			return t, nil
		}
	}
	return Target{}, fmt.Errorf("CDP 端口 %d 上没有页面型目标", port)
}

// cdpConn 与单个页面目标的 CDP WebSocket 连接
type cdpConn struct {
	mu     sync.Mutex
	ws     *websocket.Conn
	nextID int64

	// 调用应答路由：id → channel
	callMu sync.Mutex
	calls  map[int64]chan cdpMessage

	// 面板回传（Runtime.bindingCalled）
	onBinding func(name, payload string)

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

type cdpMessage struct {
	ID     int64           `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// dialCDP 连接页面目标并启动读循环
func dialCDP(ctx context.Context, port int, targetID string, onBinding func(name, payload string)) (*cdpConn, error) {
	url := fmt.Sprintf("ws://127.0.0.1:%d/devtools/page/%s", port, targetID)
	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	defer dialCancel()
	ws, _, err := websocket.Dial(dialCtx, url, nil)
	if err != nil {
		return nil, fmt.Errorf("连接 CDP WebSocket 失败: %w", err)
	}
	// 面板回传可能携带 base64 图片（自定义壁纸/宠物上传），默认 32KB 读上限会直接断连
	ws.SetReadLimit(64 << 20)

	c := &cdpConn{
		ws:        ws,
		calls:     map[int64]chan cdpMessage{},
		onBinding: onBinding,
		done:      make(chan struct{}),
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	go c.readLoop()
	return c, nil
}

// readLoop 读循环：按 id 路由应答，bindingCalled 事件回调
func (c *cdpConn) readLoop() {
	defer close(c.done)
	for {
		_, data, err := c.ws.Read(c.ctx)
		if err != nil {
			// 连接关闭/上下文取消：唤醒所有等待者，让 Call 返回错误
			c.callMu.Lock()
			for id, ch := range c.calls {
				select {
				case ch <- cdpMessage{ID: id}:
				default:
				}
			}
			c.callMu.Unlock()
			return
		}
		var msg cdpMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.Method == "Runtime.bindingCalled" {
			var p struct {
				Name       string `json:"name"`
				Payload    string `json:"payload"`
				ExecutionC int64  `json:"executionContextId"`
			}
			if json.Unmarshal(msg.Params, &p) == nil && c.onBinding != nil {
				c.onBinding(p.Name, p.Payload)
			}
			continue
		}
		if msg.ID != 0 {
			c.callMu.Lock()
			if ch, ok := c.calls[msg.ID]; ok {
				select {
				case ch <- msg:
				default:
				}
				delete(c.calls, msg.ID)
			}
			c.callMu.Unlock()
		}
	}
}

// call 发送一条 CDP 命令并等待应答（串行调用，整连接一把锁足够）
func (c *cdpConn) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextID++
	id := c.nextID
	body, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}

	ch := make(chan cdpMessage, 1)
	c.callMu.Lock()
	c.calls[id] = ch
	c.callMu.Unlock()
	defer func() {
		c.callMu.Lock()
		delete(c.calls, id)
		c.callMu.Unlock()
	}()

	wctx, wcancel := context.WithTimeout(c.ctx, timeout)
	defer wcancel()
	if err := c.ws.Write(wctx, websocket.MessageText, body); err != nil {
		return nil, fmt.Errorf("发送 %s 失败: %w", method, err)
	}

	select {
	case msg := <-ch:
		if msg.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, msg.Error.Message)
		}
		return msg.Result, nil
	case <-c.ctx.Done():
		return nil, fmt.Errorf("CDP 连接已关闭")
	case <-time.After(timeout):
		return nil, fmt.Errorf("%s 超时（%s）", method, timeout)
	}
}

// evaluate 在页面里执行 JS，返回 JSON 反序列化的返回值（returnByValue）
func (c *cdpConn) evaluate(expr string, timeout time.Duration) (json.RawMessage, error) {
	res, err := c.call("Runtime.evaluate", map[string]any{
		"expression":   expr,
		"returnByValue": true,
		"awaitPromise":  true,
	}, timeout)
	if err != nil {
		return nil, err
	}
	var r struct {
		Result struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails,omitempty"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, err
	}
	if r.ExceptionDetails != nil {
		return nil, fmt.Errorf("页面执行异常: %s", r.ExceptionDetails.Text)
	}
	if r.Result.Type == "undefined" {
		return json.RawMessage("null"), nil
	}
	return r.Result.Value, nil
}

// close 关闭连接
func (c *cdpConn) close() {
	c.cancel()
	_ = c.ws.Close(websocket.StatusNormalClosure, "stop")
	<-c.done
}

// alive 连接是否仍可用（读循环退出 = 页面刷新/关闭/断开）
func (c *cdpConn) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}
