import { useSyncExternalStore } from 'react'

// 设置页里的字体、字号。只存在这个浏览器里：手机和电脑想要的字号常常不一样。
// 落到 <html> 上的三个 CSS 变量（默认值和用法见 index.css）：
//   --rc-font-sans  界面字体        --rc-font-mono  代码字体（代码块、工具输出、终端）
//   --rc-text-scale 所有文字尺寸的倍数，正文 14px 为 1；间距、图标不跟着变

export type FontKind = 'sans' | 'mono'

export type FontChoice = {
  id: string
  label: string
  family: string // 排在 font-family 最前面的一段，后面统一接兜底字体
  hint: string
  load?: () => Promise<unknown> // 编进来的字体：选中时才下载
}

// 中文字符一律落到系统自带的中文字体上
const SANS_FALLBACK = 'ui-sans-serif, system-ui, -apple-system, "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Noto Sans CJK SC", sans-serif'
export const MONO_FALLBACK = 'ui-monospace, "SF Mono", Menlo, "Cascadia Mono", Consolas, "DejaVu Sans Mono", monospace'

export const FONTS: Record<FontKind, FontChoice[]> = {
  sans: [
    { id: 'geist', label: 'Geist', family: '"Geist Variable"', hint: '内置 · 默认' }, // index.css 里静态引入了
    { id: 'inter', label: 'Inter', family: '"Inter Variable"', hint: '内置', load: () => import('@fontsource-variable/inter') },
    { id: 'system', label: '系统字体', family: '', hint: '苹方 / 微软雅黑 / 思源黑体' },
    { id: 'serif', label: '衬线', family: 'ui-serif, Georgia, "Songti SC", "Noto Serif CJK SC", "Source Han Serif SC", SimSun, serif', hint: '宋体 / 思源宋体' },
  ],
  mono: [
    { id: 'system', label: '系统等宽', family: '', hint: 'SF Mono / Menlo / Consolas · 默认' },
    { id: 'jetbrains', label: 'JetBrains Mono', family: '"JetBrains Mono Variable"', hint: '内置', load: () => import('@fontsource-variable/jetbrains-mono') },
    { id: 'fira', label: 'Fira Code', family: '"Fira Code Variable"', hint: '内置', load: () => import('@fontsource-variable/fira-code') },
    { id: 'geist', label: 'Geist Mono', family: '"Geist Mono Variable"', hint: '内置', load: () => import('@fontsource-variable/geist-mono') },
  ],
}

export const CUSTOM = 'custom'

export type Appearance = {
  sans: string // FONTS.sans 里的 id，或 custom
  sansCustom: string // 自定义时填的字体名，逗号隔开可以写好几个
  mono: string
  monoCustom: string
  textSize: number // 正文字号 px
  termSize: number // 终端字号 px；0 = 自动（触屏 12，其余 13）
}

export const TEXT_SIZE = { min: 12, max: 20, base: 14 }
export const TERM_SIZE = { min: 10, max: 24 }

export const DEFAULTS: Appearance = { sans: 'geist', sansCustom: '', mono: 'system', monoCustom: '', textSize: TEXT_SIZE.base, termSize: 0 }

const KEY = 'rcweb.appearance'

const clamp = (n: unknown, min: number, max: number, dflt: number) => (typeof n === 'number' && n >= min && n <= max ? Math.round(n) : dflt)

function normalize(v: Partial<Appearance>): Appearance {
  const a = { ...DEFAULTS, ...v }
  for (const kind of ['sans', 'mono'] as const) {
    if (a[kind] !== CUSTOM && !FONTS[kind].some((f) => f.id === a[kind])) a[kind] = DEFAULTS[kind]
  }
  a.textSize = clamp(a.textSize, TEXT_SIZE.min, TEXT_SIZE.max, DEFAULTS.textSize)
  a.termSize = a.termSize === 0 ? 0 : clamp(a.termSize, TERM_SIZE.min, TERM_SIZE.max, 0)
  return a
}

let state: Appearance = (() => {
  try {
    return normalize(JSON.parse(localStorage.getItem(KEY) ?? '{}'))
  } catch {
    return DEFAULTS
  }
})()
const subs = new Set<() => void>()

// 通用字体族名不能加引号，加了就成了一个叫 "serif" 的字体
const GENERIC = new Set(['serif', 'sans-serif', 'monospace', 'cursive', 'fantasy', 'system-ui', 'ui-serif', 'ui-sans-serif', 'ui-monospace', 'ui-rounded', 'math', 'emoji', 'fangsong'])

// 用户填的「Sarasa Mono SC, 'LXGW WenKai'」→ 每个名字重新加好引号
export function familyList(input: string) {
  return input
    .split(/[,，]/)
    .map((s) => s.trim().replace(/^['"]+|['"]+$/g, '').replace(/["\\]/g, ''))
    .filter(Boolean)
    .map((s) => (GENERIC.has(s.toLowerCase()) ? s.toLowerCase() : `"${s}"`))
    .join(', ')
}

function head(kind: FontKind, a: Appearance) {
  if (a[kind] === CUSTOM) return familyList(kind === 'sans' ? a.sansCustom : a.monoCustom)
  return FONTS[kind].find((f) => f.id === a[kind])?.family ?? ''
}

// 完整的 font-family：选的字体在前，兜底在后
export function fontStack(kind: FontKind, a: Appearance = state) {
  const h = head(kind, a)
  const fallback = kind === 'sans' ? SANS_FALLBACK : MONO_FALLBACK
  return h ? `${h}, ${fallback}` : fallback
}

// 编进来的字体选中了才下载 CSS；下完再等字形文件就绪（终端要在字体到位后才能量准字宽）
export async function fontReady(kind: FontKind, a: Appearance = state, px = 16) {
  const choice = FONTS[kind].find((f) => f.id === a[kind])
  await choice?.load?.().catch(() => {})
  await document.fonts.load(`${px}px ${fontStack(kind, a)}`).catch(() => {})
}

function apply(a: Appearance) {
  const s = document.documentElement.style
  s.setProperty('--rc-font-sans', fontStack('sans', a))
  s.setProperty('--rc-font-mono', fontStack('mono', a))
  s.setProperty('--rc-text-scale', String(a.textSize / TEXT_SIZE.base))
  void fontReady('sans', a)
  void fontReady('mono', a)
}

// 首屏渲染前调一次（main.tsx），免得先闪一下默认字体和字号
export function initAppearance() {
  apply(state)
}

export function setAppearance(patch: Partial<Appearance>) {
  state = normalize({ ...state, ...patch })
  try {
    localStorage.setItem(KEY, JSON.stringify(state))
  } catch {
    /* 隐私模式下没有 localStorage：这次生效，下次记不住而已 */
  }
  apply(state)
  subs.forEach((f) => f())
}

export function useAppearance() {
  return useSyncExternalStore(
    (cb) => {
      subs.add(cb)
      return () => subs.delete(cb)
    },
    () => state,
  )
}

export const termFontSize = (a: Appearance, touch: boolean) => a.termSize || (touch ? 12 : 13)

// 这台设备的浏览器能不能用这个字体：同一段字用「它 + 通用字体」和「只用通用字体」各量一次，
// 三种通用字体下宽度都一样就是没有。Safari 不让网页用自己装的字体，这里也会如实报没有
export function fontAvailable(input: string) {
  const ctx = document.createElement('canvas').getContext('2d')
  const names = familyList(input)
    .split(', ')
    .filter((n) => n.startsWith('"'))
  if (!ctx || names.length === 0) return true
  const sample = 'mmmmmmmmmwwwwwww lli 1234 永和九年'
  const width = (font: string) => {
    ctx.font = `48px ${font}`
    return ctx.measureText(sample).width
  }
  return names.some((n) => ['monospace', 'serif', 'sans-serif'].some((g) => width(`${n}, ${g}`) !== width(g)))
}

// Chrome / Edge 桌面版能列出本机装的字体（要 HTTPS 或 localhost，会弹一次授权）
type LocalFontsWindow = { queryLocalFonts?: () => Promise<{ family: string }[]> }

export const canListLocalFonts = () => typeof (window as LocalFontsWindow).queryLocalFonts === 'function'

export async function listLocalFonts() {
  const fonts = (await (window as LocalFontsWindow).queryLocalFonts?.()) ?? []
  return [...new Set(fonts.map((f) => f.family))].sort((a, b) => a.localeCompare(b))
}
