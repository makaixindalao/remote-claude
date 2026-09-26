import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { ArchiveIcon, ArchiveRestoreIcon, EllipsisVerticalIcon, MessageSquareIcon, SquareTerminalIcon, Trash2Icon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Skeleton } from '@/components/ui/skeleton'
import { LoadOlder } from '@/components/load-older'
import { NotFound } from '@/components/not-found'
import { PageHeader } from '@/components/page-header'
import { StartComposer } from '@/components/start-composer'
import { SidePanel, useSidePanelOpen } from '@/components/side-panel'
import { Transcript } from '@/components/transcript'
import { ViewToggles } from '@/components/view-toggles'
import { useData } from '@/hooks/use-data'
import { useLoadOlder } from '@/hooks/use-load-older'
import { agentLabel, agentOf } from '@/lib/agents'
import { api, type Page, type SessionInfo } from '@/lib/api'
import { timeAgo } from '@/lib/format'
import { href, navigate, termName } from '@/lib/route'
import { toolDetails } from '@/lib/tool-details'
import { useView } from '@/lib/view'
import { cn } from '@/lib/utils'

// 只拿最后一页、工具只带摘要（见 ../../../session_page.go）；往上翻再要更早的，展开工具卡片再取全文
type Data = Page & { session: SessionInfo; truncated: boolean }

export function SessionView({ project, id, agent: agentParam }: { project: string; id: string; agent?: string }) {
  const agent = agentOf(agentParam)
  const [data, setData] = useState<Data | null>(null)
  const [error, setError] = useState('')
  const scrollRef = useRef<HTMLDivElement>(null)
  const { projects, archiveSession, deleteSession } = useData()
  const view = useView()

  const query = `project=${encodeURIComponent(project)}&agent=${agent}&id=${encodeURIComponent(id)}`
  const tools = useMemo(() => toolDetails(`/api/session/tools?${query}&`), [query])

  useEffect(() => {
    setData(null)
    api<Data>(`/api/session?${query}`)
      .then(setData)
      .catch((e) => setError(e.message))
  }, [query])

  const older = useLoadOlder(scrollRef, data?.start, !!data?.more, async (before) => {
    const d = await api<Data>(`/api/session?${query}&before=${before}`)
    setData((old) =>
      old && old.start === before ? { ...old, entries: [...d.entries, ...old.entries], start: d.start, more: d.more } : old,
    )
  })

  // 会话记录一般是来接着干的，打开就落在最底下（只在刚打开时；往上翻加载的那几页不动）
  const landed = useRef(false)
  useLayoutEffect(() => {
    if (!data || landed.current || !scrollRef.current) return
    landed.current = true
    scrollRef.current.scrollTop = scrollRef.current.scrollHeight
  }, [data])

  const s = data?.session
  const root = cwdRoot(projects, project)
  const sessionCwd = root ? (s?.cwd?.startsWith(root) ? s.cwd : root) : undefined
  const panelOpen = useSidePanelOpen(!!sessionCwd)
  async function toggleArchive() {
    if (!s) return
    await archiveSession(project, s, !s.archived)
    setData((d) => d && { ...d, session: { ...d.session, archived: !s.archived } })
  }
  return (
    <>
      <PageHeader
        title={s?.title || '会话记录'}
        subtitle={
          s && [agent !== 'claude' && agentLabel[agent], project, s.worktree && `worktree ${s.worktree}`, timeAgo(s.mtime)].filter(Boolean).join(' · ')
        }
      >
        {/* 会话还没拿到（或者不存在）时，页头不给任何针对它的操作 */}
        {s?.chatId ? (
          <Button size="sm" asChild>
            <a href={href.chat(s.chatId)}>
              <MessageSquareIcon /> 网页里正在跑
            </a>
          </Button>
        ) : (
          s && (
            <Button size="sm" variant="outline" asChild>
              <a href={href.term(termName(project, id, agent), { project, mode: agent, resume: id })} aria-label="终端里继续">
                <SquareTerminalIcon /> <span className="hidden sm:inline">终端里继续</span>
              </a>
            </Button>
          )
        )}
        {s && <ViewToggles />}
        {s && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon-sm" aria-label="会话操作">
                <EllipsisVerticalIcon />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={toggleArchive}>
                {s.archived ? <ArchiveRestoreIcon /> : <ArchiveIcon />}
                {s.archived ? '取消归档' : '归档'}
              </DropdownMenuItem>
              <DropdownMenuItem variant="destructive" onClick={() => deleteSession(project, s).then((ok) => ok && navigate(href.project(project)))}>
                <Trash2Icon /> 删除
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </PageHeader>
      {error ? (
        <NotFound
          title="打不开这个会话"
          actions={
            <>
              <Button asChild>
                <a href={href.project(project)}>回到 {project}</a>
              </Button>
              <Button variant="outline" asChild>
                <a href={href.home()}>回首页</a>
              </Button>
            </>
          }
        >
          {error}。可能已经被删除、挪走，或者链接不完整。
        </NotFound>
      ) : (
        <div className="flex min-h-0 flex-1">
          <div className={cn('flex min-w-0 flex-1 flex-col', panelOpen && 'max-md:hidden')}>
            <div ref={scrollRef} className="min-h-0 flex-1 overflow-y-auto">
              <div className="mx-auto flex max-w-3xl flex-col gap-3 px-4 py-5">
                {!data && [0, 1, 2].map((i) => <Skeleton key={i} className="h-16 rounded-lg" />)}
                {data && <LoadOlder more={data.more} loading={older.loading} truncated={data.truncated} onLoad={older.trigger} />}
                {data && (
                  <Transcript
                    entries={data.entries}
                    offset={data.start}
                    tools={tools}
                    cwd={s?.cwd}
                    thinkingOpen={view.thinking}
                    toolMode={view.tools}
                  />
                )}
              </div>
            </div>
            {data && !s?.chatId && <StartComposer project={project} sessionId={id} agent={agent} placeholder="接着这个会话说（/ 看命令）" />}
          </div>
          {s && <SidePanel cwd={sessionCwd} project={project} />}
        </div>
      )}
    </>
  )
}

// 会话记录里的 cwd 可能是另一台机器的路径（rcsync 同步过来的），不在这台机器上就用项目目录
function cwdRoot(projects: { name: string; path: string }[], project: string) {
  return projects.find((p) => p.name === project)?.path ?? ''
}
