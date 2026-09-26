import type { ReactNode } from 'react'

// 会话 / 项目 / 对话找不到时占满内容区：说清楚怎么回事，给一条出路（状态栏还在底下，不会被顶到中间）
export function NotFound({ title, children, actions }: { title: string; children?: ReactNode; actions: ReactNode }) {
  return (
    <div className="flex min-h-0 flex-1 items-center justify-center overflow-y-auto p-6">
      <div className="max-w-sm text-center">
        <h2 className="text-base font-medium">{title}</h2>
        {children && <p className="mt-1.5 text-sm text-muted-foreground">{children}</p>}
        <div className="mt-5 flex flex-wrap justify-center gap-2">{actions}</div>
      </div>
    </div>
  )
}
