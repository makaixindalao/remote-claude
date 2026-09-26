package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 打开会话（历史回看、网页对话的快照）只给最后一页，而且工具调用只带摘要：
//   - 分页：按 Entry 下标切；往上翻带 before=<上一页的 start> 再要一页。一页按「行」数：
//     合并模式下一串连续的工具调用只占一行，codex 一跑几百次工具也就几行字，按条数切会要好几页才填满一屏。
//     起点再往前挪到一轮对话的开头（用户说的那句话），第一屏别从一串工具调用中间开始
//   - 精简：tool_use 的参数只留摘要用得到的（结构不变，长字符串截短、长数组截短），tool_result 的正文拿掉，
//     都标上 lazy。前端展开卡片时再按 id 取全文：/api/session/tools、/api/chats/{id}/tools
// 大会话里九成以上的字节是工具的参数和输出，页面默认又停在最底下、工具默认收着，所以第一屏几乎不受影响。

const (
	pageRows     = 40  // 一页大约这么多行，两三屏
	pageSize     = 400 // 一页最多这么多条：「逐个展开」模式下每次调用都是一行，别太长
	pageMax      = 1000
	pageAlign    = 60  // 往前找一轮的开头，最多找这么远
	summaryStr   = 200 // 摘要里的字符串最多留这么多字
	summaryItems = 50  // 数组最多留这么多项（TodoWrite 的条数、Patch 的文件列表要用）
	summaryDepth = 3
)

type page struct {
	Entries []Entry `json:"entries"`
	Start   int     `json:"start"` // 这一页第一条在整个会话里的下标
	Total   int     `json:"total"`
	More    bool    `json:"more"` // 前面还有能加载的
}

// pageOf 取下标 before 之前的一页（before <= 0 表示取到最后），最多 limit 条。
// all[0] 在整个会话里是第 base 条，下标都按整个会话算
func pageOf(all []Entry, base, before, limit int) page {
	end := len(all)
	if before > 0 {
		end = min(end, max(0, before-base))
	}
	start, rows, inTools := end, 0, false
	for start > 0 && end-start < limit && rows < pageRows {
		start--
		tools := toolsOnly(all[start])
		if !tools || !inTools {
			rows++
		}
		inTools = tools
	}
	if start > 0 && !isPrompt(all[start]) {
		for i := start - 1; i >= max(0, start-pageAlign); i-- {
			if isPrompt(all[i]) {
				start = i
				break
			}
		}
	}
	return page{Entries: liteEntries(all[start:end]), Start: base + start, Total: base + len(all), More: start > 0}
}

func toolsOnly(e Entry) bool {
	for _, b := range e.Blocks {
		if b.T != "tool_use" && b.T != "tool_result" {
			return false
		}
	}
	return len(e.Blocks) > 0
}

func isPrompt(e Entry) bool {
	if e.Role != "user" {
		return false
	}
	for _, b := range e.Blocks {
		if b.T == "text" {
			return true
		}
	}
	return false
}

func pageParams(r *http.Request) (before, limit int) {
	q := r.URL.Query()
	before, _ = strconv.Atoi(q.Get("before"))
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit <= 0 {
		limit = pageSize
	}
	return before, min(limit, pageMax)
}

// 这些工具不精简：问答卡片要从结果里解析选了哪个，本身也小
var keepFull = map[string]bool{"AskUserQuestion": true}

// liteEntries 返回精简过的副本，不动传进来的（它们可能在缓存里）。
// 工具结果只有在它的 tool_use 也在这一页里时才拿掉正文 —— 孤零零的结果前端单独显示，得留着
func liteEntries(es []Entry) []Entry {
	uses := map[string]string{} // tool_use id → 工具名
	for _, e := range es {
		for _, b := range e.Blocks {
			if b.T == "tool_use" && b.ID != "" {
				uses[b.ID] = b.Name
			}
		}
	}
	out := make([]Entry, len(es))
	for i, e := range es {
		out[i] = e
		var blocks []Block
		for j, b := range e.Blocks {
			switch {
			case b.T == "tool_use" && b.ID != "" && !keepFull[b.Name]:
				b.Input = summaryInput(b.Input)
			case b.T == "tool_result" && uses[b.ID] != "" && !keepFull[uses[b.ID]]:
				b.Text = ""
				b.Files = statsOnly(b.Files)
			default:
				continue
			}
			b.Lazy = true
			if blocks == nil {
				blocks = append([]Block(nil), e.Blocks...)
			}
			blocks[j] = b
		}
		if blocks != nil {
			out[i].Blocks = blocks
		}
	}
	return out
}

// summaryInput：卡片标题、合并模式那一行字只用得到命令、路径、pattern、描述这些短字段，
// 但各个工具用哪个字段不一样（还有 MCP 工具取第一个字符串字段），所以不挑字段，
// 保留原来的结构和字段顺序，只把长的截短
func summaryInput(raw json.RawMessage) json.RawMessage {
	if len(raw) <= 512 {
		return raw
	}
	return shrinkJSON(raw, 0)
}

func shrinkJSON(raw json.RawMessage, depth int) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return raw
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil || utf8.RuneCountInString(s) <= summaryStr {
			return raw
		}
		b, _ := json.Marshal(string([]rune(s)[:summaryStr]) + "…")
		return b
	case '[':
		var items []json.RawMessage
		if depth >= summaryDepth || json.Unmarshal(raw, &items) != nil {
			return json.RawMessage("[]")
		}
		var buf bytes.Buffer
		buf.WriteByte('[')
		for i, it := range items[:min(len(items), summaryItems)] {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.Write(shrinkJSON(it, depth+1))
		}
		buf.WriteByte(']')
		return buf.Bytes()
	case '{':
		if depth >= summaryDepth {
			return json.RawMessage("{}")
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		if _, err := dec.Token(); err != nil {
			return json.RawMessage("{}")
		}
		var buf bytes.Buffer
		buf.WriteByte('{')
		for n := 0; dec.More(); n++ {
			k, err := dec.Token()
			var v json.RawMessage
			if err != nil || dec.Decode(&v) != nil {
				return json.RawMessage("{}")
			}
			if n > 0 {
				buf.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			buf.Write(kb)
			buf.WriteByte(':')
			buf.Write(shrinkJSON(v, depth+1))
		}
		buf.WriteByte('}')
		return buf.Bytes()
	}
	return raw // 数字、true / false、null
}

// ---- 工具全文 ----

type toolDetail struct {
	Input  json.RawMessage `json:"input,omitempty"`
	Result *toolResult     `json:"result,omitempty"` // 还没跑完的没有
	Files  []FileChange    `json:"files,omitempty"`  // 改文件的工具：带 hunks 的 diff，结果里有就用结果的
}

type toolResult struct {
	Text    string `json:"text"`
	IsError bool   `json:"isError,omitempty"`
}

// toolIDs：?ids=a,b,c，一次最多 200 个
func toolIDs(r *http.Request) map[string]bool {
	want := map[string]bool{}
	for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if id = strings.TrimSpace(id); id != "" && len(want) < 200 {
			want[id] = true
		}
	}
	return want
}

func findTools(entries []Entry, want map[string]bool) map[string]*toolDetail {
	out := map[string]*toolDetail{}
	get := func(id string) *toolDetail {
		if out[id] == nil {
			out[id] = &toolDetail{}
		}
		return out[id]
	}
	names := map[string]string{}
	for _, e := range entries {
		for _, b := range e.Blocks {
			switch {
			case !want[b.ID]:
			case b.T == "tool_use":
				get(b.ID).Input = b.Input
				names[b.ID] = b.Name
			case b.T == "tool_result":
				d := get(b.ID)
				d.Result = &toolResult{Text: b.Text, IsError: b.IsError}
				d.Files = b.Files
			}
		}
	}
	// 按参数算的 diff 不存，要的时候现算（参数是截短过的，特别大的改动 diff 也只到截断处）
	for id, d := range out {
		if len(d.Files) == 0 {
			d.Files = fileChanges(names[id], d.Input, true)
		}
	}
	return out
}

// ---- HTTP ----

// writeSession 回 /api/session：{session, entries, start, total, more, truncated}，entries 是精简过的一页；
// truncated 表示更早的太老了没留，翻到头也看不到
func (s *Server) writeSession(w http.ResponseWriter, r *http.Request, info SessionInfo) {
	before, limit := pageParams(r)
	key := sessionKey(info)
	if key != "" {
		etag := etagOf(key, info, before, limit)
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "private, no-cache") // 浏览器可以存，但每次用之前都要带着 ETag 来问
		if etagMatch(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	d, err := s.loadSession(info)
	if err != nil {
		w.Header().Del("ETag")
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	p := pageOf(d.entries, d.base, before, limit)
	body := map[string]any{"session": info, "entries": p.Entries, "start": p.Start, "total": p.Total, "more": p.More, "truncated": d.base > 0}
	if key == "" {
		writeJSON(w, body)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(body)
}

// handleSessionTools：GET /api/session/tools?project=&agent=&id=&ids=a,b —— 精简掉的工具参数和输出
func (s *Server) handleSessionTools(w http.ResponseWriter, r *http.Request) {
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
	d, err := s.loadSession(info)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	tools := findTools(d.entries, toolIDs(r))
	cwd := info.Cwd // 同步过来的会话，cwd 可能是另一台机器上的路径
	if !isDir(cwd) || !underDir(path, cwd) {
		cwd = path
	}
	s.anchorTools(tools, cwd)
	writeJSON(w, map[string]any{"tools": tools})
}

// chatEntries：对话当前的全部 Entry。只追加不修改（/clear 是整个换掉），拿出锁外读是安全的
func (s *Server) chatEntries(w http.ResponseWriter, r *http.Request) ([]Entry, bool) {
	c := s.chats.Get(r.PathValue("id"))
	if c == nil {
		writeErr(w, http.StatusNotFound, "没有这个对话")
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries, true
}

// handleChatEntries：GET /api/chats/{id}/entries?before=&limit= —— 往上翻时要更早的一页
func (s *Server) handleChatEntries(w http.ResponseWriter, r *http.Request) {
	if all, ok := s.chatEntries(w, r); ok {
		before, limit := pageParams(r)
		writeJSON(w, pageOf(all, 0, before, limit))
	}
}

// handleChatTools：GET /api/chats/{id}/tools?ids=a,b
func (s *Server) handleChatTools(w http.ResponseWriter, r *http.Request) {
	if all, ok := s.chatEntries(w, r); ok {
		tools := findTools(all, toolIDs(r))
		if c := s.chats.Get(r.PathValue("id")); c != nil {
			s.anchorTools(tools, c.cwd)
		}
		writeJSON(w, map[string]any{"tools": tools})
	}
}
