package ferry

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// newID 生成一个短随机 id（对齐参考实现里去掉连字符的 UUID 用法）
func newID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(buf)
}

func nowUnix() int64 { return time.Now().Unix() }

func nowISO() string { return time.Now().Format(time.RFC3339) }
