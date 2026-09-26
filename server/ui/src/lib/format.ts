export function timeAgo(ms: number) {
  if (!ms) return ''
  const s = Math.max(0, (Date.now() - ms) / 1000)
  if (s < 60) return '刚刚'
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`
  if (s < 86400) return `${Math.floor(s / 3600)} 小时前`
  if (s < 86400 * 30) return `${Math.floor(s / 86400)} 天前`
  return new Date(ms).toLocaleDateString()
}

export function fileSize(n: number) {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

export function shortPath(p: string | undefined, cwd?: string) {
  if (!p) return ''
  if (cwd && p.startsWith(cwd + '/')) return p.slice(cwd.length + 1)
  return p
}

export const isTouch = () => window.matchMedia('(pointer: coarse)').matches
