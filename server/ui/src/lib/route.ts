import { useSyncExternalStore } from 'react'
import { termPrefix, type Agent } from '@/lib/agents'

// 用 hash 路由：刷新、前进后退、把链接发到手机上都能回到同一个页面，后端也不用管前端路由

// 设置页的分页：外观只存在浏览器里，对话（新对话的默认模型这些）、通知、reclaude、Remote Control 是 VPS 上的设置
export type SettingsTab = 'appearance' | 'chat' | 'notify' | 'reclaude' | 'rc'

export type Route =
  | { name: 'home' }
  | { name: 'new'; project?: string }
  | { name: 'project'; project: string }
  | { name: 'session'; project: string; id: string; agent?: string }
  | { name: 'chat'; id: string }
  | { name: 'term'; session: string; project?: string; mode?: string; resume?: string }
  | { name: 'settings'; tab?: SettingsTab }

export function parseHash(hash: string): Route {
  const [path, query = ''] = hash.replace(/^#/, '').split('?')
  const parts = path.split('/').filter(Boolean).map(decodeURIComponent)
  const q = new URLSearchParams(query)
  const opt = (k: string) => q.get(k) ?? undefined
  switch (parts[0]) {
    case 'new':
      return { name: 'new', project: opt('project') }
    case 'p':
      if (parts[1]) return { name: 'project', project: parts[1] }
      break
    case 's':
      if (parts[1] && parts[2]) return { name: 'session', project: parts[1], id: parts[2], agent: opt('agent') }
      break
    case 'c':
      if (parts[1]) return { name: 'chat', id: parts[1] }
      break
    case 't':
      if (parts[1]) return { name: 'term', session: parts[1], project: opt('project'), mode: opt('mode'), resume: opt('resume') }
      break
    case 'settings':
      return { name: 'settings', tab: (['chat', 'notify', 'reclaude', 'rc'] as const).find((t) => t === parts[1]) ?? 'appearance' }
  }
  return { name: 'home' }
}

export const href = {
  home: () => '#/',
  newChat: (project?: string) => (project ? `#/new?project=${encodeURIComponent(project)}` : '#/new'),
  project: (p: string) => `#/p/${encodeURIComponent(p)}`,
  // claude 的会话不带 agent 参数，老链接照样能用
  session: (p: string, id: string, agent?: Agent) =>
    `#/s/${encodeURIComponent(p)}/${id}${agent && agent !== 'claude' ? `?agent=${agent}` : ''}`,
  chat: (id: string) => `#/c/${id}`,
  term: (session: string, opts: { project?: string; mode?: 'attach' | 'shell' | Agent; resume?: string } = {}) => {
    const q = new URLSearchParams(Object.entries(opts).filter((kv): kv is [string, string] => !!kv[1]))
    return `#/t/${encodeURIComponent(session)}${q.size ? `?${q}` : ''}`
  },
  settings: (tab?: SettingsTab) => (tab && tab !== 'appearance' ? `#/settings/${tab}` : '#/settings'),
}

export const navigate = (h: string) => {
  location.hash = h
}

const subscribe = (cb: () => void) => {
  window.addEventListener('hashchange', cb)
  return () => window.removeEventListener('hashchange', cb)
}

export function useHash() {
  return useSyncExternalStore(subscribe, () => location.hash)
}

// 和 scc 同一套会话名：cc-<目录名>，去掉前导点，. 和 : 换成 -（tmux 不收）。codex / grok 是 cx- / gk-
export function termName(path: string, sessionId?: string, agent: Agent = 'claude') {
  const base = (path.split('/').filter(Boolean).pop() ?? '').replace(/^\.+/, '').replace(/[.:]/g, '-')
  const prefix = termPrefix[agent]
  const name = base ? `${prefix}-${base}` : prefix
  return sessionId ? `${name}-${sessionId.slice(0, 8)}` : name
}

// 对话页右侧终端面板用的 shell 会话名：sh-<目录名>
export function shellName(cwd: string) {
  const base = (cwd.split('/').filter(Boolean).pop() ?? '').replace(/^\.+/, '').replace(/[.:]/g, '-')
  return base ? `sh-${base}` : 'sh'
}
