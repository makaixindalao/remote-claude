package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// /api/* 的 JSON 响应按需 gzip。会话记录动辄几 MB（大半是工具输入输出），手机走 Tailscale 时
// 传输比服务端处理慢得多，压完只剩 1/3～1/4。WebSocket 和静态文件不经过这里，各有各的压缩。

// 5 级之后压缩率几乎不涨、耗时明显变长：3MB 的会话记录 33ms 压到 0.97MB，6 级 35ms 还是 0.97MB，
// 27MB 的 codex 会话 5 级 270ms、6 级 370ms
const (
	gzLevel   = 5
	gzMinSize = 1024 // 再小的压了也省不了几个字节
)

var gzPool = sync.Pool{New: func() any {
	zw, _ := gzip.NewWriterLevel(io.Discard, gzLevel)
	return zw
}}

func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

func gzipBytes(data []byte) []byte {
	var buf bytes.Buffer
	zw := gzPool.Get().(*gzip.Writer)
	zw.Reset(&buf)
	zw.Write(data)
	zw.Close()
	zw.Reset(io.Discard) // 别让池子里的 writer 攥着 buf
	gzPool.Put(zw)
	return buf.Bytes()
}

func gzipAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 附件原样发：图片本来就压过，而且要支持 Range（http.ServeContent）
		if !strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/api/uploads/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		if !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.finish()
		next.ServeHTTP(gw, r)
	})
}

// gzipWriter 先攒着响应的开头，够 gzMinSize 才开始压；状态码也跟着推迟到那时再发。
// handler 自己设了 Content-Encoding 的（已经压好的缓存）原样透传。
type gzipWriter struct {
	http.ResponseWriter
	status int
	buf    []byte
	zw     *gzip.Writer
	raw    bool
}

func (g *gzipWriter) WriteHeader(code int) {
	if g.status == 0 {
		g.status = code
	}
}

func (g *gzipWriter) Write(p []byte) (int, error) {
	switch {
	case g.zw != nil:
		return g.zw.Write(p)
	case g.raw:
		return g.ResponseWriter.Write(p)
	case g.Header().Get("Content-Encoding") != "":
		if err := g.sendRaw(); err != nil {
			return 0, err
		}
		return g.ResponseWriter.Write(p)
	}
	g.buf = append(g.buf, p...)
	if len(g.buf) < gzMinSize {
		return len(p), nil
	}
	h := g.Header()
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	g.ResponseWriter.WriteHeader(g.code())
	g.zw = gzPool.Get().(*gzip.Writer)
	g.zw.Reset(g.ResponseWriter)
	buf := g.buf
	g.buf = nil
	if _, err := g.zw.Write(buf); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (g *gzipWriter) code() int {
	if g.status == 0 {
		return http.StatusOK
	}
	return g.status
}

func (g *gzipWriter) sendRaw() error {
	g.raw = true
	g.ResponseWriter.WriteHeader(g.code())
	if len(g.buf) == 0 {
		return nil
	}
	_, err := g.ResponseWriter.Write(g.buf)
	g.buf = nil
	return err
}

func (g *gzipWriter) finish() {
	switch {
	case g.zw != nil:
		g.zw.Close()
		g.zw.Reset(io.Discard)
		gzPool.Put(g.zw)
	case !g.raw:
		g.sendRaw() // 没攒够的小响应、304、没有正文的，原样发
	}
}
