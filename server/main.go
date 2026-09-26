// rcweb — remote-claude 的 Web 入口，跑在 VPS 上。
//
// 浏览器里能做三件事：翻看会话记录、和 VPS 上的 Claude / Codex / Grok 聊天（各自的 headless 协议，
// 工具权限在页面上点按钮批准）、接回 scc 开的 tmux 终端。
//
// 默认只监听 127.0.0.1，由 Tailscale 或 SSH 隧道把它带到浏览器；RCWEB_LISTEN=0.0.0.0:7681 则直接
// 用 http://<VPS IP>:7681 打开。登录靠 RCWEB_PASSWORD。
// 所有配置都走环境变量（见 .env.example）：systemd 用 EnvironmentFile 注入，
// 本地开发时读当前目录的 .env。
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	Listen      string
	Password    string
	Secret      string   // 可选：参与 cookie 签名，换掉它等于让所有登录失效
	Root        string   // 项目根目录，对应 remote-claude 的 RC_REMOTE_ROOT
	Projects    []string // 相对 Root 的项目名，对应 RC_PROJECTS / RC_SESSION_PROJECTS
	ClaudeDir   string
	ClaudeBin   string
	ReclaudeBin string // 设置页打开「用 reclaude 代替 claude」后起的是它（见 reclaude.go）
	CodexDir    string // codex 的数据目录（CODEX_HOME），会话在 sessions/ 下
	CodexBin    string
	GrokDir     string // grok 的数据目录（GROK_HOME）
	GrokBin     string
	TmuxBin     string
	// 网页终端新建 tmux 会话时，是否像 scc 一样补 --dangerously-skip-permissions
	TermSkipPermissions bool
}

func main() {
	if err := loadDotEnv(); err != nil {
		log.Fatalf("读 .env 失败: %v", err)
	}
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	if host, _, err := net.SplitHostPort(cfg.Listen); err == nil {
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			log.Printf("警告: 监听 %s 不是回环地址 —— 能连上这个端口的人都能用密码换到一个跑 Claude 的 shell", cfg.Listen)
		}
	}

	s := newServer(cfg)
	go s.ensureRC() // Remote Control 开着就拉起来（VPS 重启后）
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		log.Printf("收到退出信号，结束所有聊天进程")
		s.chats.StopAll()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("rcweb 监听 %s  root=%s  projects=%v", cfg.Listen, cfg.Root, cfg.Projects)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func loadConfig() (*Config, error) {
	home, _ := os.UserHomeDir()
	cfg := &Config{
		Listen:              env("RCWEB_LISTEN", "127.0.0.1:7681"),
		Password:            os.Getenv("RCWEB_PASSWORD"),
		Secret:              os.Getenv("RCWEB_SECRET"),
		Root:                filepath.Clean(env("RCWEB_ROOT", home)),
		ClaudeDir:           filepath.Clean(env("RCWEB_CLAUDE_DIR", filepath.Join(home, ".claude"))),
		ClaudeBin:           env("RCWEB_CLAUDE_BIN", "claude"),
		ReclaudeBin:         env("RCWEB_RECLAUDE_BIN", "reclaude"),
		CodexDir:            agentDir("RCWEB_CODEX_DIR", "CODEX_HOME", home, ".codex"),
		CodexBin:            env("RCWEB_CODEX_BIN", "codex"),
		GrokDir:             agentDir("RCWEB_GROK_DIR", "GROK_HOME", home, ".grok"),
		GrokBin:             env("RCWEB_GROK_BIN", "grok"),
		TmuxBin:             env("RCWEB_TMUX_BIN", "tmux"),
		TermSkipPermissions: env("RCWEB_TERM_SKIP_PERMISSIONS", "1") == "1",
	}
	if cfg.Password == "" {
		return nil, errors.New("没有设置 RCWEB_PASSWORD（写在 .env 里，参考 .env.example）")
	}
	for _, p := range strings.FieldsFunc(os.Getenv("RCWEB_PROJECTS"), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n'
	}) {
		if p = strings.Trim(p, "/"); p != "" {
			cfg.Projects = append(cfg.Projects, p)
		}
	}
	return cfg, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// loadDotEnv 把 $RCWEB_ENV_FILE（默认 ./.env）里的 KEY=VALUE 读进环境变量。
// 已经存在的环境变量不覆盖 —— 显式 export 的优先级最高，和 remote-claude 其余脚本一致。
func loadDotEnv() error {
	path := env("RCWEB_ENV_FILE", ".env")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && os.Getenv("RCWEB_ENV_FILE") == "" {
		return nil
	}
	if err != nil {
		return err
	}
	for k, v := range parseDotEnv(string(data)) {
		if _, ok := os.LookupEnv(k); !ok {
			os.Setenv(k, v)
		}
	}
	return nil
}

func parseDotEnv(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

// childEnv 是给 claude / codex / grok / tmux 子进程的环境：
//   - 去掉 RCWEB_*，密码不能漏进 Claude 跑的 shell 里（systemd 的 EnvironmentFile 会把它放进本进程环境）
//   - 去掉 Claude Code 自己的会话标记，否则在 Claude 里启动的 rcweb（开发时）会让子 claude 以为自己是嵌套会话
//   - 去掉 TMUX：claude 一见它就把颜色降到 256 色（见 docs/03-scc.md）
func childEnv(extra ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "RCWEB_") || sessionMarkers[k] {
			continue
		}
		out = append(out, kv)
	}
	return append(out, extra...)
}

// 只摘会话标记，不碰 CLAUDE_CODE_OAUTH_TOKEN / CLAUDE_CODE_USE_BEDROCK 这类真配置
var sessionMarkers = map[string]bool{
	"CLAUDECODE": true, "CLAUDE_PID": true, "CLAUDE_EFFORT": true,
	"CLAUDE_CODE_ENTRYPOINT": true, "CLAUDE_CODE_SESSION_ID": true,
	"CLAUDE_CODE_CHILD_SESSION": true, "CLAUDE_CODE_SESSION_ATTENDED": true,
	"CLAUDE_CODE_MESSAGING_SOCKET": true, "CLAUDE_CODE_MESSAGING_TOKEN": true,
	"CLAUDE_CODE_EXECPATH": true, "CLAUDE_CODE_SSE_PORT": true,
	"TMUX": true, "TMUX_PANE": true,
}
