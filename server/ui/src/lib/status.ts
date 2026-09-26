import { useSyncExternalStore } from 'react'

// 对话页把「上下文用量 / 当前在干嘛」发到这里，页面底部常驻的状态栏按当前路由取来显示

export type ContextUsage = {
  totalTokens?: number
  maxTokens?: number
  percentage?: number
  autoCompactThreshold?: number
  isAutoCompactEnabled?: boolean
  model?: string
}

export type ChatLive = { context?: ContextUsage; activity?: string }

const byChat = new Map<string, ChatLive>()
const subs = new Set<() => void>()

export function publishChat(id: string, v: ChatLive) {
  const old = byChat.get(id)
  if (old?.context === v.context && old?.activity === v.activity) return
  byChat.set(id, v)
  subs.forEach((f) => f())
}

export function useChatLive(id: string | undefined) {
  return useSyncExternalStore(
    (cb) => {
      subs.add(cb)
      return () => subs.delete(cb)
    },
    () => (id ? byChat.get(id) : undefined),
  )
}
