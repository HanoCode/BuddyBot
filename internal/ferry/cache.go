package ferry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 判定缓存：把「这个模型能不能按客户端的方式干活」这条结论跨重启留下来。
//
// 两个刻意的区分：
//  1. 这里存的是**探测判定**，不是某次真实调用的观察。混在一起的话，一次上游抽风
//     就会把模型从可用列表里撤掉。
//  2. 失败也分性质：超时和限流是**当时的负载**（缓存 10 分钟），已下线、无权限、
//     额度用尽才是**关于这个模型的结论**（缓存 1 小时）；两者都远短于成功判定的 6 小时。

type cacheEntry struct {
	OK          bool   `json:"ok"`
	ChatOnly    bool   `json:"chatOnly,omitempty"`
	Category    string `json:"category"`
	Message     string `json:"message,omitempty"`
	DurationMs  int64  `json:"durationMs,omitempty"`
	At          int64  `json:"at"`
	Fingerprint string `json:"fingerprint"`
}

type cacheFile struct {
	Version int                   `json:"version"`
	Entries map[string]cacheEntry `json:"entries"`
}

type probeCache struct {
	mu       sync.Mutex
	path     string
	entries  map[string]cacheEntry
	okTTL    time.Duration
	shortTTL time.Duration
	rejectTTL time.Duration
	logf     func(string, ...any)
}

func newProbeCache(path string, cfg Config, logf func(string, ...any)) *probeCache {
	return &probeCache{
		path: path, entries: map[string]cacheEntry{},
		okTTL: cfg.OKTTL, shortTTL: cfg.TransientTTL, rejectTTL: cfg.RejectTTL, logf: logf,
	}
}

// load 读缓存失败不是错误路径：最坏情况就是这一轮全量重测。
func (c *probeCache) load() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := os.ReadFile(c.path)
	if err != nil {
		return 0
	}
	var file cacheFile
	if json.Unmarshal(raw, &file) != nil || file.Version != probeCacheVersion || file.Entries == nil {
		return 0
	}
	for id, entry := range file.Entries {
		if entry.At > 0 {
			c.entries[id] = entry
		}
	}
	return len(c.entries)
}

// fingerprint 目录里同一个模型的能力声明变了，旧判定就不一定还成立
func fingerprint(model Model) string {
	return strings.Join([]string{
		strconv.FormatBool(model.Toolcall), strconv.FormatBool(model.Reasoning), strconv.FormatBool(model.Images),
		strconv.FormatInt(model.Context, 10), strconv.FormatInt(model.Output, 10),
	}, "|")
}

func (c *probeCache) ttlFor(entry cacheEntry) time.Duration {
	if entry.OK {
		return c.okTTL
	}
	switch entry.Category {
	case "timeout", "rate_limit":
		return c.shortTTL
	}
	return c.rejectTTL
}

// fresh 新鲜且能力指纹一致的判定才算数；其余当作没有，交给探测
func (c *probeCache) fresh(model Model) *cacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[model.ID]
	if !ok || entry.Fingerprint != fingerprint(model) {
		return nil
	}
	if time.Since(time.UnixMilli(entry.At)) >= c.ttlFor(entry) {
		return nil
	}
	copied := entry
	return &copied
}

func (c *probeCache) record(model Model, entry cacheEntry) {
	c.mu.Lock()
	entry.Fingerprint = fingerprint(model)
	if entry.At == 0 {
		entry.At = time.Now().UnixMilli()
	}
	c.entries[model.ID] = entry
	snapshot := c.snapshotLocked()
	c.mu.Unlock()
	c.flush(snapshot)
}

// prune 目录里已经没有的模型，判定留着只会让缓存越来越大
func (c *probeCache) prune(models []Model) int {
	alive := map[string]bool{}
	for _, model := range models {
		alive[model.ID] = true
	}
	c.mu.Lock()
	dropped := 0
	for id := range c.entries {
		if !alive[id] {
			delete(c.entries, id)
			dropped++
		}
	}
	snapshot := c.snapshotLocked()
	c.mu.Unlock()
	if dropped > 0 {
		c.flush(snapshot)
	}
	return dropped
}

func (c *probeCache) snapshotLocked() cacheFile {
	entries := make(map[string]cacheEntry, len(c.entries))
	for id, entry := range c.entries {
		entries[id] = entry
	}
	return cacheFile{Version: probeCacheVersion, Entries: entries}
}

func (c *probeCache) flush(file cacheFile) {
	payload, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return
	}
	if err := atomicWriteFileMode(c.path, string(payload)+"\n", 0o600); err != nil {
		c.logf("判定缓存写入失败：%v", err)
	}
}

// ---- 判定归类 ----

var quotaPatterns = []string{
	"insufficient_quota", "insufficient quota", "out of credits", "insufficient credit",
	"insufficient balance", "额度不足", "额度用尽", "配额",
}

var rateLimitPatterns = []string{"rate limit", "rate_limit", "too many requests"}
var timeoutPatterns = []string{"timeout", "timed out", "超时"}

// classifyVerdict 上游没有可查的剩余额度接口，所以失败只能按错误内容归类。
// 归类只影响界面上的标签，不参与「能不能用」的判断。
func classifyVerdict(ok bool, message string, status int) string {
	if ok {
		return "available"
	}
	lower := strings.ToLower(message)
	for _, pattern := range quotaPatterns {
		if strings.Contains(lower, pattern) {
			return "quota"
		}
	}
	if status == 429 {
		return "rate_limit"
	}
	for _, pattern := range rateLimitPatterns {
		if strings.Contains(lower, pattern) {
			return "rate_limit"
		}
	}
	if status == 401 || status == 403 {
		return "access"
	}
	// 499 在本进程里只由「我们自己的 context 被取消/超时」产生（见 backend.request），
	// 探测场景下就是探测预算用尽，按当时的负载归类，别按模型结论缓存一小时。
	if status == 499 {
		return "timeout"
	}
	for _, pattern := range timeoutPatterns {
		if strings.Contains(lower, pattern) {
			return "timeout"
		}
	}
	return "error"
}

// verdictFromError 把一次失败整理成判定。
// model 必须由调用方给出：判定是按模型 ID 索引的，漏填就会写成空键判定，
// 界面上那个模型就会永远停在「未检测」（失败原因也一起丢掉）。
func verdictFromError(model string, err *BridgeError) Verdict {
	status, code, message := 0, "", ""
	if err != nil {
		status, code, message = err.Status, err.Code, err.Message
	}
	if code == "timeout" {
		message = "模型探测超时"
	}
	return Verdict{
		Model: model, OK: false, Category: classifyVerdict(false, message, status),
		Time: nowISO(), Status: status, Code: code, Error: message,
	}
}

// ---- 原子写 ----

func atomicWriteFileMode(path, content string, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, ".ferry-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return err
	}
	if err := replaceFileWithRetry(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("替换 %s 失败: %w", path, err)
	}
	return nil
}
