package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGzipAPI(t *testing.T) {
	big := strings.Repeat(`{"k":"v"},`, 500)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/big", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, big) })
	mux.HandleFunc("/api/small", func(w http.ResponseWriter, r *http.Request) { writeErr(w, http.StatusNotFound, "没有") })
	mux.HandleFunc("/api/pre", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(gzipBytes([]byte(big)))
	})
	mux.HandleFunc("/api/304", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotModified) })
	mux.HandleFunc("/other", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, big) })
	h := gzipAPI(mux)

	get := func(path, ae string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if ae != "" {
			req.Header.Set("Accept-Encoding", ae)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	gunzip := func(t *testing.T, b []byte) string {
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(zr)
		return string(out)
	}

	if rec := get("/api/big", "gzip, br"); rec.Header().Get("Content-Encoding") != "gzip" || gunzip(t, rec.Body.Bytes()) != big {
		t.Errorf("大响应应该压缩且内容不变，Content-Encoding=%q", rec.Header().Get("Content-Encoding"))
	}
	if rec := get("/api/big", ""); rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != big {
		t.Error("不支持 gzip 的客户端应该拿到原文")
	}
	if rec := get("/api/small", "gzip"); rec.Code != http.StatusNotFound || rec.Header().Get("Content-Encoding") != "" || !strings.Contains(rec.Body.String(), "没有") {
		t.Errorf("小响应原样发、状态码保留：%d %q", rec.Code, rec.Body.String())
	}
	if rec := get("/api/pre", "gzip"); gunzip(t, rec.Body.Bytes()) != big {
		t.Error("handler 自己压好的不能再压一遍")
	}
	if rec := get("/api/304", "gzip"); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("304 应该原样透传：%d", rec.Code)
	}
	if rec := get("/other", "gzip"); rec.Header().Get("Content-Encoding") != "" {
		t.Error("/api/ 之外的不归这里管")
	}
}

func TestSessionCacheEvicts(t *testing.T) {
	var c sessionCache
	for _, k := range []string{"a", "b", "c", "d", "e", "f"} {
		c.put(&sessionData{key: k, size: sessionCacheMax / 5})
	}
	if c.get("a") != nil || c.get("f") == nil || c.size > sessionCacheMax {
		t.Errorf("超出上限应该挤掉最久没用的：size=%d", c.size)
	}
	c.put(&sessionData{key: "huge", size: sessionCacheMax / 2})
	if c.get("huge") != nil {
		t.Error("单份太大的不该存")
	}
}

func TestETagMatch(t *testing.T) {
	for _, h := range []string{`"x"`, `"y", "x"`, `W/"x"`, `*`} {
		if !etagMatch(h, `"x"`) {
			t.Errorf("%s 应该匹配", h)
		}
	}
	if etagMatch(`"y"`, `"x"`) || etagMatch("", `"x"`) {
		t.Error("不该匹配")
	}
}
