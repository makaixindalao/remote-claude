import { ChevronRightIcon, EllipsisIcon, ListFilterIcon, LogOutIcon, MoonIcon, PlusIcon, SettingsIcon, SquareTerminalIcon, SunIcon } from 'lucide-react'
import { useTheme } from 'next-themes'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupAction,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuAction,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSkeleton,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  useSidebar,
} from '@/components/ui/sidebar'
import { AgentBadge } from '@/components/agent-badge'
import { Logo } from '@/components/logo'
import { ClaudeSpinner, StatusDot, statusLabel } from '@/components/state-dot'
import { useData, type SessionFilter } from '@/hooks/use-data'
import { agentOf } from '@/lib/agents'
import type { ChatInfo, Project, SessionInfo, TmuxSession } from '@/lib/api'
import { timeAgo } from '@/lib/format'
import { href, useHash } from '@/lib/route'
import { cn } from '@/lib/utils'

const filterLabel: Record<SessionFilter, string> = { active: '活跃', archived: '已归档', all: '全部' }

// 项目旁边的数字跟着筛选走
function sessionCount(p: Project, f: SessionFilter) {
  return f === 'active' ? p.sessions : f === 'archived' ? p.archived : p.sessions + p.archived
}

export function AppSidebar({ onLogout }: { onLogout: () => void }) {
  const { projects, chats, tmux, sessions, expanded, sessionFilter, setSessionFilter, toggleProject } = useData()
  const hash = useHash()
  const { setOpenMobile } = useSidebar()
  const { resolvedTheme, setTheme } = useTheme()
  const close = () => setOpenMobile(false)
  // 只比路径部分：终端链接带的 ?mode=attach 之类不影响高亮
  const path = (h: string) => decodeURIComponent(h.split('?')[0])
  const active = (h: string) => path(hash) === path(h)
  const chatBySession = new Map(chats.filter((c) => c.sessionId && c.state !== 'exited').map((c) => [c.sessionId, c]))

  return (
    <Sidebar>
      <SidebarHeader className="gap-3">
        <a href={href.home()} onClick={close} className="flex items-center gap-2 px-2 pt-1">
          <Logo />
          <span className="font-semibold">remote-claude</span>
        </a>
        <Button className="w-full" asChild>
          <a href={href.newChat()} onClick={close}>
            <PlusIcon /> 新对话
          </a>
        </Button>
      </SidebarHeader>

      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupLabel>网页对话</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {chats.length === 0 && <p className="px-2 py-1 text-xs text-muted-foreground">还没有</p>}
              {chats.map((c) => (
                <SidebarMenuItem key={c.id}>
                  <SidebarMenuButton asChild isActive={active(href.chat(c.id))} className="h-auto py-1.5">
                    <a href={href.chat(c.id)} onClick={close}>
                      <StatusDot status={c.status} />
                      <div className="min-w-0">
                        <div className="flex items-center gap-1.5">
                          <span className={cn('truncate', c.status === 'done' && 'font-semibold')}>{c.title || '新对话'}</span>
                          <AgentBadge agent={c.agent} />
                        </div>
                        <div className="truncate text-xs text-muted-foreground">
                          {statusLabel[c.status]} · {c.project}
                        </div>
                      </div>
                    </a>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        <SidebarGroup>
          <SidebarGroupLabel>项目{sessionFilter !== 'active' && ` · ${filterLabel[sessionFilter]}`}</SidebarGroupLabel>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <SidebarGroupAction title="筛选会话" className={cn(sessionFilter !== 'active' && 'text-primary')}>
                <ListFilterIcon />
              </SidebarGroupAction>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuRadioGroup value={sessionFilter} onValueChange={(v) => setSessionFilter(v as SessionFilter)}>
                {(['active', 'archived', 'all'] as const).map((f) => (
                  <DropdownMenuRadioItem key={f} value={f}>
                    {filterLabel[f]}
                  </DropdownMenuRadioItem>
                ))}
              </DropdownMenuRadioGroup>
            </DropdownMenuContent>
          </DropdownMenu>
          <SidebarGroupContent>
            <SidebarMenu>
              {projects.map((p) => (
                <Collapsible key={p.name} open={expanded.has(p.name)} onOpenChange={() => toggleProject(p.name)} className="group/collapsible" asChild>
                  <SidebarMenuItem>
                    <CollapsibleTrigger asChild>
                      <SidebarMenuButton isActive={active(href.project(p.name))}>
                        <ChevronRightIcon className="transition-transform group-data-[state=open]/collapsible:rotate-90" />
                        <span className="truncate">{p.name}</span>
                        <span className="ml-auto text-xs text-muted-foreground tabular-nums group-hover/menu-item:opacity-0">{sessionCount(p, sessionFilter)}</span>
                      </SidebarMenuButton>
                    </CollapsibleTrigger>
                    {p.exists && (
                      <SidebarMenuAction showOnHover asChild title="在这个项目里开新对话">
                        <a href={href.newChat(p.name)} onClick={close}>
                          <PlusIcon />
                        </a>
                      </SidebarMenuAction>
                    )}
                    <CollapsibleContent>
                      <SidebarMenuSub className="mr-0 pr-0">
                        {!sessions[p.name] && [0, 1].map((i) => <SidebarMenuSkeleton key={i} className="h-7" />)}
                        {sessions[p.name]?.length === 0 && (
                          <p className="px-2 py-1 text-xs text-muted-foreground">{sessionFilter === 'archived' ? '没有归档的会话' : '没有会话'}</p>
                        )}
                        {sessions[p.name]?.map((s) => {
                          const chat = chatBySession.get(s.id)
                          return (
                            <SessionItem
                              key={s.id}
                              project={p.name}
                              s={s}
                              chat={chat}
                              active={active(href.session(p.name, s.id, agentOf(s.agent))) || (!!chat && active(href.chat(chat.id)))}
                              onNavigate={close}
                            />
                          )
                        })}
                        <SidebarMenuSubItem>
                          <SidebarMenuSubButton asChild size="sm" className="text-muted-foreground">
                            <a href={href.project(p.name)} onClick={close}>
                              全部会话{sessionFilter === 'active' && p.archived > 0 && ` · ${p.archived} 个已归档`}
                            </a>
                          </SidebarMenuSubButton>
                        </SidebarMenuSubItem>
                      </SidebarMenuSub>
                    </CollapsibleContent>
                  </SidebarMenuItem>
                </Collapsible>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        <SidebarGroup>
          <SidebarGroupLabel>终端 · tmux</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {tmux.length === 0 && <p className="px-2 py-1 text-xs text-muted-foreground">没有在跑的 tmux 会话</p>}
              {tmux.map((t) => (
                <SidebarMenuItem key={t.name}>
                  <SidebarMenuButton asChild isActive={active(href.term(t.name))}>
                    <a href={href.term(t.name, { mode: 'attach' })} onClick={close}>
                      {agentPane(t) ? <ClaudeSpinner /> : <SquareTerminalIcon />}
                      <span>{t.name}</span>
                    </a>
                  </SidebarMenuButton>
                  <SidebarMenuBadge className="font-mono text-3xs">{t.command}</SidebarMenuBadge>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>

      <SidebarFooter>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton asChild isActive={path(hash).startsWith(href.settings())}>
              <a href={href.settings()} onClick={close}>
                <SettingsIcon />
                <span>设置</span>
              </a>
            </SidebarMenuButton>
          </SidebarMenuItem>
          <SidebarMenuItem>
            <SidebarMenuButton onClick={() => setTheme(resolvedTheme === 'dark' ? 'light' : 'dark')}>
              {resolvedTheme === 'dark' ? <SunIcon /> : <MoonIcon />}
              {/* 写成动作，免得「深色」被看成当前状态 */}
              <span>{resolvedTheme === 'dark' ? '切换到浅色' : '切换到深色'}</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
          <SidebarMenuItem>
            <SidebarMenuButton onClick={onLogout}>
              <LogOutIcon />
              <span>退出登录</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarFooter>
    </Sidebar>
  )
}

// tmux 窗格里跑的是不是 CLI：grok 的进程名带版本号（grok-1.0.41-mac），npm 装的 codex 显示成 node，
// 设置里换成 reclaude 后是 reclaude
function agentPane(t: TmuxSession) {
  return /^(claude|reclaude|codex|grok)/.test(t.command) || (t.command === 'node' && /^(cc|cx|gk)-/.test(t.name))
}

// 侧栏里的一条会话：有网页对话在跑就显示它的状态、点进去是对话，否则点进去看记录
function SessionItem({
  project,
  s,
  chat,
  active,
  onNavigate,
}: {
  project: string
  s: SessionInfo
  chat?: ChatInfo
  active: boolean
  onNavigate: () => void
}) {
  const { archiveSession, deleteSession } = useData()
  return (
    <SidebarMenuSubItem className="group/sub relative">
      <SidebarMenuSubButton asChild isActive={active} className="pr-7">
        <a href={chat ? href.chat(chat.id) : href.session(project, s.id, agentOf(s.agent))} onClick={onNavigate} title={`${s.title}\n${timeAgo(s.mtime)}`}>
          {chat ? <StatusDot status={chat.status} /> : <span className="w-3.5 shrink-0" />}
          <span className={cn('truncate', chat?.status === 'done' && 'font-semibold', s.archived && 'text-muted-foreground')}>{s.title || '（无标题）'}</span>
          <AgentBadge agent={s.agent} />
        </a>
      </SidebarMenuSubButton>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            aria-label="会话操作"
            // 图标只有 14px，触屏上用 ::after 把能点的范围撑到 34px 左右，外观不变
            className="absolute top-1/2 right-1 -translate-y-1/2 rounded-md p-0.5 text-muted-foreground opacity-0 group-hover/sub:opacity-100 hover:bg-sidebar-accent hover:text-foreground data-[state=open]:opacity-100 pointer-coarse:opacity-100 pointer-coarse:after:absolute pointer-coarse:after:-inset-2.5"
          >
            <EllipsisIcon className="size-3.5" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent side="right" align="start">
          <DropdownMenuItem onClick={() => archiveSession(project, s, !s.archived)}>{s.archived ? '取消归档' : '归档'}</DropdownMenuItem>
          <DropdownMenuItem variant="destructive" onClick={() => deleteSession(project, s)}>
            删除
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </SidebarMenuSubItem>
  )
}
