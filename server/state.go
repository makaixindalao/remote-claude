package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 归档和删除。
//
// 归档只是 rcweb 自己记的标记（$RCWEB_STATE_DIR/archived.json：会话 ID → 归档时间），
// 不碰会话文件，随时能取消。Claude Code 本身没有「归档」这个概念，所以不往 ~/.claude 里写。
//
// 删除是把会话文件挪进各自数据目录下的 .rcweb-trash/，删错了能从那里拿回来：
// claude 是 <id>.jsonl 和同名的附属目录（子代理记录、工具输出），codex 是那个 rollout 文件，
// grok 是整个会话目录。注意 rcsync 会把 claude 会话的删除同步到 Mac。

type archiveStore struct {
	mu   sync.Mutex
	path string
	m    map[string]int64
}

func newArchiveStore(dir string) *archiveStore {
	a := &archiveStore{path: filepath.Join(dir, "archived.json"), m: map[string]int64{}}
	if data, err := os.ReadFile(a.path); err == nil {
		_ = json.Unmarshal(data, &a.m)
	}
	return a
}

func (a *archiveStore) has(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.m[id]
	return ok
}

func (a *archiveStore) set(id string, archived bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if archived {
		a.m[id] = time.Now().UnixMilli()
	} else {
		delete(a.m, id)
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(a.m, "", "  ")
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.path)
}

func stateDir() string {
	if d := os.Getenv("RCWEB_STATE_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "rcweb")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "rcweb")
}

// trashSession 把会话文件和附属目录挪进回收站，旁边留一份 meta.json 记原来的位置
func (s *Server) trashSession(project string, info SessionInfo) (string, error) {
	dest := filepath.Join(s.agentHome(info.Agent), ".rcweb-trash", time.Now().Format("20060102-150405")+"-"+info.ID)
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return "", err
	}
	srcs := []string{info.path}
	if info.Agent == agentClaude {
		srcs = append(srcs, filepath.Join(filepath.Dir(info.path), info.ID))
	}
	moved := []string{}
	for _, src := range srcs {
		if _, err := os.Lstat(src); err != nil {
			continue
		}
		if err := os.Rename(src, filepath.Join(dest, filepath.Base(src))); err != nil {
			return "", fmt.Errorf("挪 %s 失败: %w", src, err)
		}
		moved = append(moved, src)
	}
	meta, _ := json.MarshalIndent(map[string]any{
		"project": project, "agent": info.Agent, "sessionId": info.ID, "title": info.Title,
		"originalPaths": moved, "deletedAt": time.Now().Format(time.RFC3339),
	}, "", "  ")
	_ = os.WriteFile(filepath.Join(dest, "meta.json"), meta, 0o600)
	return dest, nil
}

func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project  string `json:"project"`
		Agent    string `json:"agent"`
		ID       string `json:"id"`
		Archived bool   `json:"archived"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	path, ok := s.projectPath(body.Project)
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这个项目")
		return
	}
	if _, ok := s.findSession(path, body.Agent, body.ID); !ok {
		writeErr(w, http.StatusNotFound, "没有这个会话")
		return
	}
	if err := s.archive.set(body.ID, body.Archived); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string `json:"project"`
		Agent   string `json:"agent"`
		ID      string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	path, ok := s.projectPath(body.Project)
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这个项目")
		return
	}
	info, ok := s.findSession(path, body.Agent, body.ID)
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这个会话")
		return
	}
	if s.chats.BySession()[info.ID] != "" {
		writeErr(w, http.StatusConflict, "这个会话正在网页里跑，先结束对话再删")
		return
	}
	dest, err := s.trashSession(body.Project, info)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.archive.set(info.ID, false)
	writeJSON(w, map[string]string{"trash": dest})
}
