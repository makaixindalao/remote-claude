import type { ReactNode } from 'react'
import { Separator } from '@/components/ui/separator'
import { SidebarTrigger } from '@/components/ui/sidebar'

export function PageHeader({ title, subtitle, children }: { title: ReactNode; subtitle?: ReactNode; children?: ReactNode }) {
  return (
    <header className="flex h-13 shrink-0 items-center gap-2 border-b px-3">
      <SidebarTrigger className="-ml-1" />
      <Separator orientation="vertical" className="mr-1 data-[orientation=vertical]:h-4" />
      <div className="min-w-0 flex-1">
        {/* 每页一个 h1，读屏软件按标题跳转时有落脚点 */}
        <h1 className="truncate text-sm font-medium">{title}</h1>
        {subtitle && <div className="truncate text-xs text-muted-foreground">{subtitle}</div>}
      </div>
      {children && <div className="flex shrink-0 items-center gap-1.5">{children}</div>}
    </header>
  )
}
