import type { Catalog, ModelInfo } from '@/lib/api'

// 模型在页面上一律显示具体是哪个：claude-opus-5-5[1m] → Opus 5.5 (1M)，不显示「Default」「opus」这种别名。
// 实际回复的模型可能和选的不一样（Opus 5.5 被降成 5、4.8 回），sameModel 比较时不看 [1m] 和日期后缀 ——
// 请求发的是 claude-opus-5-5[1m]，应答里写的是 claude-opus-5-5

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)

// 新格式 claude-opus-4-8、claude-haiku-4-5-20251001；老格式 claude-3-5-sonnet-20241022
const NEW = /^claude-([a-z]+)-(\d+)(?:-(\d{1,2}))?(?:-\d{8})?(\[1m\])?$/i
const OLD = /^claude-(\d+)(?:-(\d{1,2}))?-([a-z]+)(?:-\d{8})?(\[1m\])?$/i

// claudeName：认得出来的 claude 全名 → 人看的名字，认不出来返回 undefined
export function claudeName(id: string) {
  let m = NEW.exec(id)
  if (m) return `${cap(m[1])} ${m[3] ? `${m[2]}.${m[3]}` : m[2]}${m[4] ? ' (1M)' : ''}`
  m = OLD.exec(id)
  if (m) return `${cap(m[3])} ${m[2] ? `${m[1]}.${m[2]}` : m[1]}${m[4] ? ' (1M)' : ''}`
}

// modelName：全名 → 名字。claude 的按规则拼；codex / grok 的在目录里找它的 displayName，找不到就原样
export function modelName(id: string, catalog?: Catalog) {
  return claudeName(id) ?? catalog?.models?.find((m) => m.value === id || m.resolvedModel === id)?.displayName ?? id
}

// 目录里一个选项显示成什么：有全名的用全名拼（default 也显示成具体的模型），没有就用它自己的 displayName
export function optionName(m: ModelInfo) {
  return (m.resolvedModel && claudeName(m.resolvedModel)) || m.displayName
}

const base = (id: string) => id.replace(/\[[^\]]*\]$/, '').replace(/-\d{8}$/, '').toLowerCase()

export const sameModel = (a: string, b: string) => base(a) === base(b)
