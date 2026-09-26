// 和 rcweb 后端（../../*.go）对应的类型与请求封装
import type { Agent } from '@/lib/agents'

export type Block = {
  t: 'text' | 'thinking' | 'tool_use' | 'tool_result' | 'image'
  text?: string
  id?: string
  name?: string
  input?: Record<string, unknown>
  isError?: boolean
  lazy?: boolean // 打开会话时下发的是精简版：参数只剩摘要、输出拿掉了，全文按 id 另取（lib/tool-details.ts）
  files?: FileChange[] // 改文件的工具改了哪些文件（tool_use 上按参数算，Claude 的 tool_result 上是它给的 diff）
}

// 改文件的 diff（见 ../../diff.go）。hunk 的 lines 带前缀 ' ' '+' '-'；oldStart、newStart 都是 0 表示不知道行号
export type Hunk = { oldStart: number; oldLines: number; newStart: number; newLines: number; lines: string[] }

export type FileChange = {
  path: string
  status?: 'add' | 'delete' | 'rename' | 'untracked' | ''
  oldPath?: string
  add: number
  del: number
  binary?: boolean
  hunks?: Hunk[]
  truncated?: boolean
}

// GET /api/diff：工作区相对 HEAD 的改动。路径相对 root；notRepo 是看不了的原因
export type DiffSummary = { root?: string; branch?: string; files: FileChange[]; more?: boolean; notRepo?: string }

// model：assistant 这条实际是哪个模型回的（只有 claude 有）
export type Entry = { role: 'user' | 'assistant' | 'note'; blocks: Block[]; ts?: string; model?: string }

// 会话 / 对话的一页（见 ../../session_page.go）。start 是这一页第一条在整个会话里的下标，往上翻就带 before=start
export type Page = { entries: Entry[]; start: number; total: number; more: boolean }

export type ToolDetail = { input?: Record<string, unknown>; result?: { text: string; isError?: boolean }; files?: FileChange[] }

export type Project = {
  name: string
  path: string
  exists: boolean
  sessions: number // 没归档的
  archived: number
  agents?: Partial<Record<Agent, number>> // 没归档的会话按 CLI 分
  lastActive: number
  discovered?: boolean
}

export type SessionInfo = {
  id: string
  agent?: Agent
  title: string
  cwd: string
  worktree?: string
  mtime: number
  size: number
  archived?: boolean
  chatId?: string
}

export type TmuxSession = {
  name: string
  attached: number
  windows: number
  created: number
  activity: number
  path: string
  command: string
}

export type ChatState = 'idle' | 'running' | 'exited'

// 侧栏上的状态：运行中 / 等你回答（权限或提问）/ 后台运行中（这一轮完了，后台命令、子代理还在跑）/
// 已完成（还没看）/ 空闲 / 出错
export type ChatStatus = 'running' | 'waiting' | 'background' | 'done' | 'idle' | 'error'

// 还在跑的后台任务（CLI 报的）：kind 是 local_bash / local_agent 之类
export type BgTask = { id: string; kind: string; description: string }

export type ChatInfo = {
  id: string
  agent?: Agent
  project: string
  cwd: string
  sessionId: string
  title: string
  state: ChatState
  permissionMode: string
  model: string // 选的（别名，如 opus / default）
  resolvedModel: string // CLI 解析成的全名
  actualModel?: string // 最近一条回复实际是哪个模型（claude），和 resolvedModel 不一样就是被降级 / 换了
  effort: string // 没选时也是具体的一档（CLI 实际在用的）
  status: ChatStatus
  background?: BgTask[]
  activity: string // thinking / text / tool:<名字>
  createdAt: number
  exitCode: number
  clients: number
  pending: number
}

export type PermissionReq = {
  id: string
  tool: string
  input: Record<string, unknown>
  description?: string
  blockedPath?: string
  canAlways: boolean
  // 「总是允许」会写下的规则，比如 label 是 Bash(rcweb deploy:*)，where 是「项目的 .claude/settings.local.json」（只有 claude 给）
  always?: { label: string; where?: string }
}

export type PermissionAnswer = {
  id: string
  choice: 'allow' | 'always' | 'deny'
  answers?: Record<string, string>
  message?: string
}

export async function api<T>(path: string, init?: { method?: string; body?: unknown }): Promise<T> {
  const res = await fetch(path, {
    method: init?.method ?? 'GET',
    headers: init?.body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: init?.body !== undefined ? JSON.stringify(init.body) : undefined,
  })
  if (res.status === 401) {
    window.dispatchEvent(new Event('rcweb:unauthorized'))
    throw new Error('登录已失效')
  }
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data.error || `${res.status} ${res.statusText}`)
  return data as T
}

export const wsURL = (path: string) => `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}${path}`

// claude 对 initialize 的应答（../../catalog.go 里裁剪过）：可选模型、斜杠命令、agents。
// codex / grok 由后端拼成同样的形状
export type ModelInfo = {
  value: string
  displayName: string
  description?: string
  resolvedModel?: string
  supportedEffortLevels?: string[]
  defaultEffort?: string // 不指定 effort 时实际用哪一档（空 = 这个模型不支持 effort；没有 = 没问出来）
}

export type CommandInfo = { name: string; description?: string; argumentHint?: string; builtin?: boolean }

export type Catalog = { models?: ModelInfo[]; commands?: CommandInfo[] }

export type StartOpts = {
  agent?: Agent
  project: string
  sessionId?: string
  permissionMode: string
  model: string
  effort: string
  prompt?: string
  attachments?: string[] // 第一条消息带的附件 id（lib/attachments.ts）
}

// 新对话的默认模型 / effort / 权限模式，按 CLI 各一份（见 ../../prefs.go）。
// defaults 里空着的一项 = 和上次一致（用 last 的）；defaults.effort 是 'model' = 跟着模型走（不指定）
export type ChatChoice = { model?: string; effort?: string; mode?: string }
export type ChatPrefs = { defaults: ChatChoice; last?: ChatChoice }

// 设置页的 reclaude 一页（见 ../../reclaude.go）。/api/reclaude 只答装没装、开没开、版本；
// 状态、网关、组织各自一个接口。status / gateway 是它 `key: value` 输出原样拆开的，保持顺序
export type KV = { key: string; value: string }

export type ReclaudeOrg = { id: string; name: string; type?: string; email?: string; current: boolean }

export type ReclaudeInfo = { installed: boolean; path?: string; enabled: boolean; version?: string }

// `reclaude config gateway test` 的一行，已按耗时排好。detail：通的是耗时，不通的是原因
export type GatewayResult = { status: string; ok: boolean; url: string; detail: string }

// 设置页的 Remote Control 一页（见 ../../remote_control.go）：`claude remote-control` 常驻在 tmux 会话里
export type RemoteControl = {
  enabled: boolean
  project: string // 空 = 项目根目录
  name: string
  permissionMode: string
  spawn: string // same-dir / worktree / session
  capacity: number // 0 = 默认
  autoRestart: boolean
  restartDelay: number // 秒
}

// restarting：守护脚本在等着重启；exited：退出了、没设自动重启（窗格留着，output 是它最后说的话）
export type RCStatus = {
  config: RemoteControl
  state: 'stopped' | 'running' | 'restarting' | 'exited'
  exitCode?: number
  session: string
  script: string
  bin: string
  output: string
}
