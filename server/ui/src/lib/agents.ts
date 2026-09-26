// 三种 CLI 在页面上的差异：名字、权限模式、终端会话名前缀。协议上的差异都在后端（../../chat_*.go），
// 到了页面上会话、对话、Entry 都是同一个形状，只剩这几项要按 agent 区分

export type Agent = 'claude' | 'codex' | 'grok'

export const AGENTS: Agent[] = ['claude', 'codex', 'grok']

export const agentLabel: Record<Agent, string> = { claude: 'Claude', codex: 'Codex', grok: 'Grok' }

// 接口里没写 agent 的一律是 claude（老数据、老链接）
export const agentOf = (a?: string | null): Agent => (a === 'codex' || a === 'grok' ? a : 'claude')

// 权限模式用各自 CLI 的名字，不翻译。codex 没有权限模式，是审批 × 沙箱的三档预设（和它 TUI 的 /approvals 一样）；
// grok 开会话时能选 ask / auto / always-approve，之后只能开关 always-approve
export const agentModes: Record<Agent, readonly string[]> = {
  claude: ['default', 'acceptEdits', 'plan', 'auto', 'bypassPermissions'],
  codex: ['read-only', 'auto', 'full-access'],
  grok: ['default', 'auto', 'bypassPermissions'],
}

// 名字不翻译，下拉项里配一句白话说明（没把握的就不写）
export const modeHint: Record<Agent, Record<string, string>> = {
  claude: {
    default: '每次都问',
    acceptEdits: '改文件不问，跑命令还问',
    plan: '只读，先出计划',
    auto: '安全的自动放行，其余才问',
    bypassPermissions: '全都不问',
  },
  codex: { 'read-only': '只读，改动要问', auto: '项目目录里自动，出去才问', 'full-access': '全都不问' },
  grok: { default: '每次都问', bypassPermissions: '全都不问' },
}

export const defaultMode: Record<Agent, string> = { claude: 'default', codex: 'auto', grok: 'default' }

// 不再逐个确认的那一档，界面上标红
export const dangerMode: Record<Agent, string> = { claude: 'bypassPermissions', codex: 'full-access', grok: 'bypassPermissions' }

// tmux 会话名前缀：claude 和 scc 一样是 cc-
export const termPrefix: Record<Agent, string> = { claude: 'cc', codex: 'cx', grok: 'gk' }
