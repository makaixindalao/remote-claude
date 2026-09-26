package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pngBytes(t *testing.T, w, h int) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// upload 按页面的方式传一个文件（可选带缩图），返回应答
func upload(t *testing.T, s *Server, name string, data, vision []byte) (*httptest.ResponseRecorder, Upload) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(data)
	if vision != nil {
		vw, _ := mw.CreateFormFile("vision", "vision")
		vw.Write(vision)
	}
	mw.Close()
	r := httptest.NewRequest("POST", "/api/uploads", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.handleUpload(rec, r)
	var u Upload
	json.Unmarshal(rec.Body.Bytes(), &u)
	return rec, u
}

func uploadServer(t *testing.T) *Server {
	t.Setenv("RCWEB_STATE_DIR", t.TempDir())
	return &Server{cfg: &Config{}}
}

func TestUploadImageAndFile(t *testing.T) {
	s := uploadServer(t)

	rec, img := upload(t, s, "截图.png", pngBytes(t, 40, 30), nil)
	if rec.Code != 200 || !img.Image || img.Name != "截图.png" || !strings.HasPrefix(img.Path, uploadDir()) {
		t.Fatalf("小图: %d %s %+v", rec.Code, rec.Body, img)
	}

	// 长边超了、没带缩图：只能当文件，给路径
	_, big := upload(t, s, "big.png", pngBytes(t, maxVisionEdge+1, 10), nil)
	if big.Image {
		t.Errorf("超尺寸的原图不该直接给模型看: %+v", big)
	}
	// 带了缩图：给模型看缩图，路径还是原图
	_, scaled := upload(t, s, "big.png", pngBytes(t, maxVisionEdge+1, 10), pngBytes(t, 200, 1))
	u, err := loadUpload(scaled.ID)
	if err != nil || !u.Image || u.vision == u.Path || filepath.Ext(u.vision) != ".png" || filepath.Base(u.Path) != "big.png" {
		t.Errorf("缩图: %+v %v", u, err)
	}
	// 缩图不合格（不是图片）就扔掉
	_, junk := upload(t, s, "photo.heic", []byte("not an image"), []byte("<html>"))
	if junk.Image {
		t.Errorf("不合格的缩图被收下了: %+v", junk)
	}

	_, log := upload(t, s, "../../etc/server.log", []byte("hello\n"), nil)
	if log.Image || log.Name != "server.log" || filepath.Dir(filepath.Dir(log.Path)) != uploadDir() {
		t.Errorf("文件名要去掉目录: %+v", log)
	}
	ups, err := loadUploads([]string{img.ID, log.ID, img.ID})
	if err != nil || len(ups) != 2 {
		t.Fatalf("loadUploads: %v %v", ups, err)
	}
	if _, err := loadUploads([]string{"../../etc"}); err == nil {
		t.Error("不合法的 id 没拦住")
	}
	if _, err := loadUploads([]string{"0123456789abcdef"}); err == nil {
		t.Error("不存在的附件没报错")
	}
}

func TestUploadServeSafely(t *testing.T) {
	s := uploadServer(t)
	_, html := upload(t, s, "x.html", []byte("<html><script>alert(1)</script></html>"), nil)
	_, img := upload(t, s, "a.png", pngBytes(t, 4, 4), nil)

	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.SetPathValue("id", strings.Split(strings.TrimPrefix(path, "/api/uploads/"), "/")[0])
		rec := httptest.NewRecorder()
		s.handleUploadFile(rec, r)
		return rec
	}
	rec := get("/api/uploads/" + html.ID)
	if rec.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("HTML 要下载、沙箱化: %v", rec.Header())
	}
	if get("/api/uploads/"+html.ID+"/preview").Code != http.StatusNotFound {
		t.Error("不是图片没有 preview")
	}
	if rec := get("/api/uploads/" + img.ID + "/preview"); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("图片 preview: %d %v", rec.Code, rec.Header())
	}

	r := httptest.NewRequest("DELETE", "/api/uploads/"+img.ID, nil)
	r.SetPathValue("id", img.ID)
	s.handleUploadDelete(httptest.NewRecorder(), r)
	if _, err := os.Stat(img.Path); !os.IsNotExist(err) {
		t.Error("删了附件文件还在")
	}
}

func TestWithAttachments(t *testing.T) {
	ups := []*Upload{{Path: "/s/uploads/1/a.png", Image: true}, {Path: "/s/uploads/2/b c.log"}}
	got := withAttachments("看看这个\n", ups)
	want := "看看这个\n\n<attachments>\n- /s/uploads/1/a.png (image)\n- /s/uploads/2/b c.log\n</attachments>"
	if got != want {
		t.Fatalf("got %q", got)
	}
	if stripAttachments(got) != "看看这个" || stripAttachments(withAttachments("", ups)) != "" {
		t.Errorf("strip: %q", stripAttachments(got))
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"a.txt": "a.txt", `C:\x\报告.pdf`: "报告.pdf", "../../etc/passwd": "passwd", "..": "file", "": "file", "a\x00\nb.md": "ab.md",
	} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
	long := safeName(strings.Repeat("长", 100) + ".png")
	if len(long) > 180 || !strings.HasSuffix(long, ".png") {
		t.Errorf("长文件名: %d %q", len(long), long)
	}
}

// claude：图片是 base64 的 image 块，放在文字前面；只能当文件的不带
func TestClaudePromptWithImages(t *testing.T) {
	s := uploadServer(t)
	_, img := upload(t, s, "a.png", pngBytes(t, 4, 4), nil)
	_, doc := upload(t, s, "a.pdf", []byte("%PDF-1.4"), nil)
	ups, _ := loadUploads([]string{img.ID, doc.ID})

	var buf bytes.Buffer
	c, d, _ := testClaudeChat(t)
	c.stdin = nopWriteCloser{&buf}
	if err := c.send("这是什么", ups, false); err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Message struct {
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source struct {
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(buf.Bytes(), &msg); err != nil {
		t.Fatal(err, buf.String())
	}
	ct := msg.Message.Content
	if len(ct) != 2 || ct[0].Type != "image" || ct[0].Source.MediaType != "image/png" || ct[0].Source.Data == "" || ct[1].Type != "text" {
		t.Fatalf("content: %+v", ct)
	}
	if !strings.Contains(ct[1].Text, img.Path+" (image)") || !strings.Contains(ct[1].Text, doc.Path+"\n") {
		t.Errorf("正文里要列出全部附件: %q", ct[1].Text)
	}
	// 页面上记的就是交给 CLI 的正文；标题只取用户打的字
	if e := c.entries[len(c.entries)-1]; e.Blocks[0].Text != ct[1].Text || c.title != "修 bug" {
		t.Errorf("entry: %+v title %q", e, c.title)
	}
	_ = d
}
