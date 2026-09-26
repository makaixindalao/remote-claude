package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// 会话 JSONL 的一行，和 stream-json 里 user / assistant 事件是同一个形状 ——
// 历史记录和实时聊天因此共用这一套解析，前端也只认一种 Entry。

type Block struct {
	T       string          `json:"t"` // text | thinking | tool_use | tool_result | image
	Text    string          `json:"text,omitempty"`
	ID      string          `json:"id,omitempty"`
	Name    string          `json:"name,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	IsError bool            `json:"isError,omitempty"`
	Lazy    bool            `json:"lazy,omitempty"` // 精简过：参数只剩摘要 / 输出拿掉了，全文按 id 另取（见 session_page.go）
	// 改文件的工具改了哪些文件、各增删几行（见 diff.go）。tool_use 上是按参数算的，只有行数；
	// Claude 的 tool_result 上是它自己给的 structuredPatch，带 hunks（精简版里拿掉）
	Files []FileChange `json:"files,omitempty"`
}

type Entry struct {
	Role   string  `json:"role"` // user | assistant | note
	Blocks []Block `json:"blocks"`
	TS     string  `json:"ts,omitempty"`
	// assistant：这条实际是哪个模型回的（API 应答里的 model）。选的是 Opus 5.5，也可能被降成 5 / 4.8 回，
	// 页面上据此标出来。只有 claude 有；CLI 自己合成的提示（<synthetic>）不算
	Model string `json:"model,omitempty"`
}

type record struct {
	Type    string `json:"type"`
	Message *struct {
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	// 技能正文、系统注入这类不给人看的消息：会话文件里标 isMeta，stream-json 里标 isSynthetic（压缩摘要也算）
	IsMeta           bool    `json:"isMeta"`
	IsSynthetic      bool    `json:"isSynthetic"`
	IsSidechain      bool    `json:"isSidechain"`
	IsCompactSummary bool    `json:"isCompactSummary"`
	ParentToolUseID  *string `json:"parent_tool_use_id"`
	Timestamp        string  `json:"timestamp"`
	Cwd              string  `json:"cwd"`
	// 工具结果的结构化版本：会话文件里叫 toolUseResult，stream-json 里叫 tool_use_result
	ToolUseResult  json.RawMessage `json:"toolUseResult"`
	ToolUseResult2 json.RawMessage `json:"tool_use_result"`

	CustomTitle string `json:"customTitle"`
	AITitle     string `json:"aiTitle"`
	Summary     string `json:"summary"`
}

type rawBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	Source    *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	} `json:"source"`
}

const (
	maxTextLen   = 200 << 10
	maxResultLen = 32 << 10
	maxInputLen  = 48 << 10
	maxInputStr  = 16 << 10
	maxImageLen  = 2 << 20
)

// entryFromRecord 把一条记录转成界面用的 Entry；不该显示的（元信息、子代理内部消息）返回 nil
func entryFromRecord(r *record) *Entry {
	if (r.Type != "user" && r.Type != "assistant") || r.Message == nil {
		return nil
	}
	// isMeta / isSynthetic：技能正文、系统注入之类；isSidechain / parent_tool_use_id：子代理（Task）内部的来回。
	// 实时聊天的压缩摘要也是 isSynthetic，直接丢掉：compact_boundary 那边已经插了一行提示
	if r.IsMeta || r.IsSynthetic || r.IsSidechain || (r.ParentToolUseID != nil && *r.ParentToolUseID != "") {
		return nil
	}
	if r.IsCompactSummary {
		return &Entry{Role: "note", Blocks: []Block{{T: "text", Text: "上下文已压缩"}}, TS: r.Timestamp}
	}
	e := &Entry{Role: r.Type, TS: r.Timestamp}
	if r.Type == "assistant" && r.Message.Model != "<synthetic>" {
		e.Model = r.Message.Model
	}

	var s string
	if json.Unmarshal(r.Message.Content, &s) == nil {
		if s != "" {
			e.Blocks = append(e.Blocks, Block{T: "text", Text: clip(s, maxTextLen)})
		}
	} else {
		var blocks []rawBlock
		if json.Unmarshal(r.Message.Content, &blocks) != nil {
			return nil
		}
		for _, b := range blocks {
			if nb, ok := convertBlock(b); ok {
				e.Blocks = append(e.Blocks, nb)
			}
		}
	}
	if len(e.Blocks) == 0 {
		return nil
	}
	attachResultFiles(r, e)
	return e
}

// attachResultFiles 把 Claude 改文件的结果（带行号的 diff）挂到 tool_result 上。
// 一条记录只对应一次工具调用，有好几个 tool_result 时对不上是哪个的，就不挂
func attachResultFiles(r *record, e *Entry) {
	raw := r.ToolUseResult
	if len(raw) == 0 {
		raw = r.ToolUseResult2
	}
	if r.Type != "user" || len(raw) == 0 {
		return
	}
	idx := -1
	for i, b := range e.Blocks {
		if b.T == "tool_result" {
			if idx >= 0 {
				return
			}
			idx = i
		}
	}
	if idx >= 0 && !e.Blocks[idx].IsError {
		e.Blocks[idx].Files = resultChanges(raw)
	}
}

func convertBlock(b rawBlock) (Block, bool) {
	switch b.Type {
	case "text":
		return Block{T: "text", Text: clip(b.Text, maxTextLen)}, b.Text != ""
	case "thinking":
		// 新模型的 thinking 常常是空串 + 签名（内容不外发），空的就不占位置
		return Block{T: "thinking", Text: clip(b.Thinking, maxTextLen)}, strings.TrimSpace(b.Thinking) != ""
	case "tool_use":
		return Block{T: "tool_use", ID: b.ID, Name: b.Name, Input: clipInput(b.Input), Files: fileChanges(b.Name, b.Input, false)}, true
	case "tool_result":
		return Block{T: "tool_result", ID: b.ToolUseID, Text: resultText(b.Content), IsError: b.IsError}, true
	case "image":
		return Block{T: "image", Text: imageSrc(b)}, true
	}
	return Block{}, false
}

func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return clip(s, maxResultLen)
	}
	var parts []rawBlock
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range parts {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		switch p.Type {
		case "text":
			sb.WriteString(p.Text)
		case "image":
			sb.WriteString("[图片]")
		}
	}
	return clip(sb.String(), maxResultLen)
}

func imageSrc(b rawBlock) string {
	if b.Source == nil {
		return ""
	}
	switch {
	case b.Source.Type == "base64" && len(b.Source.Data) <= maxImageLen:
		return "data:" + b.Source.MediaType + ";base64," + b.Source.Data
	case b.Source.Type == "url" && strings.HasPrefix(b.Source.URL, "https://"):
		return b.Source.URL
	}
	return ""
}

// clipInput 限制工具参数的体积：Write 一个大文件时 content 能有几百 KB，
// 页面只需要看个开头。超限时逐个截断里面的长字符串，保持 JSON 结构不变。
func clipInput(raw json.RawMessage) json.RawMessage {
	if len(raw) <= maxInputLen {
		return raw
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	out, err := json.Marshal(clipStrings(v))
	if err != nil {
		return nil
	}
	return out
}

func clipStrings(v any) any {
	switch t := v.(type) {
	case string:
		return clip(t, maxInputStr)
	case []any:
		for i := range t {
			t[i] = clipStrings(t[i])
		}
	case map[string]any:
		for k := range t {
			t[k] = clipStrings(t[k])
		}
	}
	return v
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n…（已截断，原长 %d 字节）", len(s))
}

// eachLine 逐行回调，超过 maxLine 的行（多半是内嵌大图）整行跳过而不是把内存撑爆；
// fn 返回 false 提前结束。
func eachLine(r io.Reader, maxLine int, fn func([]byte) bool) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var buf []byte
	skipping := false
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 && !skipping {
			if len(buf)+len(chunk) > maxLine {
				skipping, buf = true, buf[:0]
			} else {
				buf = append(buf, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if !skipping && len(buf) > 0 {
			if !fn(buf) {
				return nil
			}
		}
		buf, skipping = buf[:0], false
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// promptTitle 从一条用户记录里取可当标题的文字：跳过 <command-name> 这类命令包装和系统注入
func promptTitle(r *record) string {
	if r.Type != "user" || r.Message == nil || r.IsMeta || r.IsSidechain || r.IsCompactSummary {
		return ""
	}
	text := ""
	var s string
	if json.Unmarshal(r.Message.Content, &s) == nil {
		text = s
	} else {
		var blocks []rawBlock
		_ = json.Unmarshal(r.Message.Content, &blocks)
		for _, b := range blocks {
			if b.Type == "text" {
				text = b.Text
				break
			}
		}
	}
	text = strings.TrimSpace(stripAttachments(text))
	if text == "" || strings.HasPrefix(text, "<") || strings.HasPrefix(text, "Caveat:") ||
		strings.HasPrefix(text, "[Request interrupted") {
		return ""
	}
	return oneLine(text, 80)
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n]) + "…"
	}
	return s
}
