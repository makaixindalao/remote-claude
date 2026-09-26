package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// codex 的会话：<CodexDir>/sessions/YYYY/MM/DD/rollout-<时间>-<id>.jsonl，不按项目分目录，
// 属于哪个项目要看文件第一行 session_meta 里的 cwd。所以这里维护一份全量索引：
// 一个文件的 id / cwd 写下就不会变，按路径缓存，每次只 stat 一遍，新文件才读第一行。
// 标题优先用 codex 自己起的线程名（session_index.jsonl），没有再用第一句提问。

type codexIndex struct {
	mu     sync.Mutex
	files  map[string]*codexFile // 路径 → 元信息
	list   []*codexFile          // 上次扫描的结果（去掉了子代理线程）
	at     time.Time             // 上次扫描时间：一次刷新里的多次调用共用一份
	names  map[string]string     // 线程 id → 线程名
	namesM time.Time
}

type codexFile struct {
	path, id, cwd string
	skip          bool // 子代理线程、读不出 session_meta 的：不列出来
	mtime, size   int64
	prompt        string // 第一句提问
	promptAt      int64  // 读 prompt 时的文件大小：还没找到的话，文件变大了再找
}

var codexRolloutRe = regexp.MustCompile(`^rollout-.*-([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\.jsonl$`)

// 索引扫描结果留多久：一次页面刷新会连着调好几个接口，没必要每次都重新 stat 一遍
const scanTTL = 2 * time.Second

func (s *Server) codexSessionsDir() string { return filepath.Join(s.cfg.CodexDir, "sessions") }

// codexAll：全部（非子代理的）codex 会话。返回副本，调用方拿去用不用加锁
func (s *Server) codexAll() []codexFile {
	ix := &s.codex
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if time.Since(ix.at) >= scanTTL {
		s.scanCodexLocked()
	}
	out := make([]codexFile, len(ix.list))
	for i, f := range ix.list {
		out[i] = *f
	}
	return out
}

func (s *Server) scanCodexLocked() {
	ix := &s.codex
	if ix.files == nil {
		ix.files = map[string]*codexFile{}
	}
	seen := map[string]bool{}
	var list []*codexFile
	root := s.codexSessionsDir()
	// 年 / 月 / 日三层目录，按名字倒序走（新的在前，列表排序反正按 mtime 另排）
	for _, y := range readDirNames(root) {
		for _, m := range readDirNames(filepath.Join(root, y)) {
			for _, d := range readDirNames(filepath.Join(root, y, m)) {
				dir := filepath.Join(root, y, m, d)
				entries, _ := os.ReadDir(dir)
				for _, e := range entries {
					mm := codexRolloutRe.FindStringSubmatch(e.Name())
					if mm == nil {
						continue
					}
					fi, err := e.Info()
					if err != nil {
						continue
					}
					path := filepath.Join(dir, e.Name())
					seen[path] = true
					f := ix.files[path]
					if f == nil {
						f = &codexFile{path: path, id: strings.ToLower(mm[1])}
						readCodexMeta(f)
						ix.files[path] = f
					}
					f.mtime, f.size = fi.ModTime().UnixMilli(), fi.Size()
					if !f.skip {
						list = append(list, f)
					}
				}
			}
		}
	}
	for p := range ix.files {
		if !seen[p] {
			delete(ix.files, p)
		}
	}
	ix.list, ix.at = list, time.Now()
}

func readDirNames(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// readCodexMeta 读第一行 session_meta：cwd、线程 id、是不是子代理
func readCodexMeta(f *codexFile) {
	fh, err := os.Open(f.path)
	if err != nil {
		f.skip = true
		return
	}
	defer fh.Close()
	f.skip = true
	_ = eachLine(fh, 4<<20, func(line []byte) bool {
		var l struct {
			Type    string `json:"type"`
			Payload struct {
				ID     string          `json:"id"`
				Cwd    string          `json:"cwd"`
				Source json.RawMessage `json:"source"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &l) != nil || l.Type != "session_meta" {
			return false
		}
		if l.Payload.ID != "" {
			f.id = l.Payload.ID
		}
		f.cwd = l.Payload.Cwd
		// source 是字符串（cli / exec / vscode …）或者 {"subagent": …}：子代理线程是主线程的内部过程，不单列
		f.skip = f.cwd == "" || bytes.Contains(l.Payload.Source, []byte(`"subagent"`))
		return false
	})
}

// codexTitle：线程名 > 第一句提问
func (s *Server) codexTitle(path string) string {
	ix := &s.codex
	ix.mu.Lock()
	defer ix.mu.Unlock()
	f := ix.files[path]
	if f == nil {
		return ""
	}
	s.loadCodexNamesLocked()
	if n := strings.TrimSpace(ix.names[f.id]); n != "" {
		return oneLine(n, 80)
	}
	if f.prompt == "" && f.promptAt != f.size {
		f.prompt, f.promptAt = codexFirstPrompt(f.path), f.size
	}
	return f.prompt
}

func (s *Server) loadCodexNamesLocked() {
	ix := &s.codex
	path := filepath.Join(s.cfg.CodexDir, "session_index.jsonl")
	fi, err := os.Stat(path)
	if err != nil || fi.ModTime().Equal(ix.namesM) {
		return
	}
	names := map[string]string{}
	if fh, err := os.Open(path); err == nil {
		_ = eachLine(fh, 1<<20, func(line []byte) bool {
			var l struct {
				ID   string `json:"id"`
				Name string `json:"thread_name"`
			}
			if json.Unmarshal(line, &l) == nil && l.ID != "" {
				names[l.ID] = l.Name // 后写的是改过的名字
			}
			return true
		})
		fh.Close()
	}
	ix.names, ix.namesM = names, fi.ModTime()
}

// codexFirstPrompt 找第一条用户消息。前面有系统注入的指令、AGENTS.md，所以要往后读一段
func codexFirstPrompt(path string) string {
	fh, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer fh.Close()
	prompt, read := "", 0
	_ = eachLine(fh, 4<<20, func(line []byte) bool {
		read += len(line)
		if bytes.Contains(line, []byte(`"UserMessage"`)) {
			var l struct {
				Payload struct {
					Type string          `json:"type"`
					Item json.RawMessage `json:"item"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &l) == nil && l.Payload.Type == "item_completed" {
				if it, ok := parseCodexItem(l.Payload.Item); ok && it.Type == "userMessage" {
					prompt = titleText(it.Text)
				}
			}
		}
		return prompt == "" && read < 16<<20
	})
	return prompt
}

func titleText(s string) string {
	s = strings.TrimSpace(stripAttachments(s))
	if s == "" || strings.HasPrefix(s, "<") {
		return ""
	}
	return oneLine(s, 80)
}

func (s *Server) codexInfo(f codexFile) SessionInfo {
	return SessionInfo{ID: f.id, Agent: agentCodex, Title: s.codexTitle(f.path), Cwd: f.cwd,
		Mtime: f.mtime, Size: f.size, Archived: s.archive.has(f.id), path: f.path}
}

// ---- rollout → Entry ----

// codexItem：codex 的一个对话条目。rollout 里（event_msg / item_completed）是核心格式：类型 PascalCase、
// 字段 snake_case；app-server 实时推的是 v2 格式：全 camelCase。先都归一成这个再转 Entry，
// 历史回看和实时对话就是同一套显示。
type codexItem struct {
	Type    string // userMessage / agentMessage / reasoning / commandExecution / fileChange / mcpToolCall …
	ID      string
	Text    string
	Command string
	Output  string
	Exit    *int
	Status  string
	Changes []patchChange
	Tool    string
	Args    json.RawMessage
}

type patchChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // add / update / delete
	Diff string `json:"diff"`

	stats FileChange // 截短 diff 之前算好的增删行数
}

type codexRawItem struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Text    string          `json:"text"`
	Content json.RawMessage `json:"content"`
	Phase   string          `json:"phase"`

	Summary     []string `json:"summary"`
	SummaryText []string `json:"summary_text"`
	RawContent  []string `json:"raw_content"`

	Command   json.RawMessage `json:"command"`
	OutV2     *string         `json:"aggregatedOutput"`
	OutCore   *string         `json:"aggregated_output"`
	Stdout    string          `json:"stdout"`
	Stderr    string          `json:"stderr"`
	ExitV2    *int            `json:"exitCode"`
	ExitCore  *int            `json:"exit_code"`
	Status    json.RawMessage `json:"status"`
	Changes   json.RawMessage `json:"changes"`
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Result    json.RawMessage `json:"result"`
	Error     json.RawMessage `json:"error"`
	Query     string          `json:"query"`
	Path      string          `json:"path"`
	Prompt    *string         `json:"prompt"`
}

func parseCodexItem(raw json.RawMessage) (*codexItem, bool) {
	var r codexRawItem
	if json.Unmarshal(raw, &r) != nil || r.Type == "" {
		return nil, false
	}
	// 核心格式 UserMessage → userMessage
	t, n := utf8.DecodeRuneInString(r.Type)
	it := &codexItem{Type: string(unicode.ToLower(t)) + r.Type[n:], ID: r.ID}
	_ = json.Unmarshal(r.Status, &it.Status)

	switch it.Type {
	case "userMessage", "agentMessage":
		it.Text = r.Text
		if it.Text == "" {
			it.Text = contentText(r.Content)
		}
	case "plan":
		it.Text = r.Text
	case "reasoning":
		parts := r.Summary
		if len(parts) == 0 {
			parts = r.SummaryText
		}
		if len(parts) == 0 {
			parts = r.RawContent
		}
		if len(parts) == 0 {
			_ = json.Unmarshal(r.Content, &parts) // v2 的 content 是完整推理文本（开了才有）
		}
		it.Text = strings.Join(parts, "\n\n")
	case "commandExecution":
		it.Command = commandText(r.Command)
		switch {
		case r.OutV2 != nil:
			it.Output = *r.OutV2
		case r.OutCore != nil:
			it.Output = *r.OutCore
		default:
			it.Output = r.Stdout + r.Stderr
		}
		it.Exit = r.ExitV2
		if it.Exit == nil {
			it.Exit = r.ExitCore
		}
	case "fileChange":
		it.Changes = patchChanges(r.Changes)
		it.Output = strings.TrimSpace(r.Stdout + r.Stderr)
	case "mcpToolCall":
		it.Tool = "mcp__" + r.Server + "__" + r.Tool
		it.Args = r.Arguments
		it.Output = mcpResultText(r.Result, r.Error)
	case "dynamicToolCall":
		it.Tool, it.Args = r.Tool, r.Arguments
		it.Output = contentText(r.Content)
	case "webSearch":
		it.Text = r.Query
	case "imageView":
		it.Text = strings.TrimPrefix(r.Path, "file://")
	case "collabAgentToolCall":
		it.Tool = r.Tool
		if r.Prompt != nil {
			it.Text = *r.Prompt
		}
	}
	return it, true
}

// contentText 取 [{type:"text"|"Text"|"input_text"|"output_text", text}] 里的文字
func contentText(raw json.RawMessage) string {
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		switch strings.ToLower(p.Type) {
		case "text", "input_text", "output_text":
			if p.Text != "" {
				out = append(out, p.Text)
			}
		case "image", "localimage", "input_image":
			out = append(out, "[图片]")
		}
	}
	return strings.Join(out, "\n")
}

var shellWrapRe = regexp.MustCompile(`^(?:\S*/)?(?:ba|z)?sh -l?c (.+)$`)

// commandText：codex 的命令都包在 `zsh -lc "…"` 里（核心格式是 argv 数组），显示时剥掉这层
func commandText(raw json.RawMessage) string {
	var argv []string
	if json.Unmarshal(raw, &argv) == nil {
		if len(argv) == 3 && (argv[1] == "-lc" || argv[1] == "-c") {
			return argv[2]
		}
		return strings.Join(argv, " ")
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	if m := shellWrapRe.FindStringSubmatch(s); m != nil {
		return unquoteShell(m[1])
	}
	return s
}

func unquoteShell(s string) string {
	if len(s) < 2 || s[0] != s[len(s)-1] {
		return s
	}
	switch s[0] {
	case '\'':
		return strings.ReplaceAll(s[1:len(s)-1], `'\''`, `'`)
	case '"':
		r := strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\$`, `$`, "\\`", "`")
		return r.Replace(s[1 : len(s)-1])
	}
	return s
}

// patchChanges：v2 是 [{path, kind:{type}, diff}]，核心格式是 {路径: {type, unified_diff | content}}
func patchChanges(raw json.RawMessage) []patchChange {
	var v2 []struct {
		Path string `json:"path"`
		Kind struct {
			Type string `json:"type"`
		} `json:"kind"`
		Diff string `json:"diff"`
	}
	var out []patchChange
	if json.Unmarshal(raw, &v2) == nil {
		for _, c := range v2 {
			out = append(out, patchChange{Path: c.Path, Kind: c.Kind.Type, Diff: clip(c.Diff, maxInputStr), stats: patchStats(c.Path, c.Kind.Type, c.Diff)})
		}
		return out
	}
	var core map[string]struct {
		Type        string `json:"type"`
		UnifiedDiff string `json:"unified_diff"`
		Content     string `json:"content"`
	}
	if json.Unmarshal(raw, &core) != nil {
		return nil
	}
	for path, c := range core {
		diff := c.UnifiedDiff
		if diff == "" {
			diff = c.Content
		}
		out = append(out, patchChange{Path: path, Kind: c.Type, Diff: clip(diff, maxInputStr), stats: patchStats(path, c.Type, diff)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func mcpResultText(result, errRaw json.RawMessage) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(errRaw, &e) == nil && e.Message != "" {
		return e.Message
	}
	var r struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(result, &r) == nil && len(r.Content) > 0 {
		if t := contentText(r.Content); t != "" {
			return t
		}
	}
	if len(result) > 0 && string(result) != "null" {
		return string(result)
	}
	return ""
}

func (it *codexItem) isTool() bool {
	switch it.Type {
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "webSearch", "imageView", "collabAgentToolCall":
		return true
	}
	return false
}

// toolUse：命令、改文件、MCP 调用显示成工具卡片。名字尽量用 Claude 的（Bash / Read / WebSearch），
// 前端按名字挑渲染方式；改文件是 Patch（统一 diff）
func (it *codexItem) toolUse() Block {
	var name string
	var input any
	switch it.Type {
	case "commandExecution":
		name, input = "Bash", map[string]any{"command": it.Command}
	case "fileChange":
		name, input = "Patch", map[string]any{"changes": it.Changes}
	case "webSearch":
		name, input = "WebSearch", map[string]any{"query": it.Text}
	case "imageView":
		name, input = "Read", map[string]any{"file_path": it.Text}
	case "collabAgentToolCall":
		name, input = "Agent", map[string]any{"description": it.Tool, "prompt": it.Text}
	default:
		name, input = it.Tool, it.Args
	}
	b := Block{T: "tool_use", ID: it.ID, Name: name}
	for _, c := range it.Changes {
		if it.Type == "fileChange" {
			b.Files = append(b.Files, c.stats)
		}
	}
	if raw, ok := input.(json.RawMessage); ok {
		b.Input = clipInput(raw)
	} else {
		b.Input, _ = json.Marshal(input)
		b.Input = clipInput(b.Input)
	}
	return b
}

func (it *codexItem) toolResult() Block {
	text := it.Output
	failed := it.Status == "failed" || it.Status == "declined" || (it.Exit != nil && *it.Exit != 0)
	switch {
	case it.Exit != nil && *it.Exit != 0:
		text = strings.TrimRight(text, "\n") + fmt.Sprintf("\n（退出码 %d）", *it.Exit)
	case it.Status == "declined" && text == "":
		text = "已拒绝"
	case it.Type == "fileChange" && text == "" && !failed:
		text = "已应用"
	}
	return Block{T: "tool_result", ID: it.ID, Text: clip(text, maxResultLen), IsError: failed}
}

// entries：一个已完成的条目转成页面上的 Entry（工具是一对：调用 + 结果）
func (it *codexItem) entries(ts string) []Entry {
	text := func(role, t, s string) []Entry {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return []Entry{{Role: role, Blocks: []Block{{T: t, Text: clip(s, maxTextLen)}}, TS: ts}}
	}
	switch it.Type {
	case "userMessage":
		return text("user", "text", it.Text)
	case "agentMessage", "plan":
		return text("assistant", "text", it.Text)
	case "reasoning":
		return text("assistant", "thinking", it.Text)
	case "contextCompaction":
		return []Entry{noteEntry("上下文已压缩", ts)}
	case "enteredReviewMode":
		return []Entry{noteEntry("进入审查模式", ts)}
	case "exitedReviewMode":
		return []Entry{noteEntry("审查结束", ts)}
	}
	if it.isTool() {
		return []Entry{
			{Role: "assistant", Blocks: []Block{it.toolUse()}, TS: ts},
			{Role: "user", Blocks: []Block{it.toolResult()}, TS: ts},
		}
	}
	return nil
}

// readCodexEntries 整个 rollout 转成 Entry 列表；太长的只留最后 limit 条。
// 0.147 起每个条目都有一条 item_completed，用它；更老的文件没有，退回去拼 response_item
func readCodexEntries(path string, limit int) ([]Entry, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	var items, legacy []Entry
	haveItems := false
	err = eachLine(f, 32<<20, func(line []byte) bool {
		var l struct {
			Timestamp string          `json:"timestamp"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &l) != nil {
			return true
		}
		switch l.Type {
		case "event_msg":
			var ev struct {
				Type   string          `json:"type"`
				Item   json.RawMessage `json:"item"`
				Reason string          `json:"reason"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(l.Payload, &ev) != nil {
				return true
			}
			var n []Entry
			switch ev.Type {
			case "item_completed":
				haveItems = true
				if it, ok := parseCodexItem(ev.Item); ok {
					items = append(items, it.entries(l.Timestamp)...)
				}
			case "turn_aborted":
				text := "已中断"
				if ev.Reason != "" && ev.Reason != "interrupted" {
					text += "：" + ev.Reason
				}
				n = []Entry{noteEntry(text, l.Timestamp)}
			case "task_complete":
				if ev.Error != nil && ev.Error.Message != "" {
					n = []Entry{noteEntry("出错："+oneLine(ev.Error.Message, 200), l.Timestamp)}
				}
			}
			items, legacy = append(items, n...), append(legacy, n...)
		case "response_item":
			legacy = append(legacy, codexResponseItem(l.Payload, l.Timestamp)...)
		}
		return true
	})
	out := items
	if !haveItems {
		out = legacy
	}
	truncated := false
	if limit > 0 && len(out) > limit {
		out, truncated = out[len(out)-limit:], true
	}
	return out, truncated, err
}

// codexResponseItem：老版本 rollout 的模型层记录（没有 item_completed 时才用）
func codexResponseItem(raw json.RawMessage, ts string) []Entry {
	var r struct {
		Type      string          `json:"type"`
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		Summary   json.RawMessage `json:"summary"`
		Name      string          `json:"name"`
		Arguments string          `json:"arguments"`
		Input     string          `json:"input"`
		CallID    string          `json:"call_id"`
		Output    json.RawMessage `json:"output"`
		Action    *struct {
			Command []string `json:"command"`
		} `json:"action"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	one := func(role string, b Block) []Entry { return []Entry{{Role: role, Blocks: []Block{b}, TS: ts}} }
	switch r.Type {
	case "message":
		text := contentText(r.Content)
		switch {
		case strings.TrimSpace(text) == "":
		case r.Role == "assistant":
			return one("assistant", Block{T: "text", Text: clip(text, maxTextLen)})
		case r.Role == "user" && !codexInjected(text):
			return one("user", Block{T: "text", Text: clip(text, maxTextLen)})
		}
	case "reasoning":
		if t := contentText(r.Summary); strings.TrimSpace(t) != "" {
			return one("assistant", Block{T: "thinking", Text: clip(t, maxTextLen)})
		}
	case "function_call", "custom_tool_call", "local_shell_call":
		name, input := r.Name, json.RawMessage(r.Arguments)
		var args struct {
			Command json.RawMessage `json:"command"`
			Cmd     string          `json:"cmd"`
		}
		switch {
		case r.Action != nil:
			name, input = "Bash", mustJSON(map[string]string{"command": commandText(mustJSON(r.Action.Command))})
		case name == "shell" || name == "exec_command" || name == "shell_command":
			_ = json.Unmarshal(input, &args)
			cmd := args.Cmd
			if cmd == "" {
				cmd = commandText(args.Command)
			}
			name, input = "Bash", mustJSON(map[string]string{"command": cmd})
		case r.Type == "custom_tool_call":
			input = mustJSON(map[string]string{"input": r.Input})
		}
		if !json.Valid(input) {
			input = mustJSON(map[string]string{"arguments": r.Arguments})
		}
		return one("assistant", Block{T: "tool_use", ID: r.CallID, Name: name, Input: clipInput(input)})
	case "function_call_output", "custom_tool_call_output":
		return one("user", Block{T: "tool_result", ID: r.CallID, Text: clip(codexOutputText(r.Output), maxResultLen)})
	}
	return nil
}

func codexOutputText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		var wrapped struct {
			Output string `json:"output"`
		}
		if json.Unmarshal([]byte(s), &wrapped) == nil && wrapped.Output != "" {
			return wrapped.Output
		}
		return s
	}
	var obj struct {
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Content != "" {
		return obj.Content
	}
	return contentText(raw)
}

// 系统注入的上下文（环境信息、AGENTS.md、技能列表）也是 user 角色，不当成提问显示
func codexInjected(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "<") || strings.HasPrefix(t, "# AGENTS.md")
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
