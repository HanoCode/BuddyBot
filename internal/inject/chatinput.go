package inject

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// insertChatText 把文本写入客户端的对话输入框。
//
// WorkBuddy 的输入框是 Slate 富文本编辑器（data-slate-editor="true"），前端合成事件
// （execCommand / beforeinput / 派发 paste）改的只是 DOM，Slate 内部 model 不同步——
// 表现为文字"进去了"但发送按钮不亮、剪切/删除错乱。必须走 CDP Input.insertText
// （协议层受信任输入，等价真实键盘/IME 输入），Slate 才会完整更新 model。
func (m *Manager) insertChatText(text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("文本为空")
	}
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn == nil || !conn.alive() {
		return fmt.Errorf("客户端未连接（请先完成注入）")
	}

	kind, err := focusChatEditor(conn)
	if err != nil {
		return err
	}
	switch kind {
	case "slate":
		return slateType(conn, text)
	case "input", "plain":
		return fallbackInsert(conn, text)
	default:
		return fmt.Errorf("未找到对话输入框（请先进入会话页面）")
	}
}

// focusChatEditor 聚焦对话输入框并把光标放到末尾。
// 返回 "slate"（Slate 编辑器）、"input"（textarea/input）、"plain"（其它 contenteditable）或 ""（未找到）。
func focusChatEditor(c *cdpConn) (string, error) {
	res, err := c.evaluate(`(function(){
		var panel=document.getElementById("wbdesk-panel");
		var inside=function(el){return panel&&panel.contains(el);};
		var eds=Array.prototype.slice.call(document.querySelectorAll('[data-slate-editor="true"]')).filter(function(e){
			var r=e.getBoundingClientRect(); return r.width>0&&r.height>0&&!inside(e);
		});
		if(eds.length){
			var ed=eds[eds.length-1];
			ed.focus();
			var sel=getSelection(),range=document.createRange();
			range.selectNodeContents(ed);range.collapse(false);
			sel.removeAllRanges();sel.addRange(range);
			return "slate";
		}
		var list=document.querySelectorAll('textarea,input[type="text"],[contenteditable="true"]');
		for(var i=list.length-1;i>=0;i--){
			var el=list[i];
			if(inside(el))continue;
			var r=el.getBoundingClientRect();
			if(r.width<=0||r.height<=0)continue;
			if(r.top<innerHeight*0.25)continue; // 输入框一般在下半屏
			el.focus();
			if(el.tagName==="TEXTAREA"||el.tagName==="INPUT"){
				try{el.selectionStart=el.selectionEnd=el.value?el.value.length:0;}catch(e){}
				return "input";
			}
			return "plain";
		}
		return "";
	})()`, 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("定位对话输入框失败: %w", err)
	}
	var kind string
	if err := json.Unmarshal(res, &kind); err != nil {
		return "", fmt.Errorf("解析输入框类型失败: %w", err)
	}
	return kind, nil
}

// slateType 向已聚焦的 Slate 编辑器逐行输入文本，行间用 Shift+Enter 软换行。
// 注意裸 Enter 会直接发送消息，绝对不能用。
func slateType(c *cdpConn, text string) error {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if i > 0 {
			// Shift+Enter 软换行（modifiers: Shift=8）
			if _, err := c.call("Input.dispatchKeyEvent", map[string]any{
				"type": "keyDown", "modifiers": 8, "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13,
			}, 3*time.Second); err != nil {
				return fmt.Errorf("模拟换行失败: %w", err)
			}
			_, _ = c.call("Input.dispatchKeyEvent", map[string]any{
				"type": "keyUp", "modifiers": 8, "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13,
			}, 3*time.Second)
		}
		if line == "" {
			continue
		}
		if _, err := c.call("Input.insertText", map[string]any{"text": line}, 5*time.Second); err != nil {
			return fmt.Errorf("输入文本失败: %w", err)
		}
	}
	return nil
}

// fallbackInsert 非 Slate 输入框的回退：textarea/input 用 React setter，contenteditable 用 execCommand。
func fallbackInsert(c *cdpConn, text string) error {
	raw, err := json.Marshal(text)
	if err != nil {
		return err
	}
	expr := `(function(t){
		var el=document.activeElement;
		if(el&&(el.tagName==="TEXTAREA"||el.tagName==="INPUT")){
			var proto=el.tagName==="TEXTAREA"?HTMLTextAreaElement.prototype:HTMLInputElement.prototype;
			var setter=Object.getOwnPropertyDescriptor(proto,"value").set; // 绕过 React 覆写的 value
			setter.call(el,(el.value?el.value.replace(/\n$/,"")+"\n":"")+t);
			el.dispatchEvent(new Event("input",{bubbles:true}));
			el.focus();
			try{el.selectionStart=el.selectionEnd=el.value.length;}catch(e){}
			return "ok";
		}
		try{document.execCommand("insertText",false,t);}catch(e){}
		return "ok";
	})(` + string(raw) + `)`
	_, err = c.evaluate(expr, 5*time.Second)
	return err
}
