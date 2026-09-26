import { useState, type CSSProperties } from 'react'
import type { ChatStatus } from '@/lib/api'
import { cn } from '@/lib/utils'

// 运行中的标志：Claude Code 终端里那颗 ✳。整颗慢慢转，光芒一起收回、再伸出 ——
// 终端里 ·✢✳✶✻✽ 来回变的那个节奏（动画在 index.css 的 .rc-spark）。
// 画成 SVG 而不是用 ✻✳ 这类字符 —— 手机上字体不一样，✳ 还会被画成绿底的 emoji。
// 线宽和 lucide 图标一样是 2，伸缩只改光芒长度不改粗细，14px 也不发虚；
// 光芒不轮流伸缩，转起来就不会有车轮倒转那种错觉。
// 系统里设了「减少动态效果」就不转不伸缩，只明暗呼吸

// 8 条光芒，从正上方顺时针，长短相间（viewBox 24 里离中心的距离，圆头再多出 1）
const RAYS = [10, 6.5, 10, 6.5, 10, 6.5, 10, 6.5]

// 同屏的「运行中」动画共用一个节拍：用负的 animation-delay 把相位对齐到页面时间线，
// 页头、侧栏、工具行、底部那行一起呼吸，中途冒出来的也跟着拍子走，不从头开始
export function useBeat() {
  const [t] = useState(() => Math.round(performance.now()))
  return { '--rc-beat': `-${t}ms` } as CSSProperties
}

export function ClaudeSpinner({ className, slow }: { className?: string; slow?: boolean }) {
  const beat = useBeat()
  return (
    <svg
      viewBox="0 0 24 24"
      aria-hidden
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      style={beat}
      className={cn('rc-spark inline-block size-[1em] shrink-0 text-primary', slow && 'rc-spark-slow', className)}
    >
      {RAYS.map((len, i) => (
        // pathLength=1：收短时按各自长度的比例收，长的短的一起动
        <path key={i} d={`M12 12V${12 - len}`} pathLength={1} transform={`rotate(${i * 45} 12 12)`} />
      ))}
    </svg>
  )
}

export const statusLabel: Record<ChatStatus, string> = {
  running: '运行中',
  waiting: '等你回答',
  background: '后台运行中',
  done: '已完成',
  idle: '空闲',
  error: '出错',
}

export function StatusDot({ status, className }: { status: ChatStatus; className?: string }) {
  if (status === 'running') return <ClaudeSpinner className={className} />
  // 后台任务在跑：同一颗星，灰色、慢一半，和正在回复的区分开
  if (status === 'background') return <ClaudeSpinner slow className={cn('text-muted-foreground', className)} />
  return (
    <span className={cn('inline-flex w-3.5 shrink-0 items-center justify-center', className)} title={statusLabel[status]}>
      <span
        className={cn(
          'size-2 rounded-full',
          // 浅色底上 500 档只有 2:1 左右，换深一档才看得清；深色底上用浅一档
          status === 'waiting' && 'animate-pulse bg-amber-600 ring-3 ring-amber-600/25 motion-reduce:animate-none dark:bg-amber-400 dark:ring-amber-400/25',
          status === 'done' && 'bg-emerald-600 dark:bg-emerald-400',
          status === 'error' && 'bg-destructive',
          status === 'idle' && 'border border-muted-foreground/50',
        )}
      />
    </span>
  )
}
