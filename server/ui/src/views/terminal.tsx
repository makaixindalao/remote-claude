import { useState } from 'react'
import { RotateCcwIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/page-header'
import { TerminalPane, termStatusLabel, type TermStatus } from '@/components/terminal-pane'

// 整页终端：接回 scc 开的 tmux 会话（里面是 Claude TUI，所有交互命令都能用）
export function TermView({ session, project, mode, resume }: { session: string; project?: string; mode?: string; resume?: string }) {
  const [status, setStatus] = useState<TermStatus>('connecting')
  const [sig, setSig] = useState(0)
  return (
    <>
      <PageHeader title={session} subtitle={project}>
        <Badge variant="outline" className="gap-1.5">
          <span className={`size-2 rounded-full ${status === 'open' ? 'bg-emerald-500' : status === 'connecting' || status === 'retrying' ? 'animate-pulse bg-amber-500' : 'bg-muted-foreground/40'}`} />
          {termStatusLabel[status]}
        </Badge>
        <Button size="sm" variant="outline" onClick={() => setSig((n) => n + 1)}>
          <RotateCcwIcon /> <span className="hidden sm:inline">重新连接</span>
        </Button>
      </PageHeader>
      <TerminalPane session={session} project={project} mode={mode} resume={resume} follow="dark" reconnectSignal={sig} onStatus={setStatus} />
    </>
  )
}
