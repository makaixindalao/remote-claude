package main

import (
	"errors"
	"fmt"
	"image"
	_ "image/gif" // image.DecodeConfig 看尺寸用
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// 附件：输入框里粘贴、拖进来、点回形针选的文件和图片。
//
// 浏览器先把文件传上来（POST /api/uploads，一次一个），存成 <state>/uploads/<id>/<原文件名>，发消息时只带 id。
// 交给 CLI 时，消息末尾附一段 <attachments> 列出每个文件在服务器上的路径，CLI 要用就自己去读；
// 图片另外直接给模型看：claude 是消息里的 image 块（base64），codex 是 localImage，grok 不收图片（只给路径）。
//
// 模型看的图有限制（单张 base64 不超过 5MB；对话里图多了长边不能超过 2000）。原图太大、或者是模型不认的格式
// （HEIC 之类）时，浏览器顺带传一份缩好的，存成 <id>.vision.<扩展名>。原图照样留着，给 CLI 的路径是原图。
// 放 30 天后删掉。

const (
	maxUploadBytes = 100 << 20
	maxVisionBytes = 5 << 20 * 3 / 4 // base64 之后正好 5MB
	maxVisionEdge  = 2000
	maxAttachments = 20
	uploadTTL      = 30 * 24 * time.Hour
)

var (
	uploadIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
	// 模型能直接看的图片格式 → 缩图的扩展名
	visionTypes = map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp"}
)

type Upload struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Path  string `json:"path"`  // 服务器上的绝对路径，给 CLI 的就是它
	Image bool   `json:"image"` // 模型能直接看：原图或缩图有一份合格的

	vision string // 给模型看的那份（缩图，或者原图本身）
	mime   string // vision 的类型
}

func uploadDir() string { return filepath.Join(stateDir(), "uploads") }

// loadUpload 按 id 找回传上来的文件。目录里只有原文件一个
func loadUpload(id string) (*Upload, error) {
	if !uploadIDRe.MatchString(id) {
		return nil, errors.New("附件 id 不对")
	}
	dir := filepath.Join(uploadDir(), id)
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) != 1 || !ents[0].Type().IsRegular() {
		return nil, errors.New("附件不在了（超过 30 天会被清掉），重新添加一次")
	}
	path := filepath.Join(dir, ents[0].Name())
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	u := &Upload{ID: id, Name: ents[0].Name(), Size: fi.Size(), Path: path}
	cands, _ := filepath.Glob(filepath.Join(uploadDir(), id+".vision.*"))
	for _, p := range append(cands, path) {
		if m, ok := visionOK(p); ok {
			u.vision, u.mime, u.Image = p, m, true
			break
		}
	}
	return u, nil
}

// loadUploads：一条消息带的全部附件，重复的只算一次
func loadUploads(ids []string) ([]*Upload, error) {
	if len(ids) > maxAttachments {
		return nil, fmt.Errorf("一条消息最多带 %d 个附件", maxAttachments)
	}
	var out []*Upload
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		u, err := loadUpload(id)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// visionOK：这个文件能不能直接给模型看 —— 格式、体积、尺寸（webp 标准库读不了尺寸，靠浏览器把关）
func visionOK(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 || fi.Size() > maxVisionBytes {
		return "", false
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	m := http.DetectContentType(head[:n])
	if visionTypes[m] == "" {
		return "", false
	}
	if m != "image/webp" {
		f.Seek(0, io.SeekStart)
		cfg, _, err := image.DecodeConfig(f)
		if err != nil || cfg.Width > maxVisionEdge || cfg.Height > maxVisionEdge {
			return "", false
		}
	}
	return m, true
}

// withAttachments：给 CLI 的正文 = 用户打的字 + 附件路径。图片标上 (image)，页面据此显示成缩略图（见 ui 的 lib/attachments.ts）
func withAttachments(text string, ups []*Upload) string {
	var b strings.Builder
	if t := strings.TrimRight(text, " \t\r\n"); t != "" {
		b.WriteString(t + "\n\n")
	}
	b.WriteString("<attachments>\n")
	for _, u := range ups {
		b.WriteString("- " + u.Path)
		if u.Image {
			b.WriteString(" (image)")
		}
		b.WriteString("\n")
	}
	b.WriteString("</attachments>")
	return b.String()
}

var attachmentsRe = regexp.MustCompile(`\s*<attachments>\n[\s\S]*?\n</attachments>\s*`)

// stripAttachments：会话标题之类只要用户打的字
func stripAttachments(s string) string { return attachmentsRe.ReplaceAllString(s, "") }

// safeName：只留文件名本身，去掉控制字符；太长的从中间截，保住扩展名
func safeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' {
			return -1
		}
		return r
	}, name))
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	if len(name) > 180 {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		stem := name[:180-len(ext)]
		for !utf8.ValidString(stem) {
			stem = stem[:len(stem)-1]
		}
		name = stem + ext
	}
	return name
}

// saveUploadPart 把一段上传写进 path，超过 limit 就删掉报错
func saveUploadPart(path string, r io.Reader, limit int64) (int64, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(r, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = fmt.Errorf("文件太大（上限 %d MB）", limit>>20)
	}
	if err != nil {
		os.Remove(path)
		return 0, err
	}
	return n, nil
}

// POST /api/uploads（multipart）：file 是原文件；vision 可选，是浏览器缩好的、给模型看的那份，必须在 file 后面
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+maxVisionBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	id := newID()
	dir := filepath.Join(uploadDir(), id)
	fail := func(err error) {
		os.RemoveAll(dir)
		if vs, _ := filepath.Glob(filepath.Join(uploadDir(), id+".vision.*")); len(vs) > 0 {
			os.Remove(vs[0])
		}
		code := http.StatusBadRequest
		if tooBig(err) {
			code, err = http.StatusRequestEntityTooLarge, fmt.Errorf("文件太大（上限 %d MB）", maxUploadBytes>>20)
		}
		writeErr(w, code, err.Error())
	}
	var saved bool
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(err)
			return
		}
		switch {
		case p.FormName() == "file" && !saved:
			if err := os.MkdirAll(dir, 0o700); err != nil {
				fail(err)
				return
			}
			if _, err := saveUploadPart(filepath.Join(dir, safeName(p.FileName())), p, maxUploadBytes); err != nil {
				fail(err)
				return
			}
			saved = true
		case p.FormName() == "vision" && saved:
			// 先落成临时文件，看过类型再起名；不合格的缩图扔掉就是，原图照样能用
			tmp := filepath.Join(uploadDir(), id+".vision-tmp")
			if _, err := saveUploadPart(tmp, p, maxVisionBytes); err != nil {
				if tooBig(err) {
					fail(err)
					return
				}
				break
			}
			if m, ok := visionOK(tmp); ok {
				os.Rename(tmp, filepath.Join(uploadDir(), id+".vision."+visionTypes[m]))
			} else {
				os.Remove(tmp)
			}
		}
		p.Close()
	}
	if !saved {
		fail(errors.New("没收到文件"))
		return
	}
	u, err := loadUpload(id)
	if err != nil {
		fail(err)
		return
	}
	writeJSON(w, u)
}

func tooBig(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

// GET /api/uploads/{id}：原文件。图片、纯文本在页面里直接看，别的一律下载，
// 而且整页沙箱化 —— 传上来的 HTML / SVG 不能借这个域名跑脚本
// GET /api/uploads/{id}/preview：给模型看的那份（一定是浏览器能显示的图），对话里的缩略图用
func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	u, err := loadUpload(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	path := u.Path
	if strings.HasSuffix(r.URL.Path, "/preview") {
		if u.vision == "" {
			writeErr(w, http.StatusNotFound, "不是图片")
			return
		}
		path = u.vision
	}
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "附件不在了")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	ctype := http.DetectContentType(head[:n])
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox")
	h.Set("Cache-Control", "private, max-age=604800, immutable")
	switch {
	case visionTypes[ctype] != "":
		h.Set("Content-Type", ctype)
	case strings.HasPrefix(ctype, "text/plain"):
		h.Set("Content-Type", "text/plain; charset=utf-8")
	default:
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": u.Name}))
	}
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// DELETE /api/uploads/{id}：输入框里把附件去掉了（还没发出去），不留在服务器上
func (s *Server) handleUploadDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uploadIDRe.MatchString(id) {
		writeErr(w, http.StatusBadRequest, "附件 id 不对")
		return
	}
	os.RemoveAll(filepath.Join(uploadDir(), id))
	vs, _ := filepath.Glob(filepath.Join(uploadDir(), id+".vision.*"))
	for _, v := range vs {
		os.Remove(v)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// reapUploads：定期删掉放了 ttl 以上的附件（目录的时间就是上传的时间，之后不会再变）
func reapUploads(dir string, ttl time.Duration) {
	for {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			fi, err := e.Info()
			if err == nil && time.Since(fi.ModTime()) > ttl {
				if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
					log.Printf("清理附件 %s: %v", e.Name(), err)
				}
			}
		}
		time.Sleep(6 * time.Hour)
	}
}
