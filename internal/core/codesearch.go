package core

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ============================================================
// 本地代码库关键词检索（MCP search_code 工具的数据层）
//
// 先用「纯 Go 逐文件逐行匹配」实现：零依赖、结果可解释、命中即带行号，
// 覆盖编程智能体最常见的「这个符号/字符串在哪里用过」诉求。
// 语义检索（embedding）留待真实需要时再补——先不做，避免引入模型与索引基建。
//
// 三点边界（这是给智能体调用的工具，不能变成整盘扫描器/任意文件读取器）：
//   1. 拒绝过宽的根目录（/、/Users、整个家目录…），只接受具体项目目录
//   2. 敏感目录（密钥/凭据）一律不进检索
//   3. 硬预算：文件数 + 时长，触及即停并如实标注，避免把单线程 MCP server 卡死
// 注意这是「缩小爆炸半径」，不是沙箱：目标目录内的普通文件仍会被读取。
// ============================================================

// CodeSearchHit 单条命中
type CodeSearchHit struct {
	Path    string `json:"path"` // 相对 root 的路径
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
}

// 停止原因（前端/MCP 文本据此解释结果为何不完整）
const (
	CodeSearchStopHits   = "hits"   // 命中数达上限
	CodeSearchStopBudget = "budget" // 触及扫描预算（文件数/时长）
)

// CodeSearchResult 一次代码检索结果
type CodeSearchResult struct {
	Query        string          `json:"query"`
	Root         string          `json:"root"`
	FilesScanned int             `json:"filesScanned"`
	FilesSkipped int             `json:"filesSkipped"`
	StopReason   string          `json:"stopReason,omitempty"` // 空 = 扫完了
	Hits         []CodeSearchHit `json:"hits"`
	MatchedFiles int             `json:"matchedFiles"`
}

// codeSearchSkipDirs 跳过目录：依赖/构建产物/版本库，进了只会徒增噪声
var codeSearchSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "dist": true, "build": true,
	"vendor": true, ".next": true, "target": true, "__pycache__": true,
	".venv": true, "venv": true, ".idea": true, ".cache": true,
}

// codeSearchSensitiveDir 敏感目录（按路径后缀判断）：密钥/凭据不进检索结果。
// 刻意放宽到「任何位置的同名目录」——宁可少检索，也不把 id_rsa 送进模型上下文。
func codeSearchSensitiveDir(path string) bool {
	for _, seg := range []string{
		"/.ssh", "/.aws", "/.gnupg", "/.buddybot", "/.config", "/.kube", "/.docker",
		"/Library/Keychains",
	} {
		if strings.HasSuffix(path, seg) {
			return true
		}
	}
	return false
}

// codeSearchForbiddenRoots 过宽的根目录（精确匹配）：检索是给「找项目里的符号」用的
var codeSearchForbiddenRoots = []string{
	"/", "/etc", "/system", "/library", "/usr", "/bin", "/sbin", "/var",
	"/private", "/dev", "/applications", "/volumes", "/opt", "/tmp",
}

// checkCodeSearchRoot 校验检索根：过宽（系统目录、一级目录、整个家目录）则拒绝
func checkCodeSearchRoot(abs string) error {
	lower := strings.ToLower(strings.TrimRight(abs, "/"))
	if lower == "" {
		lower = "/"
	}
	for _, f := range codeSearchForbiddenRoots {
		if lower == f {
			return fmt.Errorf("拒绝在过宽的目录上检索（%s）：请指定具体项目目录", abs)
		}
	}
	if home := agentHome(); home != "" && lower == strings.ToLower(strings.TrimRight(home, "/")) {
		return fmt.Errorf("拒绝在整个家目录上检索（%s）：请指定具体项目目录", abs)
	}
	// 一级目录（/Users、/Volumes/X 之外的 /xxx）都不是项目目录
	if segs := strings.Split(strings.Trim(strings.TrimRight(abs, "/"), "/"), "/"); len(segs) < 2 {
		return fmt.Errorf("拒绝在过宽的目录上检索（%s）：请指定具体项目目录", abs)
	}
	return nil
}

const (
	// codeSearchMaxFileSize 单文件上限（超过视为生成物/数据文件，跳过）
	codeSearchMaxFileSize = 1 << 20
	// codeSearchMaxFiles 单次检索最多扫描的文件数
	codeSearchMaxFiles = 20000
	// codeSearchBudget 单次检索时长上限
	codeSearchBudget = 5 * time.Second
)

// SearchProjectCode 在 root 目录内按关键词检索代码（大小写不敏感子串匹配）。
// 跳过依赖/构建/敏感目录与二进制文件；二进制判定用「前 8KB 是否含 NUL」，
// 不做扩展名白名单（避免漏掉 Dockerfile / Makefile 这类无扩展名文件）。
func SearchProjectCode(root, query string, limit int) (*CodeSearchResult, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, fmt.Errorf("检索关键词为空")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("目录不存在或不是目录: %s", abs)
	}
	if err := checkCodeSearchRoot(abs); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	res := &CodeSearchResult{Query: query, Root: abs, Hits: []CodeSearchHit{}}
	matched := map[string]struct{}{}
	deadline := time.Now().Add(codeSearchBudget)

	_ = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != abs && (codeSearchSkipDirs[d.Name()] || codeSearchSensitiveDir(path)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if res.FilesScanned >= codeSearchMaxFiles || time.Now().After(deadline) {
			res.StopReason = CodeSearchStopBudget
			return filepath.SkipAll
		}
		if info, statErr := d.Info(); statErr == nil && info.Size() > codeSearchMaxFileSize {
			res.FilesSkipped++
			return nil
		}
		rel, relErr := filepath.Rel(abs, path)
		if relErr != nil {
			rel = path
		}
		fileHits, ok := searchFileLines(path, rel, q, limit-len(res.Hits))
		if !ok {
			res.FilesSkipped++ // 二进制/不可读
			return nil
		}
		res.FilesScanned++
		for _, h := range fileHits {
			if len(res.Hits) >= limit {
				res.StopReason = CodeSearchStopHits
				return filepath.SkipAll
			}
			res.Hits = append(res.Hits, h)
			matched[h.Path] = struct{}{}
		}
		return nil
	})
	res.MatchedFiles = len(matched)
	// 命中数填满上限即视为截断：后续文件里可能仍有匹配
	if res.StopReason == "" && len(res.Hits) >= limit {
		res.StopReason = CodeSearchStopHits
	}
	sort.SliceStable(res.Hits, func(i, j int) bool {
		if res.Hits[i].Path != res.Hits[j].Path {
			return res.Hits[i].Path < res.Hits[j].Path
		}
		return res.Hits[i].Line < res.Hits[j].Line
	})
	return res, nil
}

// searchFileLines 单文件逐行匹配；返回 ok=false 表示按二进制/不可读跳过
func searchFileLines(path, rel, query string, remaining int) ([]CodeSearchHit, bool) {
	if remaining <= 0 {
		return nil, true
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	head := make([]byte, 8192)
	n, _ := f.Read(head)
	if n > 0 && strings.IndexByte(string(head[:n]), 0) >= 0 {
		return nil, false // 含 NUL：按二进制跳过
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, false
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var hits []CodeSearchHit
	lineNo := 0
	for sc.Scan() {
		lineNo++
		text := sc.Text()
		if strings.Contains(strings.ToLower(text), query) {
			hits = append(hits, CodeSearchHit{Path: rel, Line: lineNo, Snippet: trimSnippet(text)})
			if len(hits) >= remaining {
				break
			}
		}
	}
	return hits, true
}

// trimSnippet 命中行压缩为单行短片段（最长 200 字符）
func trimSnippet(line string) string {
	s := strings.Join(strings.Fields(line), " ")
	r := []rune(s)
	if len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return s
}
