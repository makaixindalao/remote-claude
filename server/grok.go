package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// grok 的会话：<GrokDir>/sessions/<URL 编码的 cwd>/<id>/。组目录名解码就是 cwd
// （太长的用 slug+hash，原路径写在组目录下的 .cwd 里）。标题在 summary.json，
// 对话在 updates.jsonl —— 和 ACP 实时推送的 session/update 是同一种格式，
// 所以历史回看和网页对话共用下面的 acpBuilder。

type grokIndex struct {
	mu    sync.Mutex
	metas map[string]grokMeta // 会话目录 → summary.json 的内容，按 mtime 缓存

	listMu sync.Mutex
	list   []grokSession
	at     time.Time // 一次刷新里的多次调用共用一份扫描结果
}

type grokMeta struct {
	mtime     time.Time
	title     string
	cwd       string
	subagent  bool
	hasPrompt bool // 没发过消息的空会话（开了网页对话没说话就关了）不列出来
}

type grokSession struct {
	id, dir, cwd, title string
	mtime, size         int64
}

func (s *Server) grokSessionsDir() string { return filepath.Join(s.cfg.GrokDir, "sessions") }

// grokAll：Root 下的全部 grok 会话（子代理、空会话除外）
func (s *Server) grokAll() []grokSession {
	ix := &s.grok
	ix.listMu.Lock()
	defer ix.listMu.Unlock()
	if time.Since(ix.at) >= scanTTL {
		ix.list, ix.at = s.scanGrok(), time.Now()
	}
	return ix.list
}

func (s *Server) scanGrok() []grokSession {
	root := s.grokSessionsDir()
	groups, _ := os.ReadDir(root)
	var out []grokSession
	for _, g := range groups {
		if !g.IsDir() {
			continue
		}
		gdir := filepath.Join(root, g.Name())
		cwd := grokGroupCwd(gdir, g.Name())
		if cwd == "" || (cwd != s.cfg.Root && !strings.HasPrefix(cwd, s.cfg.Root+"/")) {
			continue
		}
		sessions, _ := os.ReadDir(gdir)
		for _, e := range sessions {
			if !e.IsDir() || !sessionIDRe.MatchString(e.Name()) {
				continue
			}
			dir := filepath.Join(gdir, e.Name())
			fi, err := os.Stat(filepath.Join(dir, "updates.jsonl"))
			if err != nil {
				continue
			}
			m := s.grokMeta(dir)
			if m.subagent || !m.hasPrompt {
				continue
			}
			if m.cwd != "" {
				cwd = m.cwd
			}
			out = append(out, grokSession{id: e.Name(), dir: dir, cwd: cwd, title: m.title,
				mtime: fi.ModTime().UnixMilli(), size: fi.Size()})
		}
	}
	return out
}

func grokGroupCwd(gdir, name string) string {
	if strings.HasPrefix(name, "%2F") {
		if p, err := url.PathUnescape(name); err == nil {
			return filepath.Clean(p)
		}
	}
	if b, err := os.ReadFile(filepath.Join(gdir, ".cwd")); err == nil {
		return filepath.Clean(strings.TrimSpace(string(b)))
	}
	return ""
}

func (s *Server) grokMeta(dir string) grokMeta {
	path := filepath.Join(dir, "summary.json")
	fi, err := os.Stat(path)
	if err != nil {
		return grokMeta{}
	}
	ix := &s.grok
	ix.mu.Lock()
	if ix.metas == nil {
		ix.metas = map[string]grokMeta{}
	}
	if m, ok := ix.metas[dir]; ok && m.mtime.Equal(fi.ModTime()) {
		ix.mu.Unlock()
		return m
	}
	ix.mu.Unlock()

	m := grokMeta{mtime: fi.ModTime()}
	var sum struct {
		Info struct {
			Cwd string `json:"cwd"`
		} `json:"info"`
		GeneratedTitle string `json:"generated_title"`
		Summary        string `json:"session_summary"`
		Kind           string `json:"session_kind"`
	}
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &sum) == nil {
		m.cwd, m.subagent = sum.Info.Cwd, sum.Kind == "subagent"
		for _, t := range []string{sum.GeneratedTitle, sum.Summary} {
			if strings.TrimSpace(t) != "" {
				m.title = oneLine(t, 80)
				break
			}
		}
	}
	// 自动标题要等第一轮跑完才有；在那之前用第一句提问，顺便判断是不是空会话
	prompt, ok := grokFirstPrompt(filepath.Join(dir, "updates.jsonl"))
	m.hasPrompt = ok
	if m.title == "" {
		m.title = prompt
	}
	ix.mu.Lock()
	ix.metas[dir] = m
	ix.mu.Unlock()
	return m
}

// grokFirstPrompt：第一句提问（可能不适合当标题，比如以 < 开头，这时返回空串但 found 为真）
func grokFirstPrompt(path string) (prompt string, found bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	read := 0
	_ = eachLine(f, 4<<20, func(line []byte) bool {
		read += len(line)
		if strings.Contains(string(line[:min(len(line), 400)]), `"user_message_chunk"`) {
			var l struct {
				Params struct {
					Update acpUpdate `json:"update"`
				} `json:"params"`
			}
			if json.Unmarshal(line, &l) == nil {
				prompt, found = titleText(l.Params.Update.chunkText()), true
			}
		}
		return !found && read < 8<<20
	})
	return prompt, found
}

func (s *Server) grokInfo(g grokSession) SessionInfo {
	return SessionInfo{ID: g.id, Agent: agentGrok, Title: g.title, Cwd: g.cwd,
		Mtime: g.mtime, Size: g.size, Archived: s.archive.has(g.id), path: g.dir}
}

// readGrokEntries：updates.jsonl → Entry；太长的只留最后 limit 条
func readGrokEntries(dir string, limit int) ([]Entry, bool, error) {
	f, err := os.Open(filepath.Join(dir, "updates.jsonl"))
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	var out []Entry
	b := newACPBuilder(func(e Entry) { out = append(out, e) }, nil)
	err = eachLine(f, 32<<20, func(line []byte) bool {
		var l struct {
			Timestamp int64  `json:"timestamp"`
			Method    string `json:"method"`
			Params    struct {
				Update json.RawMessage `json:"update"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &l) != nil {
			return true
		}
		ts := ""
		if l.Timestamp > 0 {
			ts = time.Unix(l.Timestamp, 0).UTC().Format(time.RFC3339)
		}
		switch l.Method {
		case "session/update":
			b.update(l.Params.Update, ts)
		case "_x.ai/session/update":
			b.extension(l.Params.Update, ts)
		}
		return true
	})
	b.flush()
	truncated := false
	if limit > 0 && len(out) > limit {
		out, truncated = out[len(out)-limit:], true
	}
	return out, truncated, err
}

// ---- ACP 更新 → Entry ----

type acpUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content"` // 碎片：{type,text}；工具更新：[{type:"content"|"diff", …}]
	ToolCallID    string          `json:"toolCallId"`
	Title         string          `json:"title"`
	Status        string          `json:"status"`
	RawInput      json.RawMessage `json:"rawInput"`
	RawOutput     json.RawMessage `json:"rawOutput"`
	Meta          struct {
		Tool struct {
			Name string `json:"name"`
		} `json:"x.ai/tool"`
	} `json:"_meta"`
}

func (u *acpUpdate) chunkText() string {
	var c struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(u.Content, &c)
	return c.Text
}

// acpBuilder 把 ACP 的碎片更新（一段话拆成几十个 chunk）攒成整条 Entry：同类碎片连着来就接上，
// 换了类型（开始调工具、从思考转到正文）就把攒好的那段作为一条 Entry 发出去。
// 实时对话时 onDelta 拿到正文碎片，推给页面做流式显示。
type acpBuilder struct {
	emit    func(Entry)
	onDelta func(string)
	kind    string // 正在攒的：user / text / thinking
	buf     strings.Builder
	ts      string
	tools   map[string]string // toolCallId → 显示用的工具名
	results map[string]bool   // 已经出过结果的工具
}

func newACPBuilder(emit func(Entry), onDelta func(string)) *acpBuilder {
	return &acpBuilder{emit: emit, onDelta: onDelta, tools: map[string]string{}, results: map[string]bool{}}
}

func (b *acpBuilder) flush() {
	if b.kind == "" {
		return
	}
	text := b.buf.String()
	role, t := "assistant", b.kind
	if b.kind == "user" {
		role, t = "user", "text"
	}
	if strings.TrimSpace(text) != "" {
		b.emit(Entry{Role: role, Blocks: []Block{{T: t, Text: clip(text, maxTextLen)}}, TS: b.ts})
	}
	b.kind = ""
	b.buf.Reset()
}

func (b *acpBuilder) chunk(kind, text, ts string) {
	if b.kind != kind {
		b.flush()
		b.kind, b.ts = kind, ts
	}
	b.buf.WriteString(text)
}

// update 处理一条 session/update，返回页面上该显示的活动状态（thinking / text / tool:<名字>），不变返回 ""
func (b *acpBuilder) update(raw json.RawMessage, ts string) string {
	var u acpUpdate
	if json.Unmarshal(raw, &u) != nil {
		return ""
	}
	switch u.SessionUpdate {
	case "user_message_chunk":
		b.chunk("user", u.chunkText(), ts)
	case "agent_message_chunk":
		text := u.chunkText()
		b.chunk("text", text, ts)
		if b.onDelta != nil && text != "" {
			b.onDelta(text)
		}
		return "text"
	case "agent_thought_chunk":
		b.chunk("thinking", u.chunkText(), ts)
		return "thinking"
	case "tool_call":
		if _, ok := b.tools[u.ToolCallID]; ok {
			return ""
		}
		b.flush()
		return "tool:" + b.toolUse(&u, ts)
	case "tool_call_update":
		if (u.Status != "completed" && u.Status != "failed") || b.results[u.ToolCallID] {
			return ""
		}
		b.flush()
		if _, ok := b.tools[u.ToolCallID]; !ok {
			b.toolUse(&u, ts)
		}
		b.results[u.ToolCallID] = true
		b.emit(Entry{Role: "user", TS: ts, Blocks: []Block{{
			T: "tool_result", ID: u.ToolCallID, Text: clip(acpResultText(&u), maxResultLen), IsError: u.Status == "failed",
		}}})
		return "thinking"
	}
	return ""
}

func (b *acpBuilder) toolUse(u *acpUpdate, ts string) string {
	name := u.Meta.Tool.Name
	if name == "" {
		name = u.Title
	}
	name, input := grokTool(name, u.RawInput)
	b.tools[u.ToolCallID] = name
	b.emit(Entry{Role: "assistant", TS: ts, Blocks: []Block{{T: "tool_use", ID: u.ToolCallID, Name: name, Input: input, Files: fileChanges(name, u.RawInput, false)}}})
	return name
}

// extension 处理 grok 自己的 _x.ai/session/update（压缩、重试之类），只挑值得在对话里留一笔的
func (b *acpBuilder) extension(raw json.RawMessage, ts string) {
	var u struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if json.Unmarshal(raw, &u) == nil && u.SessionUpdate == "auto_compact_completed" {
		b.flush()
		b.emit(noteEntry("上下文已压缩", ts))
	}
}

// acpResultText：工具结果的文字。content 里的文字优先；改文件的 diff 已经在参数视图里显示了，这里只说一句
func acpResultText(u *acpUpdate) string {
	var items []struct {
		Type    string `json:"type"`
		Path    string `json:"path"`
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(u.Content, &items)
	var texts, paths []string
	for _, it := range items {
		switch {
		case it.Type == "content" && it.Content.Type == "text" && it.Content.Text != "":
			texts = append(texts, it.Content.Text)
		case it.Type == "content" && it.Content.Type == "image":
			texts = append(texts, "[图片]")
		case it.Type == "diff":
			paths = append(paths, it.Path)
		}
	}
	var out struct {
		OutputForPrompt string   `json:"output_for_prompt"`
		Text            string   `json:"text"`
		Error           string   `json:"error"`
		Message         string   `json:"message"`
		Action          struct { // 服务端联网搜索：搜的词和结果链接都在这
			Query   string `json:"query"`
			Sources []struct {
				URL string `json:"url"`
			} `json:"sources"`
		} `json:"action"`
	}
	_ = json.Unmarshal(u.RawOutput, &out)
	switch {
	case len(texts) > 0:
		return strings.Join(texts, "\n")
	case len(paths) > 0:
		return "已修改 " + strings.Join(paths, "、")
	case out.Action.Query != "":
		lines := []string{"搜索：" + out.Action.Query}
		for _, src := range out.Action.Sources {
			lines = append(lines, src.URL)
		}
		return strings.Join(lines, "\n")
	case out.OutputForPrompt != "":
		return out.OutputForPrompt
	case out.Text != "":
		return out.Text
	case out.Error != "":
		return out.Error
	case out.Message != "":
		return out.Message
	}
	return ""
}

// grokTool 把 grok 的工具名和参数换成 Claude 的（Bash / Read / Edit …），前端按名字挑渲染方式。
// 认不出的原样保留
func grokTool(name string, raw json.RawMessage) (string, json.RawMessage) {
	if strings.HasPrefix(name, "Web search") { // 服务端联网搜索没有工具名，只有这个标题
		name = "web_search"
	}
	var renames [][2]string // grok 的参数名 → Claude 的
	switch name {
	case "web_search":
		name = "WebSearch"
	case "run_terminal_command", "run_terminal_cmd":
		name = "Bash"
	case "read_file":
		name, renames = "Read", [][2]string{{"target_file", "file_path"}}
	case "search_replace":
		name = "Edit"
	case "write":
		name = "Write"
	case "grep":
		name = "Grep"
	case "list_dir":
		name, renames = "LS", [][2]string{{"target_directory", "path"}}
	case "web_fetch":
		name = "WebFetch"
	case "todo_write":
		name = "TodoWrite"
	case "spawn_subagent":
		name = "Task"
	case "ask_user_question":
		name = "AskUserQuestion"
	}
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil || in == nil {
		return name, clipInput(raw)
	}
	delete(in, "variant") // tool_call_update 里的内部标签
	for _, r := range renames {
		if v, ok := in[r[0]]; ok {
			delete(in, r[0])
			in[r[1]] = v
		}
	}
	return name, clipInput(mustJSON(in))
}
