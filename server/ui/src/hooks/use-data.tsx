import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { toast } from 'sonner'
import { AttentionSignals } from '@/components/attention'
import { agentOf, type Agent } from '@/lib/agents'
import { api, type ChatInfo, type Project, type SessionInfo, type TmuxSession } from '@/lib/api'

// 侧栏和首页共用的数据：项目、网页对话、tmux 会话，以及侧栏里展开了的项目各自的会话列表。
// 页面可见时每 10 秒全刷一次；在后台时只拉对话列表，好让标签页标题 / 通知知道有对话在等你。

export type SessionFilter = 'active' | 'archived' | 'all'

type Data = {
  projects: Project[]
  chats: ChatInfo[]
  tmux: TmuxSession[]
  sessions: Record<string, SessionInfo[]> // 只有展开了的项目才有，按 sessionFilter 筛过
  expanded: Set<string>
  sessionFilter: SessionFilter
  setSessionFilter: (f: SessionFilter) => void
  loaded: boolean
  refresh: () => Promise<void>
  toggleProject: (name: string) => void
  agents: Agent[] // VPS 上装了的 CLI
  archiveSession: (project: string, s: Pick<SessionInfo, 'id' | 'agent'>, archived: boolean) => Promise<void>
  deleteSession: (project: string, s: Pick<SessionInfo, 'id' | 'title' | 'agent'>) => Promise<boolean>
}

const DataContext = createContext<Data | null>(null)
const EXPANDED_KEY = 'rcweb.expanded'

function loadExpanded() {
  try {
    return new Set<string>(JSON.parse(localStorage.getItem(EXPANDED_KEY) ?? '[]'))
  } catch {
    return new Set<string>()
  }
}

export function DataProvider({ children }: { children: ReactNode }) {
  const [projects, setProjects] = useState<Project[]>([])
  const [chats, setChats] = useState<ChatInfo[]>([])
  const [tmux, setTmux] = useState<TmuxSession[]>([])
  const [sessions, setSessions] = useState<Record<string, SessionInfo[]>>({})
  const [expanded, setExpanded] = useState(loadExpanded)
  const [loaded, setLoaded] = useState(false)
  const [agents, setAgents] = useState<Agent[]>(['claude'])
  const [sessionFilter, setFilter] = useState<SessionFilter>('active')
  const expandedRef = useRef(expanded)
  expandedRef.current = expanded
  const filterRef = useRef(sessionFilter)

  const loadSessions = useCallback(async (names: string[]) => {
    const filter = filterRef.current
    const got = await Promise.allSettled(
      names.map((p) =>
        api<SessionInfo[]>(`/api/sessions?project=${encodeURIComponent(p)}&limit=15&filter=${filter}`).then((l) => [p, l] as const),
      ),
    )
    if (filter !== filterRef.current) return // 等的时候换了筛选，这批作废
    setSessions((old) => {
      const next = { ...old }
      for (const r of got) if (r.status === 'fulfilled') next[r.value[0]] = r.value[1]
      return next
    })
  }, [])

  const refresh = useCallback(async () => {
    const [p, c, t] = await Promise.allSettled([
      api<Project[]>('/api/projects'),
      api<ChatInfo[]>('/api/chats'),
      api<TmuxSession[]>('/api/tmux'),
      loadSessions([...expandedRef.current]),
    ])
    if (p.status === 'fulfilled') setProjects(p.value)
    if (c.status === 'fulfilled') setChats(c.value)
    if (t.status === 'fulfilled') setTmux(t.value)
    setLoaded(true)
  }, [loadSessions])

  // 装了哪些 CLI 不会变，登录后问一次
  useEffect(() => {
    api<{ agents?: string[] }>('/api/me')
      .then((me) => me.agents?.length && setAgents(me.agents.map(agentOf)))
      .catch(() => {})
  }, [])

  const refreshChats = useCallback(() => {
    api<ChatInfo[]>('/api/chats')
      .then(setChats)
      .catch(() => {})
  }, [])

  useEffect(() => {
    refresh()
    const timer = setInterval(() => (document.visibilityState === 'visible' ? refresh() : refreshChats()), 10_000)
    const onVisible = () => document.visibilityState === 'visible' && refresh()
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      clearInterval(timer)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [refresh, refreshChats])

  const toggleProject = useCallback(
    (name: string) => {
      setExpanded((old) => {
        const next = new Set(old)
        if (next.has(name)) next.delete(name)
        else {
          next.add(name)
          loadSessions([name])
        }
        try {
          localStorage.setItem(EXPANDED_KEY, JSON.stringify([...next]))
        } catch {
          /* 记不住展开状态而已 */
        }
        return next
      })
    },
    [loadSessions],
  )

  const setSessionFilter = useCallback(
    (f: SessionFilter) => {
      if (f === filterRef.current) return
      filterRef.current = f
      setFilter(f)
      setSessions({}) // 旧列表是另一种筛选的，清掉先出骨架
      loadSessions([...expandedRef.current])
    },
    [loadSessions],
  )

  const archiveSession = useCallback(
    async function archive(project: string, s: Pick<SessionInfo, 'id' | 'agent'>, archived: boolean) {
      try {
        await api('/api/session/archive', { method: 'POST', body: { project, agent: s.agent, id: s.id, archived } })
        toast.success(archived ? '已归档' : '已取消归档', {
          action: archived ? { label: '撤销', onClick: () => void archive(project, s, false) } : undefined,
        })
        refresh()
      } catch (e) {
        toast.error((e as Error).message)
      }
    },
    [refresh],
  )

  const deleteSession = useCallback(
    async (project: string, s: Pick<SessionInfo, 'id' | 'title' | 'agent'>) => {
      const agent = agentOf(s.agent)
      const where = `文件会挪到 ~/.${agent}/.rcweb-trash/，还能找回来${agent === 'claude' ? '；rcsync 会把删除同步到另一端' : ''}。`
      if (!confirm(`删除会话「${s.title || s.id.slice(0, 8)}」？\n\n${where}`)) return false
      try {
        await api('/api/session/delete', { method: 'POST', body: { project, agent, id: s.id } })
        toast.success('已删除')
        refresh()
        return true
      } catch (e) {
        toast.error((e as Error).message)
        return false
      }
    },
    [refresh],
  )

  const value = useMemo(
    () => ({
      projects,
      chats,
      tmux,
      sessions,
      expanded,
      sessionFilter,
      setSessionFilter,
      loaded,
      agents,
      refresh,
      toggleProject,
      archiveSession,
      deleteSession,
    }),
    [projects, chats, tmux, sessions, expanded, sessionFilter, setSessionFilter, loaded, agents, refresh, toggleProject, archiveSession, deleteSession],
  )
  return (
    <DataContext.Provider value={value}>
      <AttentionSignals chats={chats} loaded={loaded} />
      {children}
    </DataContext.Provider>
  )
}

export function useData() {
  const d = useContext(DataContext)
  if (!d) throw new Error('useData 必须在 DataProvider 里用')
  return d
}
