import { useSyncExternalStore } from 'react'
import { agentModes, agentOf, defaultMode, type Agent } from '@/lib/agents'
import { api, type ChatChoice, type ChatPrefs } from '@/lib/api'

// 新对话用什么模型 / effort / 权限模式：设置页里的默认值（每一项都可以是「和上次一致」）+ 上次用的，按 CLI 各一份。
// 都存在 VPS 上（/api/prefs，见 ../../prefs.go），手机和电脑是同一套；浏览器里另存一份上次用的，
// 服务端那份还没到、或者以前只存在浏览器里时用。选哪个 CLI、哪个项目只记在这个浏览器里

export type Settings = { model: string; effort: string; mode: string }

// 设置页里 effort 的「跟着模型」：不指定，用各模型自己的那一档
export const EFFORT_BY_MODEL = 'model'

type AllPrefs = Partial<Record<Agent, ChatPrefs>>

let prefs: AllPrefs | undefined
let loading: Promise<void> | undefined
const subs = new Set<() => void>()
const publish = (next: AllPrefs) => {
  prefs = next
  subs.forEach((f) => f())
}

function loadPrefs() {
  loading ??= api<AllPrefs>('/api/prefs')
    .then(publish)
    .catch(() => {
      loading = undefined // 下次用到再问
    })
  return loading
}

function subscribe(cb: () => void) {
  subs.add(cb)
  void loadPrefs()
  return () => {
    subs.delete(cb)
  }
}

// 服务端的那份；还没到时是 undefined
export function useChatPrefs() {
  return useSyncExternalStore(subscribe, () => prefs)
}

// claude 沿用最早的键名，老浏览器里记的设置不丢
const key = (agent: Agent) => (agent === 'claude' ? 'rcweb.settings' : `rcweb.settings.${agent}`)

function localLast(agent: Agent): Partial<Settings> {
  try {
    return JSON.parse(localStorage.getItem(key(agent)) ?? '{}')
  } catch {
    return {}
  }
}

// newChatSettings：新对话一开始用哪套。defaults 里空着的一项用上次的；上次的先看服务端、再看浏览器里的；
// 都没有：模型交给 CLI 自己推荐（default，页面上显示成具体的模型）、effort 跟着模型
export function newChatSettings(agent: Agent, p?: ChatPrefs): Settings {
  const l = p?.last
  const last: Settings = l
    ? { model: l.model || 'default', effort: l.effort ?? '', mode: l.mode || defaultMode[agent] }
    : { model: 'default', effort: '', mode: defaultMode[agent], ...localLast(agent) }
  const d = p?.defaults ?? {}
  const s = {
    model: d.model || last.model,
    effort: d.effort === EFFORT_BY_MODEL ? '' : d.effort || last.effort,
    mode: d.mode || last.mode,
  }
  return agentModes[agent].includes(s.mode) ? s : { ...s, mode: defaultMode[agent] }
}

// rememberSettings：输入框里改了一项就记成「上次用的」
export function rememberSettings(s: Settings, agent: Agent = 'claude') {
  try {
    localStorage.setItem(key(agent), JSON.stringify(s))
  } catch {
    /* 隐私模式下没有 localStorage：记不住而已 */
  }
  const last: ChatChoice = { model: s.model, effort: s.effort, mode: s.mode }
  publish({ ...prefs, [agent]: { defaults: prefs?.[agent]?.defaults ?? {}, last } })
  api('/api/prefs', { method: 'POST', body: { agent, last } }).catch(() => {})
}

// saveDefaults：设置页里存默认值，失败抛出去让页面提示
export async function saveDefaults(agent: Agent, defaults: ChatChoice) {
  const saved = await api<ChatPrefs>('/api/prefs', { method: 'POST', body: { agent, defaults } })
  publish({ ...prefs, [agent]: saved })
}

export function loadAgent(): Agent {
  try {
    return agentOf(localStorage.getItem('rcweb.agent'))
  } catch {
    return 'claude'
  }
}

export function saveAgent(a: Agent) {
  try {
    localStorage.setItem('rcweb.agent', a)
  } catch {
    /* 同上 */
  }
}

export function loadProject() {
  try {
    return localStorage.getItem('rcweb.project') ?? ''
  } catch {
    return ''
  }
}

export function saveProject(p: string) {
  try {
    localStorage.setItem('rcweb.project', p)
  } catch {
    /* 同上 */
  }
}
