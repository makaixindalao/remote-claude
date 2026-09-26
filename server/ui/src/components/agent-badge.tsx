import { agentLabel, type Agent } from '@/lib/agents'
import { cn } from '@/lib/utils'

// 会话 / 对话是哪个 CLI 的。claude 是这里的主角，不标；codex / grok 标一个小徽记
const tone: Record<Exclude<Agent, 'claude'>, string> = {
  codex: 'bg-sky-500/10 text-sky-700 ring-sky-500/30 dark:text-sky-300',
  grok: 'bg-violet-500/10 text-violet-700 ring-violet-500/30 dark:text-violet-300',
}

export function AgentBadge({ agent, className }: { agent?: Agent; className?: string }) {
  if (!agent || agent === 'claude') return null
  return (
    <span className={cn('shrink-0 rounded px-1 py-px text-3xs leading-4 font-medium ring-1 ring-inset', tone[agent], className)}>{agentLabel[agent]}</span>
  )
}
