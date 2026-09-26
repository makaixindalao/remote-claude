import { useEffect, useState, type ReactNode } from 'react'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { api } from '@/lib/api'
import { parseHash, useHash } from '@/lib/route'
import { useChatLive, type ContextUsage } from '@/lib/status'
import { cn } from '@/lib/utils'

// 页面底部常驻的一行，照本地 statusline（ccstatusline / zhima-statusline）的样子：
//   上下文 ▰▰▱ 30% · 60k/200k │ 5h ▰▱ 15% ↻3h53m │ 7d ▰▱ 40% ↻4d8h
// 上下文只有打开着网页对话时才有（claude 回完一轮后 get_context_usage 推过来）；
// 5h / 7d 读 GET /api/limits，和 statusline 一样不为这个发任何网络请求，只读 claude 自己留下的数据。

type Window = { pct: number; resetsAt: number }
type Limits = { fiveHour?: Window; sevenDay?: Window; updatedAt: number }

function useLimits() {
  const [limits, setLimits] = useState<Limits>()
  useEffect(() => {
    let alive = true
    const load = () =>
      document.visibilityState === 'visible' &&
      api<Limits>('/api/limits')
        .then((l) => alive && setLimits(l))
        .catch(() => {})
    load()
    const t = setInterval(load, 30_000)
    document.addEventListener('visibilitychange', load)
    return () => {
      alive = false
      clearInterval(t)
      document.removeEventListener('visibilitychange', load)
    }
  }, [])
  return limits
}

// 每分钟走一次，倒计时跟着变
function useMinuteClock() {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 30_000)
    return () => clearInterval(t)
  }, [])
  return now
}

const countdown = (ms: number) => {
  const m = Math.max(0, Math.round(ms / 60_000))
  if (m < 60) return `${m}m`
  if (m < 24 * 60) return `${Math.floor(m / 60)}h${m % 60}m`
  const h = Math.floor(m / 60)
  return `${Math.floor(h / 24)}d${h % 24}h`
}

const tokens = (n: number) => (n >= 1_000_000 ? `${+(n / 1_000_000).toFixed(1)}M` : n >= 1000 ? `${Math.round(n / 1000)}k` : `${n}`)

// 颜色和 statusline 一样三档：够用 / 注意 / 快满了
type Tone = { bar: string; text: string }
const tone = (pct: number, warn = 50, danger = 80): Tone =>
  pct >= danger
    ? { bar: 'bg-destructive', text: 'text-destructive' }
    : pct >= warn
      ? { bar: 'bg-amber-500', text: 'text-amber-600 dark:text-amber-400' }
      : { bar: 'bg-primary', text: 'text-foreground' }

function Meter({ pct, t }: { pct: number; t: Tone }) {
  return (
    <span className="relative hidden h-1.5 w-10 overflow-hidden rounded-full bg-muted sm:inline-block">
      {/* 用了一点也要看得见，所以最少画 2%；一点没用就空着，不然像个没画好的小圆点 */}
      <span className={cn('absolute inset-y-0 left-0 rounded-full', t.bar)} style={{ width: `${pct > 0 ? Math.min(100, Math.max(2, pct)) : 0}%` }} />
    </span>
  )
}

function Segment({ label, tip, children }: { label: string; tip: ReactNode; children: ReactNode }) {
  // tabIndex：能用 Tab 聚焦，键盘用户也看得到说明
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span tabIndex={0} className="flex shrink-0 cursor-default items-center gap-1.5 rounded-sm whitespace-nowrap outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
          <span className="text-muted-foreground">{label}</span>
          {children}
        </span>
      </TooltipTrigger>
      <TooltipContent side="top">{tip}</TooltipContent>
    </Tooltip>
  )
}

const Sep = () => <span className="text-border select-none">│</span>

function ContextSegment({ ctx, compacting }: { ctx?: ContextUsage; compacting: boolean }) {
  if (compacting) {
    return (
      <span className="flex shrink-0 items-center gap-2 whitespace-nowrap">
        <span className="text-muted-foreground">上下文</span>
        <span className="progress-indeterminate w-16 sm:w-24" role="progressbar" aria-label="正在压缩上下文" />
        <span className="shimmer">正在压缩…</span>
      </span>
    )
  }
  const total = ctx?.totalTokens
  const max = ctx?.maxTokens
  const pct = total && max ? (total / max) * 100 : ctx?.percentage
  if (pct === undefined) {
    return (
      <Segment label="上下文" tip="这个对话回完第一轮后显示">
        <span className="text-muted-foreground">—</span>
      </Segment>
    )
  }
  // 自动压缩的阈值当「快满了」那一档
  const threshold = ctx?.autoCompactThreshold && max ? (ctx.autoCompactThreshold / max) * 100 : 80
  const t = tone(pct, threshold * 0.6, threshold)
  return (
    <Segment
      label="上下文"
      tip={
        <div className="space-y-0.5">
          <div>
            {total !== undefined && max ? `${total.toLocaleString()} / ${max.toLocaleString()} tokens` : `${pct.toFixed(0)}%`}
            {ctx?.model && ` · ${ctx.model}`}
          </div>
          {ctx?.isAutoCompactEnabled && ctx.autoCompactThreshold ? <div>到 {tokens(ctx.autoCompactThreshold)} 时自动压缩</div> : null}
        </div>
      }
    >
      <Meter pct={pct} t={t} />
      <span className={cn('tabular-nums', t.text)}>{Math.round(pct)}%</span>
      {total !== undefined && max ? (
        <span className="hidden text-muted-foreground tabular-nums md:inline">
          {tokens(total)}/{tokens(max)}
        </span>
      ) : null}
    </Segment>
  )
}

function LimitSegment({ label, w, now, full }: { label: string; w: Window; now: number; full: string }) {
  const t = tone(w.pct)
  const left = w.resetsAt ? w.resetsAt - now : 0
  return (
    <Segment
      label={label}
      tip={
        <div>
          {full}已用 {w.pct.toFixed(0)}%
          {w.resetsAt
            ? ` · ${new Date(w.resetsAt).toLocaleString([], { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' })} 重置`
            : ''}
        </div>
      }
    >
      <Meter pct={w.pct} t={t} />
      <span className={cn('tabular-nums', t.text)}>{Math.round(w.pct)}%</span>
      {left > 0 && <span className="text-muted-foreground tabular-nums">↻{countdown(left)}</span>}
    </Segment>
  )
}

export function StatusBar() {
  const route = parseHash(useHash())
  const chatId = route.name === 'chat' ? route.id : undefined
  const live = useChatLive(chatId)
  const limits = useLimits()
  const now = useMinuteClock()
  const five = limits?.fiveHour
  const seven = limits?.sevenDay
  if (!chatId && !five && !seven) return null

  return (
    <div className="flex h-7 shrink-0 items-center gap-2.5 overflow-x-auto border-t bg-background px-3 font-mono text-2xs [scrollbar-width:none] sm:px-4">
      {chatId && <ContextSegment ctx={live?.context} compacting={live?.activity === 'compacting'} />}
      {chatId && (five || seven) && <Sep />}
      {five && <LimitSegment label="5h" w={five} now={now} full="5 小时窗口" />}
      {five && seven && <Sep />}
      {seven && <LimitSegment label="7d" w={seven} now={now} full="7 天窗口" />}
    </div>
  )
}
