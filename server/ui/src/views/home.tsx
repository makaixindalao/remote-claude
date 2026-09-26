import { useEffect, useState } from 'react'
import { FolderIcon, MessageSquarePlusIcon, SquareTerminalIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardAction, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { useSidebar } from '@/components/ui/sidebar'
import { Skeleton } from '@/components/ui/skeleton'
import { AgentBadge } from '@/components/agent-badge'
import { PageHeader } from '@/components/page-header'
import { StatusDot, statusLabel } from '@/components/state-dot'
import { useData } from '@/hooks/use-data'
import { agentLabel, agentOf } from '@/lib/agents'
import { api, type Project, type SessionInfo } from '@/lib/api'
import { timeAgo } from '@/lib/format'
import { href, termName } from '@/lib/route'

// 首页：手机上侧栏收着，所以把三张列表在这里再摆一遍；另外把各项目最近的会话合成一张表放前面，
// 打开页面最常做的就是接着刚才那个会话

export function HomeView() {
  const { projects, chats, tmux, loaded, agents } = useData()
  const { isMobile, state } = useSidebar()
  const sidebarShown = !isMobile && state === 'expanded'
  const recent = useRecentSessions(projects, loaded)
  const chatBySession = new Map(chats.filter((c) => c.sessionId && c.state !== 'exited').map((c) => [c.sessionId, c]))

  return (
    <>
      <PageHeader title="remote-claude" subtitle={`VPS 上的 ${agents.map((a) => (a === 'claude' ? 'Claude Code' : agentLabel[a])).join(' / ')}`}>
        {/* 侧栏开着时顶上已经有「新对话」了，这里只在侧栏收起（手机、桌面折叠）时补一个 */}
        {!sidebarShown && (
          <Button size="sm" asChild>
            <a href={href.newChat()}>
              <MessageSquarePlusIcon /> 新对话
            </a>
          </Button>
        )}
      </PageHeader>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-5xl space-y-8 p-4 md:p-6">
          {chats.length > 0 && (
            <Section title="网页对话">
              {chats.map((c) => (
                <a key={c.id} href={href.chat(c.id)}>
                  <Card size="sm" className="transition-colors hover:bg-muted/50">
                    <CardHeader>
                      <CardTitle className="flex items-center gap-2 truncate">
                        <StatusDot status={c.status} />
                        <span className="truncate">{c.title || '新对话'}</span>
                        <AgentBadge agent={c.agent} />
                      </CardTitle>
                      <CardDescription>
                        {c.project} · {statusLabel[c.status]}
                      </CardDescription>
                    </CardHeader>
                  </Card>
                </a>
              ))}
            </Section>
          )}

          {recent === undefined && (
            <Section title="最近会话" list>
              {[0, 1, 2].map((i) => (
                <Skeleton key={i} className="h-15 rounded-none" />
              ))}
            </Section>
          )}
          {!!recent?.length && (
            <Section title="最近会话" list>
              {recent.map((s) => {
                const chat = chatBySession.get(s.id)
                return (
                  <a
                    key={`${s.project}/${s.id}`}
                    href={chat ? href.chat(chat.id) : href.session(s.project, s.id, agentOf(s.agent))}
                    className="flex items-center gap-3 px-4 py-3 transition-colors hover:bg-muted/50"
                  >
                    {chat && <StatusDot status={chat.status} />}
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-1.5 text-sm">
                        <AgentBadge agent={s.agent} />
                        <span className="truncate">{s.title || <span className="text-muted-foreground">（无标题）</span>}</span>
                      </div>
                      <div className="mt-0.5 flex items-center gap-2 text-xs text-muted-foreground">
                        <span className="truncate">{s.project}</span>
                        <span className="shrink-0">{timeAgo(s.mtime)}</span>
                        {chat && <span className="shrink-0 text-foreground">{statusLabel[chat.status]}</span>}
                      </div>
                    </div>
                  </a>
                )
              })}
            </Section>
          )}

          {tmux.length > 0 && (
            <Section title="终端 · tmux">
              {tmux.map((t) => (
                <a key={t.name} href={href.term(t.name, { mode: 'attach' })}>
                  <Card size="sm" className="transition-colors hover:bg-muted/50">
                    <CardHeader>
                      <CardTitle className="flex items-center gap-2">
                        <SquareTerminalIcon className="size-4 text-muted-foreground" />
                        <span className="truncate">{t.name}</span>
                      </CardTitle>
                      <CardDescription className="truncate">
                        {t.command} · {t.path}
                      </CardDescription>
                      <CardAction>{t.attached > 0 && <Badge variant="secondary">{t.attached} 处连着</Badge>}</CardAction>
                    </CardHeader>
                  </Card>
                </a>
              ))}
            </Section>
          )}

          <Section title="项目">
            {!loaded && [0, 1, 2].map((i) => <Skeleton key={i} className="h-28 rounded-xl" />)}
            {loaded && projects.length === 0 && <p className="text-sm text-muted-foreground">没有找到项目。检查 RCWEB_ROOT / RCWEB_PROJECTS。</p>}
            {projects.map((p) => (
              <Card key={p.name} size="sm">
                <CardHeader>
                  <CardTitle className="flex items-center gap-2">
                    <FolderIcon className="size-4 text-muted-foreground" />
                    <a href={href.project(p.name)} className="truncate hover:underline">
                      {p.name}
                    </a>
                  </CardTitle>
                  <CardDescription>
                    {p.exists ? `${p.sessions} 个会话${p.lastActive ? ` · ${timeAgo(p.lastActive)}` : ''}` : '目录不存在'}
                  </CardDescription>
                </CardHeader>
                <div className="flex gap-2 px-3">
                  <Button variant="outline" size="sm" asChild>
                    <a href={href.project(p.name)}>会话记录</a>
                  </Button>
                  <Button variant="outline" size="sm" disabled={!p.exists} asChild={p.exists}>
                    {p.exists ? <a href={href.newChat(p.name)}>新对话</a> : <span>新对话</span>}
                  </Button>
                  <Button variant="outline" size="sm" disabled={!p.exists} asChild={p.exists}>
                    {p.exists ? <a href={href.term(termName(p.path), { project: p.name, mode: 'claude' })}>终端</a> : <span>终端</span>}
                  </Button>
                </div>
              </Card>
            ))}
          </Section>
        </div>
      </div>
    </>
  )
}

function Section({ title, list, children }: { title: string; list?: boolean; children: React.ReactNode }) {
  return (
    <section className="space-y-3">
      <h2 className="text-sm font-medium text-muted-foreground">{title}</h2>
      <div className={list ? 'divide-y overflow-hidden rounded-xl border bg-card' : 'grid gap-3 sm:grid-cols-2 lg:grid-cols-3'}>{children}</div>
    </section>
  )
}

const RECENT = 8

// 每个项目各取最近 8 个再合起来排。只在某个项目的 lastActive 变了时重拉，不跟着每 10 秒的刷新一起打一遍
function useRecentSessions(projects: Project[], loaded: boolean) {
  const [recent, setRecent] = useState<(SessionInfo & { project: string })[]>()
  const key = projects
    .filter((p) => p.exists && p.sessions > 0)
    .map((p) => `${p.name}\n${p.lastActive}`)
    .join('\n\n')

  useEffect(() => {
    if (!loaded) return
    const names = key ? key.split('\n\n').map((k) => k.split('\n')[0]) : []
    let stale = false
    Promise.allSettled(
      names.map((p) =>
        api<SessionInfo[]>(`/api/sessions?project=${encodeURIComponent(p)}&limit=${RECENT}&filter=active`).then((l) => l.map((s) => ({ ...s, project: p }))),
      ),
    ).then((got) => {
      if (stale) return
      const all = got.flatMap((r) => (r.status === 'fulfilled' ? r.value : []))
      setRecent(all.sort((a, b) => b.mtime - a.mtime).slice(0, RECENT))
    })
    return () => {
      stale = true
    }
  }, [key, loaded])

  return recent
}
