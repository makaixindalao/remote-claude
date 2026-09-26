import { useSyncExternalStore } from 'react'

// 页面右上角那几个开关：思考过程展开、工具调用三档、右侧终端面板。全局记住（这个浏览器里）

// merged：连续的工具调用合成一行摘要（「运行了 4 个命令」）；compact：摘要点开的样子，一个调用一行、参数和输出收着
// （点开单个调用时输出截短）；expanded：每个调用都展开，输出全部显示
export type ToolMode = 'merged' | 'compact' | 'expanded'
export type ViewPrefs = { thinking: boolean; tools: ToolMode; terminal: boolean }

const KEY = 'rcweb.view'
const DEFAULTS: ViewPrefs = { thinking: false, tools: 'merged', terminal: false }
const MODES: ToolMode[] = ['merged', 'compact', 'expanded']

let state: ViewPrefs = (() => {
  try {
    const v = { ...DEFAULTS, ...JSON.parse(localStorage.getItem(KEY) ?? '{}') }
    if (!MODES.includes(v.tools)) v.tools = DEFAULTS.tools // 老版本存的 collapsed
    return v
  } catch {
    return DEFAULTS
  }
})()
const subs = new Set<() => void>()

export function setView(patch: Partial<ViewPrefs>) {
  state = { ...state, ...patch }
  try {
    localStorage.setItem(KEY, JSON.stringify(state))
  } catch {
    /* 记不住而已 */
  }
  subs.forEach((f) => f())
}

export function useView() {
  return useSyncExternalStore(
    (cb) => {
      subs.add(cb)
      return () => subs.delete(cb)
    },
    () => state,
  )
}
