import type { ReactNode } from 'react'

// 设置页的一块：小标题 + 一句说明，标题右边可以放按钮
export function SettingsSection({ title, desc, action, children }: { title: ReactNode; desc?: ReactNode; action?: ReactNode; children: ReactNode }) {
  return (
    <section className="space-y-3">
      <div className="flex items-end gap-3">
        <div className="min-w-0 flex-1">
          <h2 className="text-sm font-medium">{title}</h2>
          {desc && <p className="text-xs text-muted-foreground">{desc}</p>}
        </div>
        {action && <div className="flex shrink-0 items-center gap-1.5">{action}</div>}
      </div>
      {children}
    </section>
  )
}
