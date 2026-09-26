package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// reclaude：claude 的一层外壳（本地 daemon 代理流量、管账号和组织）。不认识的参数原样转给 claude，
// 所以 `reclaude --print ...` 和 `claude --print ...` 是一回事，stdout 也一样干净（它自己的提示走 stderr）。
//
// 设置页能打开「用 reclaude 代替 claude」：网页对话、网页终端、命令菜单里起 claude 的地方都换成 reclaude，
// 参数不变。开关存在 $RCWEB_STATE_DIR/settings.json，只影响之后新起的进程。
// 设置页还能看它的状态、切组织（reclaude org）、选网关（reclaude config gateway）。
//
// 小心：reclaude 不认识的子命令会整条当成 claude 的参数 —— `reclaude org use --help` 就成了让 claude
// 回答「org use --help」。所以这里只拼固定的几种形式，参数先校验、不许以 - 开头；stdin 给空，
// 交互式的（不带子命令的 `config gateway` 会等你选编号）读到 EOF 就退出，不会卡住。

// Settings 是设置页里存在 VPS 上的那些（$RCWEB_STATE_DIR/settings.json），所有浏览器共用
type Settings struct {
	UseReclaude   bool                  `json:"useReclaude"`
	RemoteControl *RemoteControl        `json:"remoteControl,omitempty"` // nil = 没设过，用 defaultRC（见 remote_control.go）
	Chat          map[string]*ChatPrefs `json:"chat,omitempty"`          // 按 CLI：新对话的默认模型 / effort / 权限模式（见 prefs.go）
	Notify        *NotifyConfig         `json:"notify,omitempty"`        // 通知发给谁、发哪些（见 notify.go）
}

type settingsStore struct {
	mu   sync.Mutex
	path string
	v    Settings
}

func newSettingsStore(dir string) *settingsStore {
	st := &settingsStore{path: filepath.Join(dir, "settings.json")}
	if data, err := os.ReadFile(st.path); err == nil {
		_ = json.Unmarshal(data, &st.v)
	}
	return st
}

func (st *settingsStore) get() Settings {
	if st == nil {
		return Settings{}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.v
}

func (st *settingsStore) update(fn func(*Settings)) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	next := st.v
	fn(&next)
	if err := os.MkdirAll(filepath.Dir(st.path), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(next, "", "  ")
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, st.path); err != nil {
		return err
	}
	st.v = next
	return nil
}

// claudeBin：起 claude 时实际执行的程序
func (s *Server) claudeBin() string {
	if s.settings.get().UseReclaude {
		if p, ok := s.reclaudePath(); ok {
			return p
		}
		return s.cfg.ReclaudeBin // 开着但找不到：照样用它，启动失败的报错比悄悄换回 claude 好懂
	}
	return s.cfg.ClaudeBin
}

// reclaudePath：先按 RCWEB_RECLAUDE_BIN / PATH 找，再找 reclaude setup 默认装的 ~/.local/bin
func (s *Server) reclaudePath() (string, bool) {
	if p, err := exec.LookPath(s.cfg.ReclaudeBin); err == nil {
		return p, true
	}
	if s.cfg.ReclaudeBin == "reclaude" {
		home, _ := os.UserHomeDir()
		if p, err := exec.LookPath(filepath.Join(home, ".local", "bin", "reclaude")); err == nil {
			return p, true
		}
	}
	return "", false
}

// config 子命令会先「Syncing config…」（顺带修 ~/.claude 里的登录凭据），慢的时候要四五十秒，其余几条一两秒。所以：
//   - 超时给足；到点先发 SIGTERM 再等一会儿，别让它写凭据写到一半
//   - 不跟着请求走：页面关了、刷新了，已经开始的照样跑完
//   - config 和会改状态的命令一次只跑一条，免得两次同步撞在一起
const (
	reclaudeTimeout     = 90 * time.Second
	reclaudeSlowTimeout = 3 * time.Minute // gateway test / set 要挨个连网关，前面还可能先同步一遍
)

// runReclaude 跑一条 reclaude 管理命令，返回 stdout 和去掉进度提示的 stderr。
// 各命令往哪边打不一致：status、org 的结果在 stdout，gateway test 的结果却在 stderr。失败时错误信息优先取 stderr
func (s *Server) runReclaude(timeout time.Duration, args ...string) (stdout, stderr string, err error) {
	bin, ok := s.reclaudePath()
	if !ok {
		return "", "", errors.New("VPS 上找不到 reclaude")
	}
	if args[0] == "config" || (args[0] == "org" && len(args) > 1) {
		s.reclaudeMu.Lock()
		defer s.reclaudeMu.Unlock()
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = s.cfg.Root
	cmd.Env = childEnv()
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	err = cmd.Run()
	stdout = strings.TrimSpace(outBuf.String())
	stderr = strings.TrimSpace(reclaudeNoise.ReplaceAllString(errBuf.String(), ""))
	switch {
	case err == nil:
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		err = fmt.Errorf("reclaude %s 超过 %s 没返回", strings.Join(args, " "), timeout)
	case stderr != "":
		err = errors.New(stderr)
	case stdout != "":
		err = errors.New(stdout)
	}
	return stdout, stderr, err
}

// 每次都可能打的进度提示，不算错误信息
var reclaudeNoise = regexp.MustCompile(`(?m)^Syncing config…\s*$\n?`)

type reclaudeOrg struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type,omitempty"`
	Email   string `json:"email,omitempty"`
	Current bool   `json:"current"`
}

var orgLineRe = regexp.MustCompile(`^([* ]) (\S+)\t(.*)$`)

// parseOrgs 解析 `reclaude org`：
//
//	Available organizations:
//	* 4216	name	personal	me@example.com
//	  1694	Team X	team	boss@example.com
func parseOrgs(out string) []reclaudeOrg {
	orgs := []reclaudeOrg{}
	for _, line := range strings.Split(out, "\n") {
		m := orgLineRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		f := strings.Split(m[3], "\t")
		o := reclaudeOrg{ID: m[2], Name: f[0], Current: m[1] == "*"}
		if len(f) > 1 {
			o.Type = f[1]
		}
		if len(f) > 2 {
			o.Email = f[2]
		}
		orgs = append(orgs, o)
	}
	return orgs
}

type kv struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// parseKV 解析 `reclaude status` 和 `config gateway current` 那种「key: value」一行一项的输出，保持原顺序
func parseKV(out string) []kv {
	list := []kv{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		k = strings.TrimSpace(k)
		if !ok || k == "" || strings.ContainsAny(k, " \t") {
			continue
		}
		list = append(list, kv{Key: k, Value: strings.TrimSpace(v)})
	}
	return list
}

type gatewayResult struct {
	Status string `json:"status"` // OK 之外都算不通
	OK     bool   `json:"ok"`
	URL    string `json:"url"`
	Detail string `json:"detail"` // 通的是耗时，不通的是原因
}

var gatewayLineRe = regexp.MustCompile(`^(\S+)\s+(?:\d+\.\s+)?(https?://\S+)\s*(.*)$`)

// parseGatewayTest 解析 `reclaude config gateway test [url]`。测全部是一行一个网关（已按耗时排好），
// 只测一个地址是另一种写法：
//
//	OK      3. https://la.route.reclaude.ai           765ms
//	ok: https://la.route.reclaude.ai (1.747s)
//	fail: https://x.example.com (Post "https://x.example.com/proxy": EOF)
func parseGatewayTest(out string) []gatewayResult {
	list := []gatewayResult{}
	for _, line := range strings.Split(out, "\n") {
		m := gatewayLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		status := strings.ToUpper(strings.TrimSuffix(m[1], ":"))
		detail := strings.TrimSpace(m[3])
		if len(detail) >= 2 && detail[0] == '(' && detail[len(detail)-1] == ')' {
			detail = detail[1 : len(detail)-1]
		}
		list = append(list, gatewayResult{Status: status, OK: status == "OK", URL: m[2], Detail: detail})
	}
	return list
}

// GET /api/reclaude：只答不用跑 reclaude 就知道的（装没装、开没开）和版本号，页面据此先把开关画出来；
// 状态、网关、组织各自一个接口，页面分开取，慢的那块（多半是网关，要同步配置）不拖住别的
type reclaudeInfo struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Enabled   bool   `json:"enabled"`
	Version   string `json:"version,omitempty"`
}

func (s *Server) handleReclaude(w http.ResponseWriter, r *http.Request) {
	info := reclaudeInfo{Enabled: s.settings.get().UseReclaude}
	info.Path, info.Installed = s.reclaudePath()
	if info.Installed {
		info.Version, _, _ = s.runReclaude(10*time.Second, "version")
	}
	writeJSON(w, info)
}

// reclaudeRead 跑一条只读的命令，stdout 解析成 key 下的内容
func (s *Server) reclaudeRead(w http.ResponseWriter, key string, parse func(string) any, args ...string) {
	out, _, err := s.runReclaude(reclaudeTimeout, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{key: parse(out)})
}

func (s *Server) handleReclaudeStatus(w http.ResponseWriter, r *http.Request) {
	s.reclaudeRead(w, "status", func(out string) any { return parseKV(out) }, "status")
}

func (s *Server) handleReclaudeGateway(w http.ResponseWriter, r *http.Request) {
	s.reclaudeRead(w, "gateway", func(out string) any { return parseKV(out) }, "config", "gateway", "current")
}

func (s *Server) handleReclaudeOrgs(w http.ResponseWriter, r *http.Request) {
	s.reclaudeRead(w, "orgs", func(out string) any { return parseOrgs(out) }, "org")
}

func (s *Server) handleReclaudeEnabled(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	if _, ok := s.reclaudePath(); body.Enabled && !ok {
		writeErr(w, http.StatusBadRequest, "VPS 上找不到 reclaude，先装上再打开")
		return
	}
	if err := s.settings.update(func(v *Settings) { v.UseReclaude = body.Enabled }); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"enabled": body.Enabled})
}

var orgIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func (s *Server) handleReclaudeOrg(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || !orgIDRe.MatchString(body.ID) {
		writeErr(w, http.StatusBadRequest, "组织 ID 不合法")
		return
	}
	s.reclaudeAction(w, reclaudeTimeout, "org", "use", body.ID)
}

// 网关地址：只收 http(s)，不带空白（exec 不过 shell，挡的是被 reclaude 当成别的参数）
func validGatewayURL(raw string) bool {
	if raw == "" || len(raw) > 512 || strings.ContainsAny(raw, " \t\r\n") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

func (s *Server) handleReclaudeGatewayTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	args := []string{"config", "gateway", "test"}
	if body.URL = strings.TrimSpace(body.URL); body.URL != "" {
		if !validGatewayURL(body.URL) {
			writeErr(w, http.StatusBadRequest, "网关地址要是 http(s):// 开头的完整地址")
			return
		}
		args = append(args, body.URL)
	}
	// 结果打在 stderr 上，两边都看
	stdout, stderr, err := s.runReclaude(reclaudeSlowTimeout, args...)
	out := strings.TrimSpace(stdout + "\n" + stderr)
	results := parseGatewayTest(out)
	if err != nil && len(results) == 0 {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"results": results, "output": out})
}

func (s *Server) handleReclaudeGatewaySet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL   string `json:"url"`
		Force bool   `json:"force"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	body.URL = strings.TrimSpace(body.URL)
	if !validGatewayURL(body.URL) {
		writeErr(w, http.StatusBadRequest, "网关地址要是 http(s):// 开头的完整地址")
		return
	}
	args := []string{"config", "gateway", "set"}
	if body.Force {
		args = append(args, "--force")
	}
	s.reclaudeAction(w, reclaudeSlowTimeout, append(args, body.URL)...)
}

func (s *Server) handleReclaudeGatewayReset(w http.ResponseWriter, r *http.Request) {
	s.reclaudeAction(w, reclaudeTimeout, "config", "gateway", "reset")
}

// reclaudeAction 跑一条会改状态的命令，把它说的话原样带回页面（stdout 没有就用 stderr）
func (s *Server) reclaudeAction(w http.ResponseWriter, timeout time.Duration, args ...string) {
	stdout, stderr, err := s.runReclaude(timeout, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if stdout == "" {
		stdout = stderr
	}
	writeJSON(w, map[string]string{"output": stdout})
}
