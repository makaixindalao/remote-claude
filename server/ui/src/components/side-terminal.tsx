import { lazy, Suspense, useState } from 'react'
import { RotateCcwIcon, SquareTerminalIcon, XIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { shellName } from '@/lib/route'
import { termStatusLabel, type TermStatus } from '@/lib/term-status'
import { setView } from '@/lib/view'
import { cn } from '@/lib/utils'

// xterm 很大，打开面板时才下载（和整页终端共用同一个 chunk）
const TerminalPane = lazy(() => import('@/components/terminal-pane').then((m) => ({ default: m.TerminalPane })))

// 对话页 / 会话页右侧的终端面板：在对话的工作目录里开一个 shell（tmux 会话 sh-<目录名>，
// 关掉面板它也还在，下次打开接着用），Tab 补全、历史都是 shell 自己的。配色跟页面主题走。
export function SideTerminal({ cwd, project }: { cwd: string; project: string }) {
  const [status, setStatus] = useState<TermStatus>('connecting')
  const [sig, setSig] = useState(0)
  const name = shellName(cwd)
  return (
    <div className="flex min-h-0 w-full flex-col border-l md:w-1/2">
      <div className="flex h-9 shrink-0 items-center gap-2 border-b px-3 text-xs text-muted-foreground">
        <SquareTerminalIcon className="size-3.5 shrink-0" />
        <span className="min-w-0 truncate font-mono" title={cwd}>
          {cwd}
        </span>
        <span className="ml-auto flex shrink-0 items-center gap-1.5">
          <span
            className={cn(
              'size-1.5 rounded-full',
              status === 'open'
                ? 'bg-emerald-500'
                : status === 'connecting' || status === 'retrying'
                  ? 'animate-pulse bg-amber-500'
                  : 'bg-muted-foreground/40',
            )}
          />
          {termStatusLabel[status]}
        </span>
        <Button size="icon-xs" variant="ghost" onClick={() => setSig((n) => n + 1)} aria-label="重新连接">
          <RotateCcwIcon />
        </Button>
        <Button size="icon-xs" variant="ghost" onClick={() => setView({ terminal: false })} aria-label="关闭终端">
          <XIcon />
        </Button>
      </div>
      <Suspense fallback={<div className="min-h-0 flex-1" />}>
        <TerminalPane
          key={name}
          session={name}
          mode="shell"
          project={project}
          dir={cwd}
          statusOff
          follow="app"
          reconnectSignal={sig}
          onStatus={setStatus}
        />
      </Suspense>
    </div>
  )
}
