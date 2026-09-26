import { useEffect, useState } from 'react'
import { api } from '@/lib/api'

// 这个项目的输入历史（来自 CLI 的 ~/.claude/history.jsonl，最新的在前），给输入框 ↑ / ↓ 用
export function useHistory(project: string | undefined) {
  const [history, setHistory] = useState<string[]>([])
  useEffect(() => {
    if (!project) return
    let alive = true
    api<string[]>(`/api/history?project=${encodeURIComponent(project)}`)
      .then((h) => alive && setHistory(h))
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [project])
  return history
}
