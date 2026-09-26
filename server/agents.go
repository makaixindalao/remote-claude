package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
)

// 三种 CLI：claude（Claude Code）、codex（OpenAI Codex CLI）、grok（Grok Build）。
//
// 会话记录各存各的（~/.claude/projects、~/.codex/sessions、~/.grok/sessions），网页对话各走各的协议
// （stream-json / app-server / ACP），到了页面上统一成同一种 SessionInfo、Entry 和 ChatInfo。
// 接口参数里不写 agent 的一律当 claude，老链接照样能用。

const (
	agentClaude = "claude"
	agentCodex  = "codex"
	agentGrok   = "grok"
)

var (
	// codex / grok 的 effort 档位随模型走（minimal、ultra 之类），不在这里写死，只挡明显不对的
	agentEffortRe = regexp.MustCompile(`^[a-z]{1,16}$`)

	// codex 没有「权限模式」，是审批策略 × 沙箱两个旋钮；照 codex TUI 的 /approvals 给三档预设
	codexModes = map[string]codexMode{
		"read-only":   {approval: "on-request", sandbox: "read-only"},
		"auto":        {approval: "on-request", sandbox: "workspace-write"},
		"full-access": {approval: "never", sandbox: "danger-full-access"},
	}

	// grok 经 ACP 只能在开会话时选 ask / auto / always-approve（session/new 的 _meta），
	// 之后能切换的只有 always-approve 开关（/always-approve on|off）
	grokModes = map[string]bool{"default": true, "auto": true, "bypassPermissions": true}
)

type codexMode struct{ approval, sandbox string }

// sandboxPolicy 是 turn/start 里改沙箱要用的形状（和 thread/start 的 sandbox 字符串不一样）
func (m codexMode) sandboxPolicy() map[string]any {
	switch m.sandbox {
	case "read-only":
		return map[string]any{"type": "readOnly", "networkAccess": false}
	case "danger-full-access":
		return map[string]any{"type": "dangerFullAccess"}
	}
	return map[string]any{"type": "workspaceWrite", "writableRoots": []string{}, "networkAccess": false,
		"excludeTmpdirEnvVar": false, "excludeSlashTmp": false}
}

func validAgent(a string) bool { return a == agentClaude || a == agentCodex || a == agentGrok }

func normAgent(a string) string {
	if a == "" {
		return agentClaude
	}
	return a
}

func (s *Server) agentBin(a string) string {
	switch a {
	case agentCodex:
		return s.cfg.CodexBin
	case agentGrok:
		return s.cfg.GrokBin
	}
	return s.claudeBin()
}

// agentHome：各自的数据目录，回收站也放在这下面
func (s *Server) agentHome(a string) string {
	switch a {
	case agentCodex:
		return s.cfg.CodexDir
	case agentGrok:
		return s.cfg.GrokDir
	}
	return s.cfg.ClaudeDir
}

// availableAgents：VPS 上装了的才在页面上给选项。claude 是本项目的主角，一直算在内
func (s *Server) availableAgents() []string {
	out := []string{agentClaude}
	for _, a := range []string{agentCodex, agentGrok} {
		if _, err := exec.LookPath(s.agentBin(a)); err == nil {
			out = append(out, a)
		}
	}
	return out
}

func defaultMode(agent string) string {
	if agent == agentCodex {
		return "auto"
	}
	return "default"
}

// validateStart 按 agent 检查新对话的模型 / effort / 权限模式，并补上默认值
func validateStart(o *StartOpts) error {
	o.Agent = normAgent(o.Agent)
	if !validAgent(o.Agent) {
		return fmt.Errorf("不认识的 agent %q", o.Agent)
	}
	if o.PermissionMode == "" {
		o.PermissionMode = defaultMode(o.Agent)
	}
	if !validMode(o.Agent, o.PermissionMode) {
		return fmt.Errorf("不认识的权限模式 %q", o.PermissionMode)
	}
	if !modelRe.MatchString(o.Model) {
		return fmt.Errorf("模型名不合法")
	}
	if o.Effort != "" && !validEffort(o.Agent, o.Effort) {
		return fmt.Errorf("不认识的 effort %q", o.Effort)
	}
	return nil
}

func validMode(agent, mode string) bool {
	switch agent {
	case agentCodex:
		_, ok := codexModes[mode]
		return ok
	case agentGrok:
		return grokModes[mode]
	}
	return permissionModes[mode]
}

func validEffort(agent, effort string) bool {
	if agent == agentClaude {
		return efforts[effort]
	}
	return agentEffortRe.MatchString(effort)
}

// agentDir：CODEX_HOME / GROK_HOME 这类环境变量优先，和 CLI 自己找数据目录的规则一致
func agentDir(envKey, homeEnv, home, name string) string {
	return filepath.Clean(env(envKey, env(homeEnv, filepath.Join(home, name))))
}
