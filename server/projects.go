package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// 项目 = Root 下的一个目录；它的 Claude 会话在 <ClaudeDir>/projects/<mangle(路径)>/*.jsonl，
// Claude 自建 worktree 里的会话在 <mangle(路径)>--claude-worktrees-<名字>/ 下。
// 和 rcsync 同一套推导（见 bin/rcsync 的 mangle）。
//
// codex / grok 的会话不按项目分目录（见 codex.go / grok.go），按会话记录的 cwd 归到项目里：
// 在项目子目录里开的也算这个项目，嵌套的项目归里面那个。

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,100}$`)

// Claude Code 把项目路径里非字母数字的字符一律换成 -
func mangle(p string) string {
	b := []byte(p)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '-'
		}
	}
	return string(b)
}

const worktreeInfix = "--claude-worktrees-"

type Project struct {
	Name       string         `json:"name"` // 相对 Root，界面上显示、接口里当 key
	Path       string         `json:"path"`
	Exists     bool           `json:"exists"`
	Sessions   int            `json:"sessions"` // 没归档的
	Archived   int            `json:"archived"`
	Agents     map[string]int `json:"agents"`     // 没归档的会话按 CLI 分：claude / codex / grok
	LastActive int64          `json:"lastActive"` // unix 毫秒
	Discovered bool           `json:"discovered,omitempty"`
}

type SessionInfo struct {
	ID       string `json:"id"`
	Agent    string `json:"agent"`
	Title    string `json:"title"`
	Cwd      string `json:"cwd"`
	Worktree string `json:"worktree,omitempty"`
	Mtime    int64  `json:"mtime"`
	Size     int64  `json:"size"`
	Archived bool   `json:"archived,omitempty"`
	ChatID   string `json:"chatId,omitempty"`
	path     string // claude / codex：会话文件；grok：会话目录
}

type sessDir struct {
	dir      string
	worktree string
}

type metaCache struct {
	mu sync.Mutex
	m  map[string]cachedMeta
}

type cachedMeta struct {
	mtime, size int64
	title, cwd  string
}

func (s *Server) projectsDir() string { return filepath.Join(s.cfg.ClaudeDir, "projects") }

type projRef struct {
	name, path string
	discovered bool
}

// projectList：配置里列的 + 从会话记录里发现的、位于 Root 下的项目。只要名字和路径，不做统计 ——
// 每个接口都要拿它校验项目名，得便宜
func (s *Server) projectList() []projRef {
	seen := map[string]bool{}
	var out []projRef
	add := func(name string, discovered bool) {
		if !seen[name] {
			seen[name] = true
			out = append(out, projRef{name: name, path: filepath.Join(s.cfg.Root, name), discovered: discovered})
		}
	}
	for _, name := range s.cfg.Projects {
		add(name, false)
	}
	for _, name := range s.discover() {
		add(name, true)
	}
	// codex / grok：在已知项目子目录里开的会话就归那个项目，不另立门户；浅的先收，深的就落进它里面了
	set := map[string]bool{}
	for _, f := range s.codexAll() {
		set[f.cwd] = true
	}
	for _, g := range s.grokAll() {
		set[g.cwd] = true
	}
	cwds := make([]string, 0, len(set))
	for c := range set {
		cwds = append(cwds, c)
	}
	sort.Slice(cwds, func(i, j int) bool {
		return len(cwds[i]) < len(cwds[j]) || len(cwds[i]) == len(cwds[j]) && cwds[i] < cwds[j]
	})
	for _, cwd := range cwds {
		rel, err := filepath.Rel(s.cfg.Root, cwd)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") || ownedBy(cwd, refPaths(out)) != "" || !isDir(cwd) {
			continue
		}
		add(rel, true)
	}
	return out
}

func refPaths(refs []projRef) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.path
	}
	return out
}

// ownedBy：cwd 归哪个项目（取最深的那个），都不是返回空串
func ownedBy(cwd string, paths []string) string {
	best := ""
	for _, p := range paths {
		if (cwd == p || strings.HasPrefix(cwd, p+"/")) && len(p) > len(best) {
			best = p
		}
	}
	return best
}

// projects：项目列表带统计（会话数、最近活动），给首页和侧栏
func (s *Server) projects() []Project {
	refs := s.projectList()
	out := make([]Project, len(refs))
	byPath := map[string]*Project{}
	for i, r := range refs {
		p := Project{Name: r.name, Path: r.path, Discovered: r.discovered, Agents: map[string]int{}}
		p.Exists = isDir(p.Path)
		for _, d := range s.sessionDirs(p.Path) {
			entries, _ := os.ReadDir(d.dir)
			for _, e := range entries {
				id, ok := strings.CutSuffix(e.Name(), ".jsonl")
				if !ok {
					continue
				}
				if s.archive.has(id) {
					p.Archived++
					continue
				}
				p.Sessions++
				p.Agents[agentClaude]++
				if fi, err := e.Info(); err == nil && fi.ModTime().UnixMilli() > p.LastActive {
					p.LastActive = fi.ModTime().UnixMilli()
				}
			}
		}
		out[i] = p
		byPath[p.Path] = &out[i]
	}
	paths := refPaths(refs)
	count := func(agent, id, cwd string, mtime int64) {
		p := byPath[ownedBy(cwd, paths)]
		switch {
		case p == nil:
		case s.archive.has(id):
			p.Archived++
		default:
			p.Sessions++
			p.Agents[agent]++
			p.LastActive = max(p.LastActive, mtime)
		}
	}
	for _, f := range s.codexAll() {
		count(agentCodex, f.id, f.cwd, f.mtime)
	}
	for _, g := range s.grokAll() {
		count(agentGrok, g.id, g.cwd, g.mtime)
	}
	return out
}

// discover 看 <ClaudeDir>/projects 里以 mangle(Root) 开头的目录，取最新会话里记录的 cwd
// 反推项目名。目录名本身反推不回去（_ 和 / 都被编成了 -），必须读文件里的 cwd。
func (s *Server) discover() []string {
	prefix := mangle(s.cfg.Root) + "-"
	entries, _ := os.ReadDir(s.projectsDir())
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, prefix) || strings.Contains(name, worktreeInfix) {
			continue
		}
		dir := filepath.Join(s.projectsDir(), name)
		var files []os.FileInfo
		entries, _ := os.ReadDir(dir)
		for _, f := range entries {
			if fi, err := f.Info(); err == nil && strings.HasSuffix(f.Name(), ".jsonl") {
				files = append(files, fi)
			}
		}
		sort.Slice(files, func(i, j int) bool { return files[i].ModTime().After(files[j].ModTime()) })
		// rcsync 会把另一台机器的会话同步进来，它们记的 cwd 是那台机器的路径；
		// 往前多看几份，找一份 cwd 编码后正好是这个目录名的
		for _, fi := range files[:min(len(files), 8)] {
			cwd := s.meta(filepath.Join(dir, fi.Name()), fi).cwd
			rel, err := filepath.Rel(s.cfg.Root, cwd)
			if err != nil || rel == "." || strings.HasPrefix(rel, "..") || mangle(cwd) != name {
				continue
			}
			if isDir(cwd) {
				out = append(out, rel)
			}
			break
		}
	}
	sort.Strings(out)
	return out
}

// projectPath 只认列表里有的项目名 —— 接口参数不能变成任意路径
func (s *Server) projectPath(name string) (string, bool) {
	for _, p := range s.projectList() {
		if p.name == name {
			return p.path, true
		}
	}
	return "", false
}

func (s *Server) sessionDirs(projectPath string) []sessDir {
	base := mangle(projectPath)
	entries, _ := os.ReadDir(s.projectsDir())
	var out []sessDir
	for _, e := range entries {
		switch name := e.Name(); {
		case !e.IsDir():
		case name == base:
			out = append(out, sessDir{dir: filepath.Join(s.projectsDir(), name)})
		case strings.HasPrefix(name, base+worktreeInfix):
			out = append(out, sessDir{dir: filepath.Join(s.projectsDir(), name), worktree: strings.TrimPrefix(name, base+worktreeInfix)})
		}
	}
	return out
}

// listSessions：这个项目下三种 CLI 的会话，最近活动的在前。
// codex 的标题要读文件才知道，这里先空着，由 withTitles 只给真要显示的那几条补上
func (s *Server) listSessions(projectPath string) []SessionInfo {
	out := s.claudeSessions(projectPath)
	paths := refPaths(s.projectList())
	for _, f := range s.codexAll() {
		if ownedBy(f.cwd, paths) == projectPath {
			out = append(out, s.codexInfo(f))
		}
	}
	for _, g := range s.grokAll() {
		if ownedBy(g.cwd, paths) == projectPath {
			out = append(out, s.grokInfo(g))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mtime > out[j].Mtime })
	return out
}

func (s *Server) withTitles(list []SessionInfo) {
	for i := range list {
		if list[i].Agent == agentCodex && list[i].Title == "" {
			list[i].Title = s.codexTitle(list[i].path)
		}
	}
}

func (s *Server) claudeSessions(projectPath string) []SessionInfo {
	var out []SessionInfo
	for _, d := range s.sessionDirs(projectPath) {
		entries, _ := os.ReadDir(d.dir)
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), ".jsonl")
			if !ok || !sessionIDRe.MatchString(id) {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			path := filepath.Join(d.dir, e.Name())
			m := s.meta(path, fi)
			out = append(out, SessionInfo{
				ID: id, Agent: agentClaude, Title: m.title, Cwd: m.cwd, Worktree: d.worktree,
				Mtime: fi.ModTime().UnixMilli(), Size: fi.Size(), Archived: s.archive.has(id), path: path,
			})
		}
	}
	return out
}

// findSession 在项目里找某个 CLI 的某个会话（agent 为空当 claude）
func (s *Server) findSession(projectPath, agent, id string) (SessionInfo, bool) {
	if !sessionIDRe.MatchString(id) {
		return SessionInfo{}, false
	}
	switch normAgent(agent) {
	case agentClaude:
		for _, d := range s.sessionDirs(projectPath) {
			path := filepath.Join(d.dir, id+".jsonl")
			if fi, err := os.Stat(path); err == nil {
				m := s.meta(path, fi)
				return SessionInfo{ID: id, Agent: agentClaude, Title: m.title, Cwd: m.cwd, Worktree: d.worktree,
					Mtime: fi.ModTime().UnixMilli(), Size: fi.Size(), Archived: s.archive.has(id), path: path}, true
			}
		}
	case agentCodex:
		for _, f := range s.codexAll() {
			if f.id == id && ownedBy(f.cwd, refPaths(s.projectList())) == projectPath {
				info := s.codexInfo(f)
				info.Title = s.codexTitle(f.path)
				return info, true
			}
		}
	case agentGrok:
		for _, g := range s.grokAll() {
			if g.id == id && ownedBy(g.cwd, refPaths(s.projectList())) == projectPath {
				return s.grokInfo(g), true
			}
		}
	}
	return SessionInfo{}, false
}

// sessionEntries：会话记录转成页面用的 Entry，太长的只留最后 limit 条
func sessionEntries(info SessionInfo, limit int) ([]Entry, bool, error) {
	switch info.Agent {
	case agentCodex:
		return readCodexEntries(info.path, limit)
	case agentGrok:
		return readGrokEntries(info.path, limit)
	}
	return readEntries(info.path, limit)
}

// meta 取会话的标题和 cwd，按 (mtime, size) 缓存。
// 标题优先级：/rename 的 custom-title > Claude 自动起的 ai-title > summary > 第一句提问。
// 前两种追加在文件末尾，所以读头（提问、cwd）和读尾（标题）各一小段就够，不整文件解析。
func (s *Server) meta(path string, fi os.FileInfo) cachedMeta {
	s.metas.mu.Lock()
	if c, ok := s.metas.m[path]; ok && c.mtime == fi.ModTime().UnixNano() && c.size == fi.Size() {
		s.metas.mu.Unlock()
		return c
	}
	s.metas.mu.Unlock()

	c := cachedMeta{mtime: fi.ModTime().UnixNano(), size: fi.Size()}
	f, err := os.Open(path)
	if err != nil {
		return c
	}
	defer f.Close()

	prompt := ""
	read := 0
	_ = eachLine(f, 1<<20, func(line []byte) bool {
		read += len(line)
		var r record
		if json.Unmarshal(line, &r) == nil {
			if c.cwd == "" && r.Cwd != "" {
				c.cwd = r.Cwd
			}
			if prompt == "" {
				prompt = promptTitle(&r)
			}
		}
		return (c.cwd == "" || prompt == "") && read < 4<<20
	})

	const tail = 256 << 10
	var custom, ai, summary string
	off := max(fi.Size()-tail, 0)
	if _, err := f.Seek(off, io.SeekStart); err == nil {
		data, _ := io.ReadAll(io.LimitReader(f, tail))
		lines := bytes.Split(data, []byte("\n"))
		if off > 0 && len(lines) > 0 {
			lines = lines[1:] // 第一行多半从中间切开
		}
		for _, line := range lines {
			// customTitle / aiTitle / summary 之外的行不值得反序列化
			if !bytes.Contains(line, []byte(`Title"`)) && !bytes.Contains(line, []byte(`"summary"`)) {
				continue
			}
			var r record
			if json.Unmarshal(line, &r) != nil {
				continue
			}
			switch r.Type {
			case "custom-title":
				custom = r.CustomTitle
			case "ai-title":
				ai = r.AITitle
			case "summary":
				summary = r.Summary
			}
		}
	}
	for _, t := range []string{custom, ai, summary, prompt} {
		if strings.TrimSpace(t) != "" {
			c.title = oneLine(t, 80)
			break
		}
	}

	s.metas.mu.Lock()
	s.metas.m[path] = c
	s.metas.mu.Unlock()
	return c
}

// readEntries 整个会话转成 Entry 列表；太长的只留最后 limit 条
func readEntries(path string, limit int) ([]Entry, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	var out []Entry
	err = eachLine(f, 32<<20, func(line []byte) bool {
		var r record
		if json.Unmarshal(line, &r) == nil {
			if e := entryFromRecord(&r); e != nil {
				out = append(out, *e)
			}
		}
		return true
	})
	truncated := false
	if limit > 0 && len(out) > limit {
		out, truncated = out[len(out)-limit:], true
	}
	return out, truncated, err
}

// ---- HTTP ----

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.projects())
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	path, ok := s.projectPath(r.URL.Query().Get("project"))
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这个项目")
		return
	}
	// filter=active（默认）/ archived / all
	all := s.listSessions(path)
	list := all[:0:0]
	for _, x := range all {
		switch r.URL.Query().Get("filter") {
		case "all":
		case "archived":
			if !x.Archived {
				continue
			}
		default:
			if x.Archived {
				continue
			}
		}
		list = append(list, x)
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n < len(list) {
		list = list[:n]
	}
	s.withTitles(list)
	running := s.chats.BySession()
	for i := range list {
		list[i].ChatID = running[list[i].ID]
	}
	writeJSON(w, list)
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	path, ok := s.projectPath(q.Get("project"))
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这个项目")
		return
	}
	info, ok := s.findSession(path, q.Get("agent"), q.Get("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这个会话")
		return
	}
	info.ChatID = s.chats.BySession()[info.ID]
	s.writeSession(w, r, info) // 带 ETag 和服务端缓存，见 session_cache.go
}
