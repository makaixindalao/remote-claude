package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 输入历史：直接用 Claude Code 自己的 <ClaudeDir>/history.jsonl（TUI 里按 ↑ 翻的就是它），
// 按项目路径筛。网页里发的消息也追加进去，这样在 VPS 上开 TUI 按 ↑ 同样翻得到。
// 发给 codex 的追加进 codex 自己的 history.jsonl（它的 TUI 按 ↑ 翻那个）；grok 的历史不是文件，不写。

type historyLine struct {
	Display        string                     `json:"display"`
	PastedContents map[string]json.RawMessage `json:"pastedContents"`
	Timestamp      int64                      `json:"timestamp"`
	Project        string                     `json:"project"`
	SessionID      string                     `json:"sessionId,omitempty"`
}

var (
	historyMu  sync.Mutex
	pastedRefs = regexp.MustCompile(`\[Pasted text #(\d+)(?: \+\d+ lines)?\]`)
)

func (s *Server) historyPath() string { return filepath.Join(s.cfg.ClaudeDir, "history.jsonl") }

func (s *Server) appendHistory(agent, cwd, sessionID, text string) {
	var line []byte
	path := s.historyPath()
	switch agent {
	case agentClaude:
		line, _ = json.Marshal(historyLine{
			Display: text, PastedContents: map[string]json.RawMessage{},
			Timestamp: time.Now().UnixMilli(), Project: cwd, SessionID: sessionID,
		})
	case agentCodex:
		path = filepath.Join(s.cfg.CodexDir, "history.jsonl")
		line, _ = json.Marshal(map[string]any{"session_id": sessionID, "ts": time.Now().Unix(), "text": text})
	default:
		return
	}
	historyMu.Lock()
	defer historyMu.Unlock()
	// O_APPEND + 一次写完：和 CLI 同时追加也不会交错
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// /api/history?project=  最新的在前，最多 200 条，连续重复的只留一条
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	path, ok := s.projectPath(r.URL.Query().Get("project"))
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这个项目")
		return
	}
	f, err := os.Open(s.historyPath())
	if err != nil {
		writeJSON(w, []string{})
		return
	}
	defer f.Close()
	const tail = 4 << 20
	if fi, err := f.Stat(); err == nil && fi.Size() > tail {
		_, _ = f.Seek(fi.Size()-tail, io.SeekStart)
	}
	data, _ := io.ReadAll(f)

	var out []string
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0 && len(out) < 200; i-- {
		var h historyLine
		if json.Unmarshal([]byte(lines[i]), &h) != nil || h.Display == "" {
			continue
		}
		// 本项目和它的 worktree 里输入过的都算
		if h.Project != path && !strings.HasPrefix(h.Project, path+"/") {
			continue
		}
		text := expandPasted(h)
		if len(out) > 0 && out[len(out)-1] == text {
			continue
		}
		out = append(out, text)
	}
	if out == nil {
		out = []string{}
	}
	writeJSON(w, out)
}

// TUI 把大段粘贴记成 [Pasted text #1 +7 lines]，原文在 pastedContents 里，翻历史时还原回去
func expandPasted(h historyLine) string {
	return pastedRefs.ReplaceAllStringFunc(h.Display, func(ref string) string {
		id := pastedRefs.FindStringSubmatch(ref)[1]
		var p struct {
			Content string `json:"content"`
		}
		if raw, ok := h.PastedContents[id]; ok && json.Unmarshal(raw, &p) == nil && p.Content != "" {
			return p.Content
		}
		return ref
	})
}
