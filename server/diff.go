package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 改文件的记录：对话里每轮末尾那张「改了哪些文件 +N -M」的卡片，点开是右侧的 diff 面板。
//
// diff 有两个来源：
//   - 这一轮：从改文件的工具调用里算。Claude 的 Edit / Write 结果里带着 structuredPatch（和它 TUI 里显示的
//     同一份 diff，带行号），有就用；没有（codex / grok、结果还没回来）就按参数算：Edit 的 old / new、
//     Write 的全文、codex Patch 的统一 diff。tool_use 上只挂增删行数（卡片用），hunks 展开时按 id 另取
//   - 工作区：git diff HEAD，未提交的全部改动（含没跟踪的新文件），GET /api/diff、/api/diff/file

type FileChange struct {
	Path      string `json:"path"`
	Status    string `json:"status,omitempty"` // add / delete / rename / untracked；空 = 修改
	OldPath   string `json:"oldPath,omitempty"`
	Add       int    `json:"add"`
	Del       int    `json:"del"`
	Binary    bool   `json:"binary,omitempty"`
	Hunks     []Hunk `json:"hunks,omitempty"`
	Truncated bool   `json:"truncated,omitempty"` // 太长，hunks 只留了前面一部分
}

// Hunk 是统一 diff 的一段，Lines 带前缀：' ' 没改、'+' 加的、'-' 删的。
// 按 Edit 参数算出来的不知道在文件第几行，OldStart、NewStart 都是 0
type Hunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

const maxHunkLines = 5000 // 一个文件最多带这么多行 diff

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// lineDiff 逐行对比，首尾相同的先剥掉，中间做 LCS
func lineDiff(a, b []string) []string {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	out := make([]string, 0, len(a)+len(b)-pre-suf)
	for _, l := range a[:pre] {
		out = append(out, " "+l)
	}
	out = append(out, lcsDiff(a[pre:len(a)-suf], b[pre:len(b)-suf])...)
	for _, l := range a[len(a)-suf:] {
		out = append(out, " "+l)
	}
	return out
}

func lcsDiff(a, b []string) []string {
	n, m := len(a), len(b)
	var out []string
	if n*m > 1_000_000 { // 太大就不逐行对齐了：整段删、整段加
		for _, l := range a {
			out = append(out, "-"+l)
		}
		for _, l := range b {
			out = append(out, "+"+l)
		}
		return out
	}
	// dp[i*w+j] = a[i:] 和 b[j:] 的最长公共子序列长度
	w := m + 1
	dp := make([]int32, (n+1)*w)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i*w+j] = dp[(i+1)*w+j+1] + 1
			} else {
				dp[i*w+j] = max(dp[(i+1)*w+j], dp[i*w+j+1])
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, " "+a[i])
			i, j = i+1, j+1
		case dp[(i+1)*w+j] >= dp[i*w+j+1]:
			out = append(out, "-"+a[i])
			i++
		default:
			out = append(out, "+"+b[j])
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, "-"+a[i])
	}
	for ; j < m; j++ {
		out = append(out, "+"+b[j])
	}
	return out
}

func newHunk(oldStart, newStart int, lines []string) Hunk {
	h := Hunk{OldStart: oldStart, NewStart: newStart, Lines: lines}
	for _, l := range lines {
		switch l[0] {
		case '+':
			h.NewLines++
		case '-':
			h.OldLines++
		default:
			h.OldLines++
			h.NewLines++
		}
	}
	return h
}

// 整个文件：新建（或 Write 重写）全算加的，删除全算删的
func wholeFile(path, status, content string) FileChange {
	lines := splitLines(content)
	prefix, oldStart, newStart := "+", 0, 1
	if status == "delete" {
		prefix, oldStart, newStart = "-", 1, 0
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = prefix + l
	}
	return finish(FileChange{Path: path, Status: status, Hunks: []Hunk{newHunk(oldStart, newStart, out)}})
}

// finish 按 hunks 数增删行，再把太长的 hunks 截短（行数照原样算）
func finish(fc FileChange) FileChange {
	fc.Add, fc.Del = 0, 0
	for _, h := range fc.Hunks {
		for _, l := range h.Lines {
			if l == "" {
				continue
			}
			switch l[0] {
			case '+':
				fc.Add++
			case '-':
				fc.Del++
			}
		}
	}
	budget := maxHunkLines
	for i := range fc.Hunks {
		if budget <= 0 {
			fc.Hunks, fc.Truncated = fc.Hunks[:i], true
			break
		}
		if len(fc.Hunks[i].Lines) > budget {
			fc.Hunks[i].Lines, fc.Truncated = fc.Hunks[i].Lines[:budget], true
		}
		budget -= len(fc.Hunks[i].Lines)
	}
	return fc
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// parseUnifiedDiff 把统一 diff 拆成 hunks；没有 @@ 头的（只有 +- 行）当成一段不知道行号的
func parseUnifiedDiff(diff string) []Hunk {
	var hunks []Hunk
	var cur *Hunk
	var loose []string
	for _, line := range splitLines(diff) {
		if m := hunkHeader.FindStringSubmatch(line); m != nil {
			o, _ := strconv.Atoi(m[1])
			n, _ := strconv.Atoi(m[3])
			hunks = append(hunks, Hunk{OldStart: o, NewStart: n})
			cur = &hunks[len(hunks)-1]
			continue
		}
		if line == "" {
			line = " " // 有的工具把空的上下文行连前缀一起吃掉了
		}
		switch line[0] {
		case ' ', '+', '-':
		default:
			continue // diff --git、index、\ No newline at end of file
		}
		if cur == nil {
			if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
				continue
			}
			loose = append(loose, line)
			continue
		}
		cur.Lines = append(cur.Lines, line)
	}
	if len(hunks) == 0 && len(loose) > 0 {
		return []Hunk{newHunk(0, 0, loose)}
	}
	for i := range hunks {
		hunks[i] = newHunk(hunks[i].OldStart, hunks[i].NewStart, hunks[i].Lines)
	}
	return hunks
}

// codex Patch 的一项：新建 / 删除给的是整个文件，修改给的是统一 diff
func patchFileChange(path, kind, diff string) FileChange {
	isDiff := strings.HasPrefix(diff, "@@") || strings.HasPrefix(diff, "--- ") || strings.Contains(diff, "\n@@ ")
	switch {
	case kind == "add" && !isDiff:
		return wholeFile(path, "add", diff)
	case kind == "delete" && !isDiff:
		return wholeFile(path, "delete", diff)
	}
	status := ""
	if kind == "add" || kind == "delete" {
		status = kind
	}
	return finish(FileChange{Path: path, Status: status, Hunks: parseUnifiedDiff(diff)})
}

func patchStats(path, kind, diff string) FileChange {
	return statsOnly([]FileChange{patchFileChange(path, kind, diff)})[0]
}

// fileChanges 按工具参数算改了哪些文件。hunks=false 只要增删行数（挂在 tool_use 上给卡片用）
func fileChanges(name string, raw json.RawMessage, hunks bool) []FileChange {
	switch name {
	case "Edit", "MultiEdit", "Write", "Patch":
	default:
		return nil
	}
	var in struct {
		FilePath  string  `json:"file_path"`
		OldString *string `json:"old_string"`
		NewString string  `json:"new_string"`
		Content   *string `json:"content"`
		Edits     []struct {
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		} `json:"edits"`
		Changes []patchChange `json:"changes"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &in) != nil {
		return nil
	}
	var out []FileChange
	switch name {
	case "Edit":
		if in.FilePath != "" && in.OldString != nil {
			lines := lineDiff(splitLines(*in.OldString), splitLines(in.NewString))
			out = append(out, finish(FileChange{Path: in.FilePath, Hunks: []Hunk{newHunk(0, 0, lines)}}))
		}
	case "MultiEdit":
		fc := FileChange{Path: in.FilePath}
		for _, e := range in.Edits {
			fc.Hunks = append(fc.Hunks, newHunk(0, 0, lineDiff(splitLines(e.OldString), splitLines(e.NewString))))
		}
		if in.FilePath != "" && len(fc.Hunks) > 0 {
			out = append(out, finish(fc))
		}
	case "Write":
		// 光看参数分不出是新建还是重写，状态留空；Claude 的结果里会说清楚（resultChanges）
		if in.FilePath != "" && in.Content != nil {
			out = append(out, wholeFile(in.FilePath, "", *in.Content))
		}
	case "Patch":
		for _, c := range in.Changes {
			out = append(out, patchFileChange(c.Path, c.Kind, c.Diff))
		}
	}
	if !hunks {
		out = statsOnly(out)
	}
	return out
}

// resultChanges 读 Claude 工具结果里的 toolUseResult（stream-json 里叫 tool_use_result）：
// Edit / MultiEdit / Write 带 filePath + structuredPatch，Write 新建时 structuredPatch 是空的、type 是 create
func resultChanges(raw json.RawMessage) []FileChange {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var r struct {
		Type            string `json:"type"`
		FilePath        string `json:"filePath"`
		Content         string `json:"content"`
		StructuredPatch []Hunk `json:"structuredPatch"`
	}
	if json.Unmarshal(raw, &r) != nil || r.FilePath == "" {
		return nil
	}
	switch {
	case r.Type == "create":
		return []FileChange{wholeFile(r.FilePath, "add", r.Content)}
	case len(r.StructuredPatch) > 0:
		return []FileChange{finish(FileChange{Path: r.FilePath, Hunks: r.StructuredPatch})}
	}
	return nil
}

// statsOnly 拿掉 hunks，只留路径和增删行数（精简版、tool_use 上用）
func statsOnly(fs []FileChange) []FileChange {
	if len(fs) == 0 {
		return fs
	}
	out := make([]FileChange, len(fs))
	for i, f := range fs {
		f.Hunks, f.Truncated = nil, false
		out[i] = f
	}
	return out
}

// ---- 工作区：git diff HEAD ----

type diffSummary struct {
	Root    string       `json:"root,omitempty"` // 仓库根目录，下面的路径都相对它
	Branch  string       `json:"branch,omitempty"`
	Files   []FileChange `json:"files"`
	More    bool         `json:"more,omitempty"`    // 文件太多，只列了前面的
	NotRepo string       `json:"notRepo,omitempty"` // 看不了工作区 diff 的原因（不是 git 仓库之类）
}

const (
	maxDiffFiles  = 300
	maxDiffOutput = 4 << 20
	emptyTree     = "4b825dc642cb6eb9a060e54bf8d69288fbee4904" // 还没有提交时拿它当 HEAD 比
)

func git(ctx context.Context, dir string, args ...string) (string, error) {
	// --literal-pathspecs：路径原样当路径，不认 :(glob) 这类魔法；safe.directory：仓库属主和 rcweb 不是同一个用户也能读
	base := []string{"-C", dir, "--literal-pathspecs", "-c", "core.quotepath=off", "-c", "safe.directory=*"}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Env = childEnv("GIT_OPTIONAL_LOCKS=0", "LC_ALL=C") // 只读，别去抢 index.lock
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &limitWriter{&out, maxDiffOutput}, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", errors.New(msg)
		}
		return "", err
	}
	return out.String(), nil
}

// limitWriter 写满 n 字节后丢掉后面的（照样报成功，免得 git 因为管道断了出错）
type limitWriter struct {
	buf *bytes.Buffer
	n   int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if room := l.n - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// diffRepo 找对话工作目录（或项目目录）所在的 git 仓库。仓库根目录也得在 RCWEB_ROOT 下，
// 不然 RCWEB_ROOT 外面套着的仓库（比如 ~ 是个 dotfiles 仓库）会把外面的文件带出来
func (s *Server) diffRepo(ctx context.Context, r *http.Request) (root, why string) {
	q := r.URL.Query()
	dir := q.Get("cwd")
	if !filepath.IsAbs(dir) {
		dir, _ = s.projectPath(q.Get("project"))
	}
	if dir == "" {
		return "", "不知道在哪个目录"
	}
	if _, err := os.Stat(dir); err != nil {
		return "", "目录不存在：" + dir
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", "VPS 上没装 git"
	}
	if _, ok := s.underRoot(dir); !ok {
		return "", "只能看项目根目录（" + s.cfg.Root + "）下的仓库"
	}
	top, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "不是 git 仓库"
	}
	root, ok := s.underRoot(strings.TrimSpace(top))
	if !ok {
		return "", "仓库根目录不在 " + s.cfg.Root + " 下"
	}
	return root, ""
}

// underRoot 解开符号链接后在 RCWEB_ROOT 下才返回（真实路径）
func (s *Server) underRoot(p string) (string, bool) {
	real, err := filepath.EvalSymlinks(p)
	if err != nil || !underDir(s.cfg.Root, real) {
		return "", false
	}
	return real, true
}

func diffBase(ctx context.Context, root string) string {
	if _, err := git(ctx, root, "rev-parse", "--verify", "-q", "HEAD"); err != nil {
		return emptyTree
	}
	return "HEAD"
}

func gitBranch(ctx context.Context, root string) string {
	if b, err := git(ctx, root, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		return strings.TrimSpace(b)
	}
	b, _ := git(ctx, root, "rev-parse", "--short", "HEAD") // detached HEAD
	return strings.TrimSpace(b)
}

// handleDiff：GET /api/diff?cwd=&project= —— 工作区相对 HEAD 改了哪些文件、各增删几行
func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	root, why := s.diffRepo(ctx, r)
	if why != "" {
		writeJSON(w, diffSummary{Files: []FileChange{}, NotRepo: why})
		return
	}
	base := diffBase(ctx, root)
	status, err := git(ctx, root, "diff", base, "--name-status", "-z", "-M", "--no-ext-diff")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	numstat, _ := git(ctx, root, "diff", base, "--numstat", "-z", "-M", "--no-ext-diff", "--no-textconv")
	untracked, _ := git(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")

	stats := parseNumstat(numstat)
	files := []FileChange{}
	for _, f := range parseNameStatus(status) {
		st := stats[f.Path]
		f.Add, f.Del, f.Binary = st.Add, st.Del, st.Binary
		files = append(files, f)
	}
	for _, p := range strings.Split(untracked, "\x00") {
		if p != "" {
			f := FileChange{Path: p, Status: "untracked"}
			f.Add, f.Binary = countFileLines(filepath.Join(root, p))
			files = append(files, f)
		}
	}
	more := len(files) > maxDiffFiles
	if more {
		files = files[:maxDiffFiles]
	}
	writeJSON(w, diffSummary{Root: root, Branch: gitBranch(ctx, root), Files: files, More: more})
}

// --name-status -z：「M\0路径\0」，改名 / 复制是「R100\0旧\0新\0」
func parseNameStatus(out string) []FileChange {
	toks := strings.Split(out, "\x00")
	var files []FileChange
	for i := 0; i < len(toks); i++ {
		code := toks[i]
		if code == "" || i+1 >= len(toks) {
			continue
		}
		f := FileChange{Path: toks[i+1]}
		i++
		switch code[0] {
		case 'A':
			f.Status = "add"
		case 'D':
			f.Status = "delete"
		case 'R', 'C':
			if i+1 < len(toks) {
				f.OldPath, f.Path = f.Path, toks[i+1]
				i++
			}
			if code[0] == 'R' {
				f.Status = "rename"
			} else {
				f.Status = "add"
			}
		}
		files = append(files, f)
	}
	return files
}

// --numstat -z：「加\t删\t路径\0」，改名是「加\t删\t\0旧\0新\0」，二进制的加删是 -
func parseNumstat(out string) map[string]FileChange {
	stats := map[string]FileChange{}
	toks := strings.Split(out, "\x00")
	for i := 0; i < len(toks); i++ {
		parts := strings.SplitN(toks[i], "\t", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if path == "" && i+2 < len(toks) {
			path = toks[i+2]
			i += 2
		}
		add, errA := strconv.Atoi(parts[0])
		del, errD := strconv.Atoi(parts[1])
		stats[path] = FileChange{Add: add, Del: del, Binary: errA != nil || errD != nil}
	}
	return stats
}

// countFileLines 没跟踪的新文件有几行；太大的不数
func countFileLines(p string) (lines int, binary bool) {
	f, err := os.Open(p)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() || fi.Size() > 8<<20 {
		return 0, false
	}
	br := bufio.NewReaderSize(f, 64<<10)
	head, _ := br.Peek(8192)
	if bytes.IndexByte(head, 0) >= 0 {
		return 0, true
	}
	buf := make([]byte, 64<<10)
	last := byte('\n')
	for {
		n, err := br.Read(buf)
		if n > 0 {
			lines += bytes.Count(buf[:n], []byte{'\n'})
			last = buf[n-1]
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return lines, false
		}
	}
	if last != '\n' {
		lines++ // 最后一行没有换行符
	}
	return lines, false
}

// handleDiffFile：GET /api/diff/file?cwd=&project=&path=&oldPath=&status= —— 一个文件的 diff。
// 上下文给整个文件（-U 很大），没改的长段由前端收起来、点开再看；太大的文件退回 3 行上下文
func (s *Server) handleDiffFile(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	root, why := s.diffRepo(ctx, r)
	if why != "" {
		writeErr(w, http.StatusBadRequest, why)
		return
	}
	q := r.URL.Query()
	rel := filepath.Clean(q.Get("path"))
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		writeErr(w, http.StatusBadRequest, "path 要是仓库里的相对路径")
		return
	}

	if q.Get("status") == "untracked" {
		real, ok := s.underRoot(filepath.Join(root, rel))
		if !ok {
			writeErr(w, http.StatusNotFound, "找不到文件: "+rel)
			return
		}
		data, err := readHead(real, maxFileBytes)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 {
			writeJSON(w, FileChange{Path: rel, Status: "untracked", Binary: true})
			return
		}
		fc := wholeFile(rel, "untracked", string(data))
		writeJSON(w, fc)
		return
	}

	paths := []string{rel}
	if old := filepath.Clean(q.Get("oldPath")); q.Get("oldPath") != "" && !filepath.IsAbs(old) && !strings.HasPrefix(old, "..") {
		paths = []string{old, rel} // 改名要两个路径都在 pathspec 里才认得出来
	}
	base := diffBase(ctx, root)
	diff := func(context string) (string, error) {
		return git(ctx, root, append([]string{"diff", base, "-M", "--no-ext-diff", "--no-textconv", "--no-color", context, "--"}, paths...)...)
	}
	out, err := diff("-U100000")
	if err == nil && len(out) >= maxDiffOutput {
		out, err = diff("-U3")
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	fc := FileChange{Path: rel, Status: q.Get("status")}
	if strings.Contains(out, "\nBinary files ") || strings.HasPrefix(out, "Binary files ") {
		fc.Binary = true
		writeJSON(w, fc)
		return
	}
	fc.Hunks = parseUnifiedDiff(out)
	writeJSON(w, finish(fc))
}

func readHead(p string, n int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, n))
}

// ---- 给按参数算的 hunk 补上行号和上下文 ----

const ctxLines = 3 // 改动上下各带几行没改的，和 git / Claude 的 diff 一样

// anchorTools：Edit 的参数只有改动那一小段，不知道在文件第几行、前后是什么。在现在的文件里找改完的样子，
// 找到了就补上行号和前后各 3 行没改的；找不到（之后又被改过、文件没了）就原样。行号按现在的文件算
func (s *Server) anchorTools(tools map[string]*toolDetail, cwd string) {
	cache := map[string][]string{}
	for _, d := range tools {
		for i := range d.Files {
			if !unanchored(d.Files[i].Hunks) {
				continue
			}
			path := d.Files[i].Path
			lines, ok := cache[path]
			if !ok {
				lines = s.readLines(path, cwd)
				cache[path] = lines
			}
			if lines == nil {
				continue
			}
			// 拷一份再改：Files 可能和缓存里的 Entry 共用底层数组
			d.Files = append([]FileChange(nil), d.Files...)
			f := &d.Files[i]
			f.Hunks = append([]Hunk(nil), f.Hunks...)
			for j := range f.Hunks {
				f.Hunks[j] = anchorHunk(f.Hunks[j], lines)
			}
		}
	}
}

func unanchored(hunks []Hunk) bool {
	for _, h := range hunks {
		if h.OldStart != 0 || h.NewStart != 0 {
			return false
		}
	}
	return len(hunks) > 0
}

// readLines 读 RCWEB_ROOT 下的一个文本文件，按行拆开；读不了、太大、二进制的返回 nil
func (s *Server) readLines(path, cwd string) []string {
	if !filepath.IsAbs(path) {
		if cwd == "" {
			return nil
		}
		path = filepath.Join(cwd, path)
	}
	real, ok := s.underRoot(path)
	if !ok {
		return nil
	}
	if fi, err := os.Stat(real); err != nil || !fi.Mode().IsRegular() || fi.Size() > maxFileBytes {
		return nil
	}
	data, err := os.ReadFile(real)
	if err != nil || bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 {
		return nil
	}
	return splitLines(string(data))
}

func anchorHunk(h Hunk, file []string) Hunk {
	var after []string // 改完之后的样子：没改的 + 加的
	for _, l := range h.Lines {
		if l != "" && l[0] != '-' {
			after = append(after, l[1:])
		}
	}
	at := indexLines(file, after)
	if at < 0 {
		return h // 纯删除（改完什么都不剩）也找不到位置
	}
	end := at + len(after)
	pre, post := min(ctxLines, at), min(ctxLines, len(file)-end)
	lines := make([]string, 0, pre+len(h.Lines)+post)
	for _, l := range file[at-pre : at] {
		lines = append(lines, " "+l)
	}
	lines = append(lines, h.Lines...)
	for _, l := range file[end : end+post] {
		lines = append(lines, " "+l)
	}
	return newHunk(at-pre+1, at-pre+1, lines)
}

func indexLines(file, sub []string) int {
	if len(sub) == 0 || len(sub) > len(file) {
		return -1
	}
outer:
	for i := 0; i+len(sub) <= len(file); i++ {
		for j := range sub {
			if file[i+j] != sub[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}
