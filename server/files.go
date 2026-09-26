package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// GET /api/file?path=&cwd=&project=  —— 页面上点文件名，在右侧打开看（只读）。
//
// 相对路径依次按 cwd（对话的工作目录）、项目目录、RCWEB_ROOT 去找，第一个存在的算数。
// 只给看 RCWEB_ROOT 下的文件：解开符号链接后再判断，防止借链接跳出去。

const maxFileBytes = 1 << 20

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := strings.TrimSpace(q.Get("path"))
	if p == "" {
		writeErr(w, http.StatusBadRequest, "缺少 path")
		return
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, rest)
	}
	var cands []string
	if filepath.IsAbs(p) {
		cands = []string{p}
	} else {
		if c := q.Get("cwd"); filepath.IsAbs(c) {
			cands = append(cands, filepath.Join(c, p))
		}
		if proj, ok := s.projectPath(q.Get("project")); ok {
			cands = append(cands, filepath.Join(proj, p))
		}
		cands = append(cands, filepath.Join(s.cfg.Root, p))
	}

	root, err := filepath.EvalSymlinks(s.cfg.Root)
	if err != nil {
		root = s.cfg.Root
	}
	var real string
	forbidden := false
	for _, c := range cands {
		rp, err := filepath.EvalSymlinks(c)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(root, rp); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			forbidden = true
			continue
		}
		if fi, err := os.Stat(rp); err == nil && !fi.IsDir() {
			real = rp
			break
		}
	}
	if real == "" {
		if forbidden {
			writeErr(w, http.StatusForbidden, "只能看项目根目录（"+s.cfg.Root+"）下的文件")
		} else {
			writeErr(w, http.StatusNotFound, "找不到文件: "+q.Get("path"))
		}
		return
	}

	f, err := os.Open(real)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()
	fi, _ := f.Stat()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 前 8KB 里有 NUL 就当二进制，不往页面上发内容
	binary := bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0
	rel, _ := filepath.Rel(root, real)
	resp := map[string]any{
		"path": real, "rel": rel, "size": fi.Size(),
		"truncated": fi.Size() > maxFileBytes, "binary": binary, "content": "",
	}
	if !binary {
		resp["content"] = string(data)
	}
	writeJSON(w, resp)
}
