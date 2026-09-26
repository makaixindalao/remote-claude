package main

import (
	"bytes"
	"compress/gzip"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
)

// 前端是 ui/ 下的 Vite + React + shadcn/ui 工程，npm run build 的产物 ui/dist 编进二进制。
// 所以编译 Go 之前必须先构建前端 —— rcweb build / deploy 会按顺序做。
// all: 不能省：默认会跳过 _ 开头的文件，而 mermaid 的依赖里就有 _baseUniq-xxx.js 这种 chunk。
//
//go:embed all:ui/dist
var uiFS embed.FS

type staticFiles struct {
	fs   fs.FS
	gzip sync.Map // 路径 → 压缩后的字节；内容是编进来的，压一次就够
}

func init() {
	// 加到主屏幕用的 manifest；Go 的 mime 表里没有这个扩展名
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

func newStatic() *staticFiles {
	sub, _ := fs.Sub(uiFS, "ui/dist")
	return &staticFiles{fs: sub}
}

func (s *staticFiles) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	data, err := fs.ReadFile(s.fs, name)
	if err != nil {
		// 前端用 hash 路由，其余路径一律回首页
		name = "index.html"
		if data, err = fs.ReadFile(s.fs, name); err != nil {
			http.Error(w, "前端没有编进来：先在 server/ui 里 npm run build", http.StatusInternalServerError)
			return
		}
	}

	h := w.Header()
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		h.Set("Content-Type", ct)
	}
	if strings.HasPrefix(name, "assets/") {
		h.Set("Cache-Control", "public, max-age=31536000, immutable") // 文件名带内容 hash
	} else {
		h.Set("Cache-Control", "no-cache")
	}

	ae := r.Header.Get("Accept-Encoding")
	if compressible(name) {
		h.Set("Vary", "Accept-Encoding")
	}
	// 构建时预压好的 brotli（见 ui/vite.config.ts），比 gzip 再小 15% 左右；Go 标准库没有 brotli，只能现成的
	if compressible(name) && strings.Contains(ae, "br") {
		if br, err := fs.ReadFile(s.fs, name+".br"); err == nil {
			h.Set("Content-Encoding", "br")
			w.Write(br)
			return
		}
	}
	if compressible(name) && strings.Contains(ae, "gzip") {
		gz, ok := s.gzip.Load(name)
		if !ok {
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			zw.Write(data)
			zw.Close()
			gz, _ = s.gzip.LoadOrStore(name, buf.Bytes())
		}
		h.Set("Content-Encoding", "gzip")
		data = gz.([]byte)
	}
	w.Write(data)
}

func compressible(name string) bool {
	switch path.Ext(name) {
	case ".html", ".js", ".css", ".svg", ".json", ".txt", ".webmanifest":
		return true
	}
	return false
}
