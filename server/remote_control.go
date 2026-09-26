package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Remote Control（claude rc）：`claude remote-control` 常驻在这台机器上，手机上的 Claude App、
// claude.ai/code 就能连过来开会话、在这里干活。
//
// 设置页「Remote Control」打开后，rcweb 把一个守护脚本写到 $RCWEB_STATE_DIR/claude-rc.sh
// （cd 到选的项目目录，带上页面上选的参数；可选退出后隔几秒重启），放进 tmux 会话 claude-rc 里跑。
// 放 tmux 里是因为：它第一次跑、换了目录时会在终端里问问题（开不开 Remote Control、信不信任这个目录），
// 网页终端接上去就能答；而且和别的 tmux 会话一样，重启 rcweb 不会带走它（见 tmuxCreate）。
// rcweb 起来时发现开关开着、会话却不在（VPS 重启过），会自己再拉起来。关掉开关 = 结束这个 tmux 会话。
//
// 起的是 s.claudeBin()：打开了 reclaude 时跑的就是 reclaude remote-control。
// 改设置（包括 reclaude 开关）要「保存并重启」才作用到正在跑的这个。

const rcSession = "claude-rc"

type RemoteControl struct {
	Enabled        bool   `json:"enabled"`
	Project        string `json:"project"`        // 在哪个项目目录里跑，空 = RCWEB_ROOT
	Name           string `json:"name"`           // --name：claude.ai/code 里显示的名字，空 = 它自己起
	PermissionMode string `json:"permissionMode"` // --permission-mode：它开出来的会话用什么权限模式
	Spawn          string `json:"spawn"`          // --spawn：same-dir / worktree / session
	Capacity       int    `json:"capacity"`       // --capacity：最多同时几个会话，0 = 默认（32）
	AutoRestart    bool   `json:"autoRestart"`    // 守护脚本：退出了隔 RestartDelay 秒再起
	RestartDelay   int    `json:"restartDelay"`   // 秒
}

var defaultRC = RemoteControl{PermissionMode: "default", Spawn: "same-dir", AutoRestart: true, RestartDelay: 10}

var rcSpawns = map[string]bool{"same-dir": true, "worktree": true, "session": true}

func (s *Server) rcConfig() RemoteControl {
	if rc := s.settings.get().RemoteControl; rc != nil {
		return *rc
	}
	return defaultRC
}

// rcDir：要跑的目录，项目不存在了就报错（不悄悄换到别处跑）
func (s *Server) rcDir(rc RemoteControl) (string, error) {
	dir := s.cfg.Root
	if rc.Project != "" {
		p, ok := s.projectPath(rc.Project)
		if !ok {
			return "", fmt.Errorf("没有项目 %s", rc.Project)
		}
		dir = p
	}
	if !isDir(dir) {
		return "", fmt.Errorf("目录不存在: %s", dir)
	}
	return dir, nil
}

func (s *Server) validateRC(rc *RemoteControl) error {
	rc.Name = strings.TrimSpace(rc.Name)
	if utf8.RuneCountInString(rc.Name) > 64 || strings.IndexFunc(rc.Name, unicode.IsControl) >= 0 {
		return errors.New("名字最多 64 个字，不能有控制字符")
	}
	if rc.PermissionMode == "" {
		rc.PermissionMode = "default"
	}
	if !permissionModes[rc.PermissionMode] {
		return fmt.Errorf("不认识的权限模式 %q", rc.PermissionMode)
	}
	if rc.Spawn == "" {
		rc.Spawn = "same-dir"
	}
	if !rcSpawns[rc.Spawn] {
		return fmt.Errorf("不认识的 spawn 模式 %q", rc.Spawn)
	}
	if rc.Capacity < 0 || rc.Capacity > 256 {
		return errors.New("并发会话数要在 1–256 之间（0 = 默认）")
	}
	if rc.RestartDelay < 1 || rc.RestartDelay > 3600 {
		rc.RestartDelay = defaultRC.RestartDelay
	}
	// 目录只在要起的时候查：项目删了也得能把开关关掉
	if rc.Enabled {
		if _, err := s.rcDir(*rc); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) rcScriptPath() string { return filepath.Join(stateDir(), "claude-rc.sh") }

// rcScript 是 tmux 里跑的守护脚本
func (s *Server) rcScript(rc RemoteControl, dir string) string {
	args := []string{shq(s.claudeBin()), "remote-control"}
	if rc.Name != "" {
		args = append(args, "--name", shq(rc.Name))
	}
	if rc.PermissionMode != "default" {
		args = append(args, "--permission-mode", rc.PermissionMode)
	}
	if rc.Spawn != "same-dir" {
		args = append(args, "--spawn", rc.Spawn)
	}
	if rc.Capacity > 0 {
		args = append(args, "--capacity", strconv.Itoa(rc.Capacity))
	}
	cmd := strings.Join(args, " ")

	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "# 由 rcweb 生成于 %s（设置 → Remote Control），在 tmux 会话 %s 里跑。\n", time.Now().Format("2006-01-02 15:04:05"), rcSession)
	b.WriteString("# 改设置会重写这个文件，直接改它下次就没了。\n")
	fmt.Fprintf(&b, "cd %s || exit 1\n", shq(dir))
	b.WriteString("unset TMUX TMUX_PANE # 否则 claude 把颜色降到 256 色\n")
	// claude 不让 root bypass 权限，和 scc 一样用 IS_SANDBOX=1 绕过（见 docs/03-scc.md）
	if rc.PermissionMode == "bypassPermissions" && os.Geteuid() == 0 {
		b.WriteString("export IS_SANDBOX=1\n")
	}
	if !rc.AutoRestart {
		fmt.Fprintf(&b, "exec %s\n", cmd)
		return b.String()
	}
	fmt.Fprintf(&b, "while :; do\n  %s\n  code=$?\n", cmd)
	// ${code} 的花括号不能省：紧跟着全角括号，有的 sh 会把那几个字节也当成变量名
	fmt.Fprintf(&b, "  echo \"[$(date '+%%F %%T')] claude remote-control 退出（${code}），%d 秒后重启。不想要就去设置里关掉\"\n", rc.RestartDelay)
	fmt.Fprintf(&b, "  sleep %d\ndone\n", rc.RestartDelay)
	return b.String()
}

// startRC 按当前设置（重新）起 tmux 会话。已经在跑的先结束
func (s *Server) startRC() error {
	rc := s.rcConfig()
	dir, err := s.rcDir(rc)
	if err != nil {
		return err
	}
	script := s.rcScriptPath()
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(script, []byte(s.rcScript(rc, dir)), 0o700); err != nil {
		return err
	}
	s.stopRC()
	// 不自动重启时，退出后留着窗格：页面上还能看到它最后说了什么、退出码是几。
	// 和建会话放在同一次调用里，一起来就失败退出（比如没登录）的也留得住
	var then []string
	if !rc.AutoRestart {
		then = []string{"set-option", "-t", "=" + rcSession + ":", "remain-on-exit", "on"}
	}
	return s.tmuxCreate(rcSession, dir, "sh "+shq(script), 120, 40, then...)
}

func (s *Server) stopRC() {
	if s.tmuxHas(rcSession) {
		_ = s.tmux("kill-session", "-t", "="+rcSession).Run()
	}
}

// rcMu 让起停一次只做一件：两个页面同时点保存，别建出两次会话
var rcMu sync.Mutex

// ensureRC：rcweb 起来时调一次。开着但会话不在就拉起来
func (s *Server) ensureRC() {
	rcMu.Lock()
	defer rcMu.Unlock()
	if !s.rcConfig().Enabled || s.tmuxHas(rcSession) {
		return
	}
	if err := s.startRC(); err != nil {
		log.Printf("Remote Control 没起来: %v", err)
		return
	}
	log.Printf("Remote Control 已在 tmux 会话 %s 里拉起", rcSession)
}

type rcStatus struct {
	Config  RemoteControl `json:"config"`
	State   string        `json:"state"` // stopped / running / restarting（守护脚本在等着重启）/ exited（退出了，没设自动重启）
	Exit    *int          `json:"exitCode,omitempty"`
	Session string        `json:"session"`
	Script  string        `json:"script"`
	Bin     string        `json:"bin"`    // 现在去起的话用哪个程序（claude 或 reclaude）
	Output  string        `json:"output"` // 窗格里最后几十行，Remote Control 的会话链接就在里面
}

func (s *Server) rcStatus() rcStatus {
	st := rcStatus{Config: s.rcConfig(), State: "stopped", Session: rcSession, Script: s.rcScriptPath(), Bin: s.claudeBin()}
	if !s.tmuxHas(rcSession) {
		return st
	}
	target := "=" + rcSession + ":"
	out, err := s.tmux("display-message", "-p", "-t", target, "#{pane_dead}\x1f#{pane_dead_status}\x1f#{pane_current_command}").Output()
	if err != nil {
		return st
	}
	f := strings.Split(strings.TrimSpace(string(out)), "\x1f")
	switch {
	case len(f) == 3 && f[0] == "1":
		st.State = "exited"
		if n, err := strconv.Atoi(f[1]); err == nil {
			st.Exit = &n
		}
	case len(f) == 3 && f[2] == "sleep":
		st.State = "restarting"
	default:
		st.State = "running"
	}
	// -J 把折行接回来，-S -60 往回多取一些；窗格底下没写到的是一大片空行，连着的空行并成一行
	if pane, err := s.tmux("capture-pane", "-p", "-J", "-t", target, "-S", "-60").Output(); err == nil {
		st.Output = strings.TrimSpace(blankRuns.ReplaceAllString(string(pane), "\n\n"))
	}
	return st
}

var blankRuns = regexp.MustCompile(`\n(?:[ \t]*\n){2,}`)

func (s *Server) handleRC(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.rcStatus())
}

// POST /api/rc：存设置。开着就按新设置重启，关了就结束
func (s *Server) handleRCSave(w http.ResponseWriter, r *http.Request) {
	var rc RemoteControl
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&rc); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	if err := s.validateRC(&rc); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rcMu.Lock()
	defer rcMu.Unlock()
	if err := s.settings.update(func(v *Settings) { v.RemoteControl = &rc }); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rc.Enabled {
		if err := s.startRC(); err != nil {
			writeErr(w, http.StatusInternalServerError, "设置存了，但没起来: "+err.Error())
			return
		}
	} else {
		s.stopRC()
	}
	writeJSON(w, s.rcStatus())
}

func (s *Server) handleRCRestart(w http.ResponseWriter, r *http.Request) {
	rcMu.Lock()
	defer rcMu.Unlock()
	if !s.rcConfig().Enabled {
		writeErr(w, http.StatusBadRequest, "Remote Control 没打开")
		return
	}
	if err := s.startRC(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, s.rcStatus())
}
