import type { ITheme } from '@xterm/xterm'

// xterm.js 只认 #rrggbb / rgb()，主题变量是 oklch：借 canvas 换算一下
function cssVar(name: string) {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  const ctx = document.createElement('canvas').getContext('2d')
  if (!ctx || !v) return undefined
  ctx.fillStyle = v
  ctx.fillRect(0, 0, 1, 1)
  const [r, g, b] = ctx.getImageData(0, 0, 1, 1).data
  return `#${[r, g, b].map((x) => x.toString(16).padStart(2, '0')).join('')}`
}

// 浅色底上 ANSI「白色」要够深，不然程序输出的白字看不见
const LIGHT: ITheme = {
  black: '#1f1e1d',
  red: '#c0392b',
  green: '#2e7d32',
  yellow: '#9a6700',
  blue: '#1f5fbf',
  magenta: '#8e44ad',
  cyan: '#0e7c86',
  white: '#6e6d68',
  brightBlack: '#8a8983',
  brightRed: '#e5534b',
  brightGreen: '#3fa24c',
  brightYellow: '#b58407',
  brightBlue: '#3b82f6',
  brightMagenta: '#a855f7',
  brightCyan: '#14a3b8',
  brightWhite: '#3d3c38',
}
const DARK: ITheme = {
  black: '#3a3936',
  red: '#f07167',
  green: '#7ecf8a',
  yellow: '#e6c07b',
  blue: '#7aa2f7',
  magenta: '#c49bf2',
  cyan: '#6fd3dd',
  white: '#d9d7d0',
  brightBlack: '#8a8983',
  brightRed: '#ff8e87',
  brightGreen: '#9ee6a8',
  brightYellow: '#f2d28d',
  brightBlue: '#9bbcff',
  brightMagenta: '#d7b6ff',
  brightCyan: '#8ee6ee',
  brightWhite: '#f5f4ee',
}

// app：跟着页面主题（对话页右侧的 shell）；dark：固定深色（整页终端里跑 Claude TUI，它按深色终端配的色）
export function termTheme(follow: 'app' | 'dark'): ITheme {
  if (follow === 'dark') {
    return { ...DARK, background: '#1f1e1d', foreground: '#ecebe6', cursor: '#d97757', selectionBackground: '#4a4945' }
  }
  const dark = document.documentElement.classList.contains('dark')
  return {
    ...(dark ? DARK : LIGHT),
    background: cssVar('--background'),
    foreground: cssVar('--foreground'),
    cursor: cssVar('--primary'),
    cursorAccent: cssVar('--background'),
    selectionBackground: cssVar('--accent'),
  }
}
