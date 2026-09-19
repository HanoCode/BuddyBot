package api

import (
	"fmt"

	"workbuddy-desktop/internal/core"
)

// ensureWritable 只读模式守卫：Security.read_only 开启时拒绝一切管理侧变更。
func ensureWritable(s *core.Service) error {
	if s.ReadOnly() {
		return fmt.Errorf("只读模式已开启，禁止管理侧变更（可在「设置 → 安全」中关闭）")
	}
	return nil
}

// audit 记录管理侧审计日志（配置变更、密钥管理、账号管理等敏感操作）。
func audit(s *core.Service, action, target, detail string) {
	s.Store().Audit(action, target, detail)
}
