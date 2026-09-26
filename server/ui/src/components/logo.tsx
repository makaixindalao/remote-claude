import { cn } from '@/lib/utils'

// 产品标志：陶土色圆角方块里一颗 ✻。和 favicon（components/attention.tsx 的 faviconSvg）是同一个图形；
// 画成 SVG 而不是用字符，各平台字体里的 ✻ 长得不一样
export function Logo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 32 32" aria-hidden className={cn('size-7 shrink-0', className)}>
      <rect width="32" height="32" rx="7" className="fill-primary" />
      <g className="stroke-primary-foreground" strokeWidth="3" strokeLinecap="round">
        {[0, 45, 90, 135].map((a) => (
          <line key={a} x1="16" y1="8" x2="16" y2="24" transform={`rotate(${a} 16 16)`} />
        ))}
      </g>
    </svg>
  )
}
