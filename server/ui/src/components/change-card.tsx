import { ChevronRightIcon, CodeXmlIcon } from 'lucide-react'
import { DiffStat } from '@/components/diff-view'
import { openDiff, type TurnChange } from '@/lib/file-panel'
import { shortPath } from '@/lib/format'
import type { ToolDetails } from '@/lib/tool-details'

// 每轮对话末尾的改动卡片：这一轮改了哪些文件、各加删几行。点一行在右侧打开 diff 并滚到那个文件

const SHOW = 6

export function ChangeCard({ files, cwd, tools, partial }: { files: TurnChange[]; cwd?: string; tools?: ToolDetails; partial?: boolean }) {
  const open = (focus?: string) => openDiff({ turn: files, focus, tools })
  const shown = files.length > SHOW ? files.slice(0, SHOW - 1) : files
  const total = files.reduce((s, f) => ({ add: s.add + f.add, del: s.del + f.del }), { add: 0, del: 0 })
  return (
    <div className="overflow-hidden rounded-xl border bg-card">
      {files.length > 1 && (
        <button
          type="button"
          className="flex w-full items-center gap-2 border-b px-3.5 py-2 text-left text-xs text-muted-foreground transition-colors hover:bg-muted/50"
          onClick={() => open()}
        >
          <span>改了 {files.length} 个文件</span>
          <DiffStat {...total} className="ml-auto" />
          <ChevronRightIcon className="size-3.5 shrink-0" />
        </button>
      )}
      {shown.map((f) => {
        const rel = shortPath(f.path, cwd)
        const slash = rel.lastIndexOf('/')
        return (
          <button
            key={f.path}
            type="button"
            className="flex w-full min-w-0 items-center gap-2.5 px-3.5 py-2.5 text-left transition-colors not-last:border-b hover:bg-muted/50"
            onClick={() => open(f.path)}
            title={f.path}
          >
            <CodeXmlIcon className="size-4 shrink-0 text-muted-foreground" />
            <span className="shrink-0 truncate text-sm">{rel.slice(slash + 1)}</span>
            {slash > 0 && <span className="min-w-0 truncate text-xs text-muted-foreground">{rel.slice(0, slash)}</span>}
            <DiffStat add={f.add} del={f.del} className="ml-auto pl-2 text-sm" />
            <ChevronRightIcon className="size-4 shrink-0 text-muted-foreground" />
          </button>
        )
      })}
      {files.length > shown.length && (
        <button
          type="button"
          className="w-full px-3.5 py-2 text-left text-xs text-muted-foreground transition-colors hover:bg-muted/50"
          onClick={() => open()}
        >
          还有 {files.length - shown.length} 个文件
        </button>
      )}
      {partial && <p className="border-t px-3.5 py-1.5 text-xs text-muted-foreground">这一轮开头的记录还没加载，往上翻加载后这里会补全</p>}
    </div>
  )
}
