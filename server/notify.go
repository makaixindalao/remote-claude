package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// 通知：对话完成了、等你回答（批准工具、回答提问、审批计划）、出错了。由服务端发，所以页面关着、手机锁屏也能收到。
// 三条路，配了哪条走哪条：
//   - Web Push：浏览器里订阅（见 webpush.go）。要 https 或 localhost，iPhone 还要先把网页加到主屏幕
//   - ntfy：手机装 ntfy App 订阅一个主题，这里填主题地址。http 打开的 rcweb 也能用
//   - Bark：iPhone 装 Bark，这里填它给的推送地址
// 有人正开着这个对话的页面（在前台）就不发（见 Chat.noticeLocked）。设置存在 settings.json 的 notify 下。

type Notice struct {
	Kind  string // done | waiting | error
	Title string
	Body  string
	Chat  string // 对话 ID：点通知打开它
}

var noticeKinds = []string{"done", "waiting", "error"}

// NotifyConfig：Off 里是不发的种类，默认全发
type NotifyConfig struct {
	Off       []string `json:"off,omitempty"`
	Ntfy      string   `json:"ntfy,omitempty"`      // 如 https://ntfy.sh/my-topic
	Bark      string   `json:"bark,omitempty"`      // 如 https://api.day.app/<key>
	PublicURL string   `json:"publicUrl,omitempty"` // 点 ntfy / Bark 的通知打开哪里：设置页保存时记下浏览器地址栏里的
}

func agentName(a string) string {
	switch a {
	case agentCodex:
		return "Codex"
	case agentGrok:
		return "Grok"
	}
	return "Claude"
}

func pendingTitle(who string, p *PermissionReq) string {
	switch p.Tool {
	case "AskUserQuestion":
		return who + " 有问题问你"
	case "ExitPlanMode":
		return who + " 等你审批计划"
	}
	return who + " 等你批准 " + p.Tool
}

// pendingText：通知正文里写它要干什么（命令、文件、问题）
func pendingText(p *PermissionReq) string {
	var in map[string]any
	_ = json.Unmarshal(p.Input, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	switch p.Tool {
	case "AskUserQuestion":
		if qs, ok := in["questions"].([]any); ok && len(qs) > 0 {
			if q, ok := qs[0].(map[string]any); ok {
				s, _ := q["question"].(string)
				return oneLine(s, 160)
			}
		}
	case "ExitPlanMode":
		return oneLine(str("plan"), 160)
	}
	for _, k := range []string{"command", "file_path", "path", "url", "pattern"} {
		if s := str(k); s != "" {
			return oneLine(s, 160)
		}
	}
	return oneLine(p.Description, 160)
}

// notify 按设置把一条通知发到各条路上
func (s *Server) notify(n Notice) {
	cfg := s.notifyConfig()
	if slices.Contains(cfg.Off, n.Kind) {
		return
	}
	for _, err := range s.sendNotice(n, cfg, "") {
		log.Printf("通知没发出去: %v", err)
	}
}

func (s *Server) notifyConfig() NotifyConfig {
	if c := s.settings.get().Notify; c != nil {
		return *c
	}
	return NotifyConfig{}
}

// sendNotice 同时发给所有订阅的浏览器和配了的 App；only 非空就只发这一条路（设置页的「试一下」）
func (s *Server) sendNotice(n Notice, cfg NotifyConfig, only string) []error {
	path := "/"
	if n.Chat != "" {
		path = "/#/c/" + n.Chat
	}
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	send := func(name string, f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f(); err != nil && !errors.Is(err, errPushGone) {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				mu.Unlock()
			}
		}()
	}
	if only == "" || strings.HasPrefix(only, "https://") {
		payload, _ := json.Marshal(map[string]string{"title": n.Title, "body": n.Body, "tag": "rcweb-" + n.Chat, "url": path, "kind": n.Kind})
		urgency := "high"
		if n.Kind == "done" {
			urgency = "normal"
		}
		for _, sub := range s.push.list() {
			if only == "" || only == sub.Endpoint {
				send("浏览器推送", func() error { return s.push.send(sub, payload, urgency, n.Chat) })
			}
		}
	}
	link := ""
	if cfg.PublicURL != "" {
		link = strings.TrimRight(cfg.PublicURL, "/") + path
	}
	if cfg.Ntfy != "" && (only == "" || only == "ntfy") {
		send("ntfy", func() error { return sendNtfy(cfg.Ntfy, n, link, cfg.PublicURL) })
	}
	if cfg.Bark != "" && (only == "" || only == "bark") {
		send("Bark", func() error { return sendBark(cfg.Bark, n, link) })
	}
	wg.Wait()
	return errs
}

// sendNtfy：用 JSON 发（标题里有中文，走 header 要另外编码）。JSON 要发到服务器根路径，主题放在 body 里
func sendNtfy(topicURL string, n Notice, link, publicURL string) error {
	u, err := url.Parse(topicURL)
	if err != nil {
		return err
	}
	path := strings.TrimRight(u.Path, "/")
	i := strings.LastIndex(path, "/")
	msg := map[string]any{"topic": path[i+1:], "title": n.Title, "message": n.Body, "priority": 4}
	if n.Kind == "done" {
		msg["priority"] = 3
	}
	if link != "" {
		msg["click"] = link
	}
	if publicURL != "" {
		msg["icon"] = strings.TrimRight(publicURL, "/") + "/icon-192.png"
	}
	u.Path = path[:i] + "/"
	return postJSON(u.String(), msg)
}

// sendBark：POST <服务器>/push，device_key 是地址里的第一段
func sendBark(barkURL string, n Notice, link string) error {
	u, err := url.Parse(barkURL)
	if err != nil {
		return err
	}
	key, _, _ := strings.Cut(strings.Trim(u.Path, "/"), "/")
	msg := map[string]any{"device_key": key, "title": n.Title, "body": n.Body, "group": "rcweb"}
	if n.Kind != "done" {
		msg["level"] = "timeSensitive" // 专注模式下也提醒
	}
	if link != "" {
		msg["url"] = link
	}
	return postJSON(u.Scheme+"://"+u.Host+"/push", msg)
}

func postJSON(u string, v any) error {
	body, _ := json.Marshal(v)
	resp, err := pushClient.Post(u, "application/json; charset=utf-8", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// validPushURL：ntfy / Bark 的地址得是 http(s)，而且带着主题 / key
func validPushURL(s string) bool {
	if s == "" {
		return true
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && strings.Trim(u.Path, "/") != ""
}

// ---- HTTP ----

func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	key, err := s.push.publicKey()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"config": s.notifyConfig(), "publicKey": key, "subscriptions": len(s.push.list())})
}

func (s *Server) handleNotifySave(w http.ResponseWriter, r *http.Request) {
	var c NotifyConfig
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&c); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	c.Ntfy, c.Bark, c.PublicURL = strings.TrimSpace(c.Ntfy), strings.TrimSpace(c.Bark), strings.TrimRight(strings.TrimSpace(c.PublicURL), "/")
	for _, k := range c.Off {
		if !slices.Contains(noticeKinds, k) {
			writeErr(w, http.StatusBadRequest, "不认识的通知种类 "+k)
			return
		}
	}
	if !validPushURL(c.Ntfy) || !validPushURL(c.Bark) {
		writeErr(w, http.StatusBadRequest, "推送地址要是 http(s)://服务器/主题 这样的")
		return
	}
	if u, err := url.Parse(c.PublicURL); c.PublicURL != "" && (err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "") {
		writeErr(w, http.StatusBadRequest, "打开地址不对")
		return
	}
	if err := s.settings.update(func(v *Settings) { v.Notify = &c }); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, c)
}

// handleNotifyTest：设置页的「试一下」。channel 是 ntfy / bark，或者浏览器订阅的 endpoint；失败把原因带回去
func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Channel string `json:"channel"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.Channel == "" {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	if strings.HasPrefix(body.Channel, "https://") && !s.push.has(body.Channel) {
		writeErr(w, http.StatusNotFound, "服务器上没有这个浏览器的订阅，关掉再打开试试")
		return
	}
	n := Notice{Kind: "waiting", Title: "rcweb 通知试一下", Body: "对话完成、等你回答、出错时会这样提醒你"}
	if errs := s.sendNotice(n, s.notifyConfig(), body.Channel); len(errs) > 0 {
		writeErr(w, http.StatusBadGateway, errs[0].Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
		Origin string `json:"origin"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	if u, err := url.Parse(body.Endpoint); err != nil || u.Scheme != "https" || body.Keys.P256dh == "" || body.Keys.Auth == "" {
		writeErr(w, http.StatusBadRequest, "订阅信息不完整")
		return
	}
	sub := pushSub{Endpoint: body.Endpoint, P256dh: body.Keys.P256dh, Auth: body.Keys.Auth, Origin: body.Origin, Created: time.Now().UnixMilli()}
	if err := s.push.add(sub); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	if err := s.push.remove(body.Endpoint); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
