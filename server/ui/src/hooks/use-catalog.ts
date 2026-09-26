import { useEffect, useState } from 'react'
import type { Agent } from '@/lib/agents'
import { api, type Catalog } from '@/lib/api'

// 新对话还没开始时也要知道有哪些模型、斜杠命令。后端会起一个短命的 CLI 进程去问（要一两秒），
// 这里按 CLI + 项目缓存 Promise，同一组只问一次
const cache = new Map<string, Promise<Catalog>>()

export function useCatalog(project: string | undefined, agent: Agent = 'claude') {
  const [catalog, setCatalog] = useState<Catalog>()
  useEffect(() => {
    if (project === undefined) return
    let alive = true
    setCatalog(undefined) // 换了 CLI，上一个的模型列表不能留着
    const k = `${agent}\0${project}`
    let p = cache.get(k)
    if (!p) {
      p = api<Catalog>(`/api/catalog?project=${encodeURIComponent(project)}&agent=${agent}`)
      p.catch(() => cache.delete(k))
      cache.set(k, p)
    }
    p.then((c) => alive && setCatalog(c)).catch(() => {})
    return () => {
      alive = false
    }
  }, [project, agent])
  return catalog
}
