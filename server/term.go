package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// 网页终端：WebSocket ↔ PTY ↔ `tmux attach`。
//
// 断线恢复交给 tmux —— 页面关了只是 tmux 客户端退出，会话和里面的 claude 照跑，
// 而且和 Mac 上 scc 接回的是同一个会话（默认名同样是 cc-<目录名>），两边看到的完全一样。
// codex / grok 的终端同理，会话名是 cx-<目录名> / gk-<目录名>（前端拼）。

type TmuxSession struct {
	Name     string `json:"name"`
	Attached int    `json:"attached"`
	Windows  int    `json:"windows"`
	Created  int64  `json:"created"`
	Activity int64  `json:"activity"`
	Path     string `json:"path"`
	Command  string `json:"command"` // 活动窗格里在跑的程序，界面据此标出哪些是 claude
}

// tmux 会话名不能含 . 和 :，也别以 - 开头（会被当成参数）
func validTmuxName(s string) bool {
	if s == "" || len(s) > 100 || !utf8.ValidString(s) || s[0] == '-' {
		return false
	}
	for _, r := range s {
		if r == '.' || r == ':' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (s *Server) tmux(args ...string) *exec.Cmd {
	cmd := exec.Command(s.cfg.TmuxBin, args...)
	cmd.Env = s.termEnv()
	return cmd
}

func (s *Server) termEnv() []string {
	extra := []string{"TERM=xterm-256color", "COLORTERM=truecolor"}
	// systemd 下没有 LANG，tmux 会把中文当成非 UTF-8 显示成下划线
	if os.Getenv("LANG") == "" && os.Getenv("LC_ALL") == "" {
		extra = append(extra, "LANG=C.UTF-8")
	}
	return childEnv(extra...)
}

func (s *Server) tmuxSessions() []TmuxSession {
	const sep = "\x1f"
	format := strings.Join([]string{"#{session_name}", "#{session_attached}", "#{session_windows}",
		"#{session_created}", "#{session_activity}", "#{session_path}", "#{pane_current_command}"}, sep)
	out, err := s.tmux("list-sessions", "-F", format).Output()
	if err != nil {
		return []TmuxSession{} // 没有 tmux server 在跑
	}
	list := []TmuxSession{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, sep)
		if len(f) != 7 {
			continue
		}
		atoi := func(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
		list = append(list, TmuxSession{
			Name: f[0], Attached: int(atoi(f[1])), Windows: int(atoi(f[2])),
			Created: atoi(f[3]) * 1000, Activity: atoi(f[4]) * 1000, Path: f[5], Command: f[6],
		})
	}
	return list
}

func (s *Server) tmuxHas(name string) bool {
	return s.tmux("has-session", "-t", "="+name).Run() == nil
}

// agentCommand 是 tmux 里跑的那条命令。claude 和 scc 拼的是同一条：摘掉 TMUX（否则 claude 降到 256 色），
// 默认补 --dangerously-skip-permissions，root 下配 IS_SANDBOX=1（见 docs/03-scc.md）。
// codex / grok 照此补各自「不再逐个确认」的开关，续会话各用各的参数。
func (s *Server) agentCommand(agent, resume string) string {
	skip := s.cfg.TermSkipPermissions
	parts := []string{"env", "-u", "TMUX", "-u", "TMUX_PANE"}
	if skip && agent == agentClaude {
		parts = append(parts, "IS_SANDBOX=1")
	}
	parts = append(parts, shq(s.agentBin(agent)))
	switch agent {
	case agentCodex:
		if skip {
			parts = append(parts, "--dangerously-bypass-approvals-and-sandbox")
		}
		if resume != "" {
			parts = append(parts, "resume", resume)
		}
	case agentGrok:
		if skip {
			parts = append(parts, "--always-approve")
		}
		if resume != "" {
			parts = append(parts, "--resume", resume)
		}
	default:
		if skip {
			parts = append(parts, "--dangerously-skip-permissions")
		}
		if resume != "" {
			parts = append(parts, "--resume", resume)
		}
	}
	return strings.Join(parts, " ")
}

// 新建会话。在 systemd 服务里跑时，第一次起 tmux 会连 tmux server 一起起，而 server 会落在
// rcweb.service 的 cgroup 里 —— 下次 systemctl restart rcweb 就把所有 tmux 会话连同里面的
// claude 一起杀了。所以套一层 systemd-run --scope，让它有自己的 cgroup。
// then 是紧跟着在同一次 tmux 调用里执行的命令（用 ; 隔开），赶在窗格里的程序退出之前生效
func (s *Server) tmuxCreate(name, dir, command string, cols, rows int, then ...string) error {
	args := []string{"new-session", "-d", "-s", name, "-c", dir,
		"-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows)}
	if command != "" {
		args = append(args, command)
	}
	if len(then) > 0 {
		args = append(append(args, ";"), then...)
	}
	var cmd *exec.Cmd
	if runner, err := exec.LookPath("systemd-run"); err == nil && os.Getenv("INVOCATION_ID") != "" {
		sargs := []string{"--scope", "--quiet", "--collect"}
		if os.Geteuid() != 0 {
			sargs = append([]string{"--user"}, sargs...)
		}
		cmd = exec.Command(runner, append(append(sargs, s.cfg.TmuxBin), args...)...)
		cmd.Env = s.termEnv()
	} else {
		cmd = s.tmux(args...)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (s *Server) handleTmuxList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.tmuxSessions())
}

// /ws/term?session=<名字>&mode=attach|claude|codex|grok|shell&project=<项目>&resume=<会话 ID>&dir=<目录>&status=off&cols=&rows=
//
//	attach             只接回已有会话，不存在就报错（从会话列表点进来的）
//	claude/codex/grok  不存在就新建并在里面跑对应的 CLI（默认 claude），resume 是那个 CLI 的会话 ID
//	shell              不存在就新建一个普通 shell
//
// dir 指定新建时的工作目录（必须在 RCWEB_ROOT 下，比如 worktree 里的对话）；
// status=off 关掉 tmux 状态栏：对话页右侧的终端面板要看起来像一个普通 shell
func (s *Server) handleTermWS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name, mode, resume := q.Get("session"), q.Get("mode"), q.Get("resume")
	if !validTmuxName(name) {
		writeErr(w, http.StatusBadRequest, "会话名不合法")
		return
	}
	if resume != "" && !sessionIDRe.MatchString(resume) {
		writeErr(w, http.StatusBadRequest, "会话 ID 不合法")
		return
	}
	agent := agentClaude
	if validAgent(mode) {
		agent = mode
	}
	cols := clampInt(q.Get("cols"), 20, 500, 100)
	rows := clampInt(q.Get("rows"), 5, 200, 30)

	if !s.tmuxHas(name) {
		if mode == "attach" {
			writeErr(w, http.StatusNotFound, "tmux 会话不存在: "+name)
			return
		}
		dir := s.cfg.Root
		if p := q.Get("project"); p != "" {
			path, ok := s.projectPath(p)
			if !ok {
				writeErr(w, http.StatusNotFound, "没有这个项目")
				return
			}
			dir = path
			if resume != "" {
				if info, ok := s.findSession(path, agent, resume); ok && isDir(info.Cwd) {
					dir = info.Cwd
				}
			}
		}
		if d := q.Get("dir"); d != "" {
			d = filepath.Clean(d)
			if !underDir(s.cfg.Root, d) {
				writeErr(w, http.StatusBadRequest, "目录不在项目根目录下: "+d)
				return
			}
			dir = d
		}
		if !isDir(dir) {
			writeErr(w, http.StatusNotFound, "目录不存在: "+dir)
			return
		}
		command := ""
		if mode != "shell" {
			command = s.agentCommand(agent, resume)
		}
		if err := s.tmuxCreate(name, dir, command, cols, rows); err != nil {
			writeErr(w, http.StatusInternalServerError, "建 tmux 会话失败: "+err.Error())
			return
		}
	}

	if q.Get("status") == "off" {
		// set-option 的 -t 是窗格目标，精确匹配会话名要写成 "=名字:"（has-session 那种会话目标才能省掉冒号）
		_ = s.tmux("set-option", "-t", "="+name+":", "status", "off").Run()
	}

	// -T：告诉 tmux 这个客户端（浏览器里的 xterm.js）支持 24 位色，不然 claude 的配色会被降级
	cmd := s.tmux("-u", "-T", "256,RGB", "attach-session", "-t", "="+name)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "打开终端失败: "+err.Error())
		return
	}
	defer func() {
		_ = ptmx.Close() // tmux 客户端收到 SIGHUP 自行 detach，会话本身不受影响
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Signal(syscall.SIGKILL)
		}
	}()

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go func() {
		defer cancel()
		buf := make([]byte, 32<<10)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if conn.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				conn.Close(websocket.StatusNormalClosure, "tmux 客户端已退出")
				return
			}
		}
	}()
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, 20*time.Second)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil {
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			if _, err := ptmx.Write(data); err != nil {
				return
			}
			continue
		}
		var msg struct {
			Type       string `json:"type"`
			Cols, Rows int
		}
		if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" && msg.Cols > 0 && msg.Rows > 0 {
			_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(min(msg.Cols, 500)), Rows: uint16(min(msg.Rows, 200))})
		}
	}
}

func clampInt(s string, lo, hi, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return max(lo, min(hi, n))
}

// underDir：p 在 root 下（含 root 本身），两边先解开软链再比。rcsync 同步来的会话记的是 Mac 路径
// （/Users/…/workspace/x），VPS 上靠 /Users/…/workspace -> /workspace 软链才落在根目录下；
// 按字面比会把它们全拒掉。p 解不开（不存在）两边都按字面比，交给后面的 isDir 报「目录不存在」
func underDir(root, p string) bool {
	if rp, err := filepath.EvalSymlinks(p); err == nil {
		p = rp
		if r, err := filepath.EvalSymlinks(root); err == nil {
			root = r
		}
	}
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func isDir(p string) bool {
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
