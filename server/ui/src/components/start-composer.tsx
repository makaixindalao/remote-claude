import { useState } from 'react'
import { toast } from 'sonner'
import { Composer, effectiveEffort } from '@/components/composer'
import { useCatalog } from '@/hooks/use-catalog'
import { useData } from '@/hooks/use-data'
import { useHistory } from '@/hooks/use-history'
import type { Agent } from '@/lib/agents'
import { api, type ChatInfo } from '@/lib/api'
import { loadAgent, newChatSettings, rememberSettings, saveAgent, saveProject, useChatPrefs, type Settings } from '@/lib/prefs'
import { href, navigate } from '@/lib/route'

// 还没有 CLI 进程时的输入框：发第一条消息 = 起一个对话（给了 sessionId 就是续那个会话）。
// 续会话时 agent 由会话定死；新对话可以在输入框下面选 Claude / Codex / Grok，默认上次用的
export function StartComposer({
  project,
  sessionId,
  agent: fixed,
  placeholder,
}: {
  project?: string
  sessionId?: string
  agent?: Agent
  placeholder?: string
}) {
  const { refresh, agents } = useData()
  const [picked, setPicked] = useState<Agent>(loadAgent)
  // 记住的 CLI 可能在这台 VPS 上没装
  const agent = fixed ?? (agents.includes(picked) ? picked : 'claude')
  const catalog = useCatalog(project, agent)
  const history = useHistory(project)
  // 模型这些按 CLI 各记各的；装了哪些 CLI、设置里的默认值都是异步问来的，所以按当下的 agent 取，不在初始化时定死
  const prefs = useChatPrefs()
  const [edited, setEdited] = useState<Partial<Record<Agent, Settings>>>({})
  const settings = edited[agent] ?? newChatSettings(agent, prefs?.[agent])

  function onAgent(a: Agent) {
    setPicked(a)
    saveAgent(a)
  }

  function onSettings(s: Settings) {
    setEdited((e) => ({ ...e, [agent]: s }))
    rememberSettings(s, agent)
  }

  async function onSend(prompt: string, attachments: string[]) {
    if (!project) {
      toast.error('先选一个项目')
      return false
    }
    try {
      const chat = await api<ChatInfo>('/api/chats', {
        method: 'POST',
        body: {
          agent,
          project,
          sessionId,
          prompt,
          attachments,
          model: settings.model,
          effort: effectiveEffort(catalog, settings),
          permissionMode: settings.mode,
        },
      })
      saveProject(project)
      rememberSettings(settings, agent) // 没动过下拉框也算「上次用的」
      refresh()
      navigate(href.chat(chat.id))
      return true
    } catch (e) {
      toast.error((e as Error).message)
      return false
    }
  }

  return (
    <Composer
      agent={agent}
      agents={agents}
      onAgent={fixed ? undefined : onAgent}
      catalog={catalog}
      settings={settings}
      onSettings={onSettings}
      onSend={onSend}
      disabled={!project}
      allowDefaultEffort
      placeholder={placeholder}
      history={history}
      autoFocus
    />
  )
}
