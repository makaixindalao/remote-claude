import { useCallback, useEffect, useRef, useState } from 'react'
import { ArchiveIcon, ArchiveRestoreIcon, ChevronDownIcon, EllipsisIcon, GitBranchIcon, SquareTerminalIcon, Trash2Icon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Skeleton } from '@/components/ui/skeleton'
import { AgentBadge } from '@/components/agent-badge'
import { NotFound } from '@/components/not-found'
import { PageHeader } from '@/components/page-header'
import { StartComposer } from '@/components/start-composer'
import { StatusDot, statusLabel } from '@/components/state-dot'
import { useData } from '@/hooks/use-data'
import { AGENTS, agentLabel, agentOf, type Agent } from '@/lib/agents'
import { api, type SessionInfo } from '@/lib/api'
import { timeAgo } from '@/lib/format'
import { href, termName } from '@/lib/route'
import { cn } from '@/lib/utils'

type Filter = 'active' | 'archived' | 'all'

export function ProjectView({ project }: { project: string }) {
  const { projects, chats, agents, loaded, archiveSession, deleteSession } = useData()
  const p = projects.find((x) => x.name === project)
  const [filter, setFilter] = useState<Filter>('active')
  const [only, setOnly] = useState<Agent | 'all'>('all')
  const [sessions, setSessions] = useState<SessionInfo[] | null>(null)
  const [error, setError] = useState('')
  const chatBySession = new Map(chats.filter((c) => c.sessionId && c.state !== 'exited').map((c) => [c.sessionId, c]))

  const load = useCallback(() => {
    setError('')
    api<SessionInfo[]>(`/api/sessions?project=${encodeURIComponent(project)}&limit=500&filter=${filter}`)
      .then(setSessions)
      .catch((e) => setError(e.message))
  }, [project, filter])

  useEffect(() => {
    setSessions(null)
    load()
  }, [load])

  // 归档后点了「撤销」、或者别处删了会话，项目的计数会变：跟着重拉一次（不清空，免得闪骨架）
  const counts = p ? `${p.sessions}/${p.archived}` : ''
  const lastCounts = useRef(counts)
  useEffect(() => {
    const prev = lastCounts.current
    lastCounts.current = counts
    if (prev && counts && prev !== counts) load()
  }, [counts, load])

  const filters: [Filter, string, number | undefined][] = [
    ['active', '活跃', p?.sessions],
    ['archived', '已归档', p?.archived],
    ['all', '全部', p ? p.sessions + p.archived : undefined],
  ]
  // 这个项目里有不止一种 CLI 的会话时，才给按 CLI 筛的开关
  const used = AGENTS.filter((a) => p?.agents?.[a])
  const shown = sessions?.filter((s) => only === 'all' || agentOf(s.agent) === only)

  return (
    <>
      <PageHeader title={project} subtitle={p?.path}>
        {p?.exists && agents.length > 1 ? (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button size="sm" variant="outline">
                <SquareTerminalIcon /> <span className="hidden sm:inline">终端</span>
                <ChevronDownIcon />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              {agents.map((a) => (
                <DropdownMenuItem key={a} asChild>
                  <a href={href.term(termName(p.path, undefined, a), { project, mode: a })}>在终端里跑 {agentLabel[a]}</a>
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        ) : (
          // 还没加载到、或者根本没有这个项目时不给按钮；项目在但目录没了才显示成灰的
          p && (
            <Button size="sm" variant="outline" disabled={!p?.exists} asChild={!!p?.exists}>
              {p?.exists ? (
                <a href={href.term(termName(p.path), { project, mode: 'claude' })}>
                  <SquareTerminalIcon /> <span className="hidden sm:inline">终端</span>
                </a>
              ) : (
                <span>终端</span>
              )}
            </Button>
          )
        )}
      </PageHeader>
      {loaded && !p ? (
        <NotFound
          title={`找不到项目「${project}」`}
          actions={
            <Button asChild>
              <a href={href.home()}>回首页</a>
            </Button>
          }
        >
          它可能不在 RCWEB_ROOT 下，或者目录被改名、删掉了。
        </NotFound>
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="mx-auto max-w-3xl p-4 md:p-6">
            <div className="mb-4 inline-flex rounded-lg bg-muted p-0.5">
              {filters.map(([f, label, n]) => (
                <button
                  key={f}
                  onClick={() => setFilter(f)}
                  className={cn(
                    'rounded-md px-3 py-1 text-sm text-muted-foreground transition-colors',
                    filter === f && 'bg-background font-medium text-foreground shadow-xs',
                  )}
                >
                  {label}
                  {n !== undefined && <span className="ml-1 text-xs tabular-nums opacity-60">{n}</span>}
                </button>
              ))}
            </div>
            {used.length > 1 && (
              <div className="mb-4 ml-2 inline-flex rounded-lg bg-muted p-0.5">
                {(['all', ...used] as const).map((a) => (
                  <button
                    key={a}
                    onClick={() => setOnly(a)}
                    className={cn('rounded-md px-3 py-1 text-sm text-muted-foreground transition-colors', only === a && 'bg-background font-medium text-foreground shadow-xs')}
                  >
                    {a === 'all' ? '全部' : agentLabel[a]}
                    {a !== 'all' && <span className="ml-1 text-xs tabular-nums opacity-60">{p?.agents?.[a]}</span>}
                  </button>
                ))}
              </div>
            )}

            {error && <p className="text-sm text-destructive">{error}</p>}
            {!sessions && !error && (
              <div className="space-y-2">
                {[0, 1, 2, 3].map((i) => (
                  <Skeleton key={i} className="h-14 rounded-lg" />
                ))}
              </div>
            )}
            {shown?.length === 0 && (
              <p className="text-sm text-muted-foreground">{filter === 'archived' ? '没有归档的会话。' : '这里还没有会话记录。'}</p>
            )}
            {!!shown?.length && (
              <div className="divide-y rounded-xl border bg-card">
                {shown.map((s) => {
                  const chat = chatBySession.get(s.id)
                  return (
                    <div key={s.id} className="group flex items-center gap-2 pr-2 transition-colors first:rounded-t-xl last:rounded-b-xl hover:bg-muted/50">
                      <a
                        href={chat ? href.chat(chat.id) : href.session(project, s.id, agentOf(s.agent))}
                        title={`会话 ${s.id}`}
                        className="flex min-w-0 flex-1 items-center gap-3 py-3 pl-4"
                      >
                        {chat && <StatusDot status={chat.status} />}
                        <div className="min-w-0 flex-1">
                          <div className={cn('flex items-center gap-1.5 text-sm', s.archived && 'text-muted-foreground')}>
                            <AgentBadge agent={s.agent} />
                            <span className="truncate">{s.title || <span className="text-muted-foreground">（无标题）</span>}</span>
                          </div>
                          <div className="mt-0.5 flex items-center gap-2 text-xs text-muted-foreground">
                            <span>{timeAgo(s.mtime)}</span>
                            {chat && <span className="text-foreground">{statusLabel[chat.status]}</span>}
                          </div>
                        </div>
                        {s.worktree && (
                          <Badge variant="outline" className="gap-1">
                            <GitBranchIcon /> {s.worktree}
                          </Badge>
                        )}
                        {s.archived && <Badge variant="secondary">已归档</Badge>}
                      </a>
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <Button variant="ghost" size="icon-sm" aria-label="会话操作" className="opacity-60 group-hover:opacity-100">
                            <EllipsisIcon />
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem onClick={() => archiveSession(project, s, !s.archived).then(load)}>
                            {s.archived ? <ArchiveRestoreIcon /> : <ArchiveIcon />}
                            {s.archived ? '取消归档' : '归档'}
                          </DropdownMenuItem>
                          <DropdownMenuItem variant="destructive" onClick={() => deleteSession(project, s).then((ok) => ok && load())}>
                            <Trash2Icon /> 删除
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        </div>
      )}
      {p?.exists && <StartComposer project={project} placeholder="在这个项目里开个新对话（/ 看命令）" />}
    </>
  )
}
