import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import {
  ArrowDownIcon,
  EllipsisVerticalIcon,
  Loader2Icon,
  RotateCcwIcon,
  SquareIcon,
  SquareTerminalIcon,
  TriangleAlertIcon,
  XIcon,
} from 'lucide-react'
import { toast } from 'sonner'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Composer, type WebCommand } from '@/components/composer'
import { LoadOlder } from '@/components/load-older'
import { NotFound } from '@/components/not-found'
import { PageHeader } from '@/components/page-header'
import { PermissionCard } from '@/components/permission-card'
import { ClaudeSpinner, StatusDot, statusLabel, useBeat } from '@/components/state-dot'
import { SidePanel, useSidePanelOpen } from '@/components/side-panel'
import { Transcript } from '@/components/transcript'
import { ViewToggles } from '@/components/view-toggles'
import { useChat } from '@/hooks/use-chat'
import { useLoadOlder } from '@/hooks/use-load-older'
import { useData } from '@/hooks/use-data'
import { useHistory } from '@/hooks/use-history'
import { agentLabel, agentOf, defaultMode } from '@/lib/agents'
import { api, type BgTask, type ChatInfo, type Entry } from '@/lib/api'
import { modelName } from '@/lib/models'
import { rememberSettings, type Settings } from '@/lib/prefs'
import { href, navigate, termName } from '@/lib/route'
import { useView } from '@/lib/view'
import { cn } from '@/lib/utils'

export function ChatView({ id }: { id: string }) {
  const chat = useChat(id)
  const { refresh } = useData()
  const { info, entries, pending, live, conn, result, stderr } = chat
  const agent = agentOf(info?.agent)
  const who = agentLabel[agent]
  const history = useHistory(info?.project)
  const view = useView()
  const panelOpen = useSidePanelOpen(!!info)
  const scrollRef = useRef<HTMLDivElement>(null)
  const stick = useRef(true)
  const [atBottom, setAtBottom] = useState(true)

  // 侧栏的状态点每 10 秒才轮询一次，本页知道得更早，状态一变就让它刷新
  useEffect(() => {
    if (info) refresh()
  }, [info?.status, info?.title, refresh])

  // 页面开着、人看得见，「已完成」就算读过了
  const status = info?.status
  const seen = chat.seen
  useEffect(() => {
    if (status !== 'done') return
    const mark = () => document.visibilityState === 'visible' && seen()
    mark()
    document.addEventListener('visibilitychange', mark)
    return () => document.removeEventListener('visibilitychange', mark)
  }, [status, seen])

  // 人在底部时跟着新内容往下滚；往上翻看历史时不打扰
  useLayoutEffect(() => {
    const el = scrollRef.current
    if (el && stick.current) el.scrollTop = el.scrollHeight
  }, [entries, live, pending, result])

  // 快照只带了最后一页，往上翻到顶再要更早的
  const older = useLoadOlder(scrollRef, info && chat.start, chat.more, chat.loadOlder)

  const onScroll = () => {
    const el = scrollRef.current
    if (!el) return
    const bottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80
    stick.current = bottom
    setAtBottom(bottom)
  }
  const toBottom = () => {
    const el = scrollRef.current
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
  }

  const running = info?.state === 'running'
  const exited = info?.state === 'exited'

  async function restart() {
    if (!info) return
    try {
      const c = await api<ChatInfo>('/api/chats', {
        method: 'POST',
        body: {
          agent,
          project: info.project,
          sessionId: info.sessionId,
          permissionMode: info.permissionMode,
          model: info.model,
          effort: info.effort,
        },
      })
      refresh()
      navigate(href.chat(c.id))
    } catch (e) {
      toast.error((e as Error).message)
    }
  }

  async function close(ask = true) {
    if (ask && !confirm(`结束这个对话？${agent} 进程会被停掉，会话记录还在，之后可以从项目里继续。`)) return false
    try {
      await api(`/api/chats/${id}`, { method: 'DELETE' })
      refresh()
      return true
    } catch (e) {
      toast.error((e as Error).message)
      return false
    }
  }

  // 同一个会话不能两个进程同时写：先结束这里的，再到终端里续（claude / grok 是 --resume，codex 是 resume）
  async function toTerminal() {
    if (!info?.sessionId) return toast.error('还没有会话记录，先发条消息')
    if (!confirm(`在网页终端里接着这个会话？这里的 ${agent} 进程会先结束。`)) return
    if (await close(false))
      navigate(href.term(termName(info.project, info.sessionId, agent), { project: info.project, mode: agent, resume: info.sessionId }))
  }

  const webCommands = useMemo<WebCommand[]>(
    () => [
      { name: 'exit', description: `结束这个对话（停掉 ${agent} 进程，会话记录还在）`, run: () => close().then((ok) => ok && navigate(href.home())) },
      { name: 'resume', description: '打开这个项目的会话列表', run: () => info && navigate(href.project(info.project)) },
      { name: 'terminal', description: '到网页终端里接着这个会话（TUI，所有交互命令都能用）', run: toTerminal },
    ],
    [info],
  )

  const settings: Settings = { model: info?.model || 'default', effort: info?.effort ?? '', mode: info?.permissionMode ?? defaultMode[agent] }
  function onSettings(next: Settings) {
    if (next.model !== settings.model) chat.setModel(next.model)
    if (next.mode !== settings.mode) chat.setMode(next.mode)
    if (next.effort && next.effort !== settings.effort) chat.setEffort(next.effort)
    rememberSettings(next, agent)
  }
  // 页头写实际回复的模型（被降级时和选的不一样），还没回过就写 CLI 解析成的全名
  const shownModel = info && (info.actualModel || info.resolvedModel)

  if (conn === 'gone') {
    return (
      <>
        <PageHeader title="对话已不在" />
        <NotFound
          title="这个对话已经结束了"
          actions={
            <Button asChild>
              <a href={info ? href.project(info.project) : href.home()}>{info ? '去项目里找' : '回首页'}</a>
            </Button>
          }
        >
          可能被结束了，或者 rcweb 重启过。会话记录还在，可以到项目里找到它继续。
        </NotFound>
      </>
    )
  }

  // 读屏软件：有请求等你、断线、这一轮做完时念一句
  const announce =
    pending.length > 0
      ? pending[0].tool === 'AskUserQuestion'
        ? `${who} 有问题想问你`
        : `${who} 等你批准：${pending[0].tool}`
      : conn === 'retrying'
        ? '连接断了，正在重连'
        : result?.ok
          ? `${who} 这一轮做完了`
          : ''

  return (
    <>
      <PageHeader
        title={info?.title || '新对话'}
        subtitle={info && [agent !== 'claude' && who, info.project, shownModel && modelName(shownModel, chat.catalog)].filter(Boolean).join(' · ')}
      >
        {info && (
          <>
            <Badge variant="outline" className="hidden gap-1 sm:inline-flex">
              <StatusDot status={info.status} />
              {statusLabel[info.status]}
            </Badge>
            {/* 手机上放不下徽标，至少留个点 */}
            <span className="sm:hidden" role="img" aria-label={statusLabel[info.status]}>
              <StatusDot status={info.status} />
            </span>
          </>
        )}
        {running && (
          <Button variant="outline" size="sm" onClick={chat.interrupt}>
            <SquareIcon /> 中断
          </Button>
        )}
        {exited && (
          <Button size="sm" onClick={restart}>
            <RotateCcwIcon /> 重新启动
          </Button>
        )}
        <ViewToggles />
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon-sm" aria-label="对话操作">
              <EllipsisVerticalIcon />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            {info?.sessionId && (
              <DropdownMenuItem asChild>
                <a href={href.session(info.project, info.sessionId, agent)}>查看会话记录</a>
              </DropdownMenuItem>
            )}
            <DropdownMenuItem onClick={toTerminal}>
              <SquareTerminalIcon /> 到终端里继续
            </DropdownMenuItem>
            <DropdownMenuItem variant="destructive" onClick={() => close().then((ok) => ok && navigate(href.home()))}>
              <XIcon /> 结束对话
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </PageHeader>

      <div className="flex min-h-0 flex-1">
        {/* 手机上终端面板占满屏，对话先藏起来 */}
        <div className={cn('flex min-w-0 flex-1 flex-col', panelOpen && 'max-md:hidden')}>
          <div className="relative min-h-0 flex-1">
            <div ref={scrollRef} onScroll={onScroll} className="h-full overflow-y-auto">
              <div className="mx-auto flex max-w-3xl flex-col gap-3 px-4 py-5">
                {conn === 'connecting' && !info && <p className="text-center text-sm text-muted-foreground">连接中…</p>}
                <LoadOlder more={chat.more} loading={older.loading} onLoad={older.trigger} />
                {info && entries.length === 0 && (
                  <div className="py-16 text-center text-sm text-muted-foreground">
                    <p>在 {info.cwd} 里</p>
                    <p className="mt-1">发条消息开始吧</p>
                  </div>
                )}
                <Transcript
                  entries={entries}
                  offset={chat.start}
                  tools={chat.tools}
                  cwd={info?.cwd}
                  live={running}
                  animateFrom={chat.baseline}
                  thinkingOpen={view.thinking}
                  toolMode={view.tools}
                />
                {live && (
                  <div className="animate-in text-sm leading-relaxed whitespace-pre-wrap duration-300 fade-in">
                    {live}
                    <span className="ml-0.5 inline-block h-4 w-1.5 animate-pulse rounded-[1px] bg-primary align-text-bottom" />
                  </div>
                )}
                {running && pending.length === 0 && <Activity activity={info?.activity ?? ''} since={turnStart(entries)} />}
                {!running && !!info?.background?.length && <Background tasks={info.background} who={who} />}
                {result?.ok && (
                  <p className="text-xs text-muted-foreground">
                    完成 · {(result.durationMs / 1000).toFixed(1)}s{result.cost > 0 && ` · $${result.cost.toFixed(4)}`}
                  </p>
                )}
                {exited && stderr.length > 0 && (
                  <Alert variant="destructive">
                    <TriangleAlertIcon />
                    <AlertTitle>
                      {agent} 退出了（code {info?.exitCode}）
                    </AlertTitle>
                    <AlertDescription>
                      <pre className="mt-1 max-h-48 overflow-auto font-mono text-xs whitespace-pre-wrap">{stderr.slice(-20).join('\n')}</pre>
                    </AlertDescription>
                  </Alert>
                )}
              </div>
            </div>
            {!atBottom && (
              <Button size="icon-sm" variant="secondary" className="absolute right-4 bottom-3 rounded-full shadow" onClick={toBottom}>
                <ArrowDownIcon />
              </Button>
            )}
          </div>

          {conn === 'retrying' && (
            <div className="flex items-center justify-center gap-2 border-t bg-amber-500/10 py-1.5 text-xs text-amber-700 dark:text-amber-300">
              <Loader2Icon className="size-3.5 animate-spin" /> 连接断了，正在重连（{who} 在服务器上照常跑）
            </div>
          )}

          {pending.length > 0 && (
            <div className="max-h-[60dvh] shrink-0 space-y-2 overflow-y-auto border-t bg-muted/30 px-4 py-3">
              <div className="mx-auto max-w-3xl space-y-2">
                {pending.map((p, i) => (
                  <PermissionCard key={p.id} req={p} onAnswer={chat.answer} agent={agent} autoFocus={i === 0} />
                ))}
              </div>
            </div>
          )}

          <Composer
            agent={agent}
            catalog={chat.catalog}
            settings={settings}
            onSettings={onSettings}
            live={info && { resolved: info.resolvedModel, actual: info.actualModel }}
            webCommands={webCommands}
            // 有请求等你处理时先别让发消息：桌面上输入框有焦点，一按回车就误发了（TUI 里这时也打不了字）
            disabled={conn !== 'open' || exited || pending.length > 0}
            placeholder={pending.length > 0 ? '先处理上面的请求' : undefined}
            running={running}
            onSend={chat.sendText}
            onInterrupt={chat.interrupt}
            suggestion={chat.suggestion}
            history={history}
            autoFocus
          />
        </div>
        {info && <SidePanel cwd={info.cwd} project={info.project} />}
      </div>
      <div className="sr-only" role="status" aria-live="polite">
        {announce}
      </div>
    </>
  )
}

// Claude Code 那一行：闪动的星形 + 扫光的「思考中… / 正在回复… / 正在运行 Bash…」
function Activity({ activity, since }: { activity: string; since?: number }) {
  const compacting = activity === 'compacting'
  const label = activity.startsWith('tool:')
    ? `正在运行 ${activity.slice(5)}`
    : activity === 'text'
      ? '正在回复'
      : compacting
        ? '正在压缩上下文'
        : '思考中'
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [])
  const secs = since ? Math.max(0, Math.floor((now - since) / 1000)) : undefined
  const beat = useBeat()
  return (
    <div className="flex animate-in items-center gap-2 py-1 text-sm duration-300 fade-in">
      <ClaudeSpinner className="text-base" />
      {secs !== undefined && (
        <span className="text-muted-foreground tabular-nums">{secs < 60 ? `${secs}s` : `${Math.floor(secs / 60)}m ${secs % 60}s`} ·</span>
      )}
      <span className="shimmer font-medium" style={beat}>{label}…</span>
      {compacting && <span className="progress-indeterminate ml-1 w-28" role="progressbar" aria-label="正在压缩上下文" />}
    </div>
  )
}

// 这一轮回完了，但后台命令 / 子代理还在跑：跑完 CLI 会自己接着处理，到时候又变成运行中
function Background({ tasks, who }: { tasks: BgTask[]; who: string }) {
  return (
    <div className="flex animate-in items-start gap-2 py-1 text-sm text-muted-foreground duration-300 fade-in">
      <ClaudeSpinner slow className="mt-0.5 text-base text-muted-foreground" />
      <div className="min-w-0">
        <p>
          后台还有 {tasks.length} 个任务在跑，跑完 {who} 会接着处理
        </p>
        <ul className="mt-0.5 space-y-0.5 text-xs">
          {tasks.map((t) => (
            <li key={t.id} className="truncate">
              <span className="font-mono opacity-70">{bgKind(t.kind)}</span> {t.description}
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}

const bgKind = (k: string) => ({ local_bash: '命令', local_agent: '子代理', remote_agent: '远程代理' })[k] ?? k

// 这一轮从哪条用户消息开始：往回找最近一条带文字的用户消息（工具结果也是 user，不算）
function turnStart(entries: Entry[]) {
  for (let i = entries.length - 1; i >= 0; i--) {
    const e = entries[i]
    if (e.role === 'user' && e.ts && e.blocks.some((b) => b.t === 'text')) return Date.parse(e.ts) || undefined
  }
}
