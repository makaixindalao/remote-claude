import type { HLJSApi } from 'highlight.js'

// 代码高亮和 mermaid 都按需加载：消息里真有代码块 / 流程图、或者打开了文件查看才下载

let hljs: Promise<HLJSApi> | null = null
export const loadHljs = () => (hljs ??= import('highlight.js/lib/common').then((m) => m.default))

type Mermaid = (typeof import('mermaid'))['default']
let mermaid: Promise<Mermaid> | null = null
export const loadMermaid = () => (mermaid ??= import('mermaid').then((m) => m.default))

const BY_EXT: Record<string, string> = {
  go: 'go',
  ts: 'typescript',
  tsx: 'typescript',
  mts: 'typescript',
  js: 'javascript',
  jsx: 'javascript',
  mjs: 'javascript',
  cjs: 'javascript',
  py: 'python',
  rb: 'ruby',
  rs: 'rust',
  java: 'java',
  kt: 'kotlin',
  kts: 'kotlin',
  swift: 'swift',
  c: 'c',
  h: 'c',
  cc: 'cpp',
  cpp: 'cpp',
  hpp: 'cpp',
  cs: 'csharp',
  php: 'php',
  lua: 'lua',
  sh: 'bash',
  bash: 'bash',
  zsh: 'bash',
  md: 'markdown',
  mdx: 'markdown',
  json: 'json',
  jsonl: 'json',
  yaml: 'yaml',
  yml: 'yaml',
  toml: 'ini',
  ini: 'ini',
  conf: 'ini',
  cfg: 'ini',
  env: 'bash',
  xml: 'xml',
  html: 'xml',
  htm: 'xml',
  vue: 'xml',
  svg: 'xml',
  plist: 'xml',
  css: 'css',
  scss: 'scss',
  less: 'less',
  sql: 'sql',
  graphql: 'graphql',
  diff: 'diff',
  mod: 'go',
  r: 'r',
  pl: 'perl',
  makefile: 'makefile',
  mk: 'makefile',
}

export function langFromPath(path: string) {
  const name = path.split('/').pop()?.toLowerCase() ?? ''
  if (name === 'makefile') return 'makefile'
  if (name === 'dockerfile') return 'bash'
  return BY_EXT[name.split('.').pop() ?? '']
}
