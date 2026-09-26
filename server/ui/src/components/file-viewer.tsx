import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { CopyIcon, FileTextIcon, XIcon } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { api } from '@/lib/api'
import { copyText } from '@/lib/clipboard'
import { closePanel } from '@/lib/file-panel'
import { langFromPath, loadHljs } from '@/lib/highlight'
import { cn } from '@/lib/utils'

// 右侧的文件查看：面包屑路径、行号、语法高亮，带 :行号 的跳过去并高亮那一行。
// 内容来自服务端 /api/file（只读，限制在 RCWEB_ROOT 下）。

type FileData = { path: string; rel: string; size: number; content: string; truncated: boolean; binary: boolean }

const LINE_H = 20 // 和下面的 leading-5 对齐，跳行用

export function FileViewer({ path, line, cwd, project }: { path: string; line?: number; cwd?: string; project?: string }) {
  const [data, setData] = useState<FileData | null>(null)
  const [error, setError] = useState('')
  const [html, setHtml] = useState<string | null>(null)
  const scrollRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    setData(null)
    setError('')
    setHtml(null)
    const q = new URLSearchParams({ path })
    if (cwd) q.set('cwd', cwd)
    if (project) q.set('project', project)
    api<FileData>(`/api/file?${q}`)
      .then(setData)
      .catch((e) => setError(e.message))
  }, [path, cwd, project])

  // 高亮放到下一拍：大文件先把纯文本显示出来
  useEffect(() => {
    if (!data || data.binary || data.content.length > 300_000) return
    const lang = langFromPath(data.path)
    if (!lang) return
    let alive = true
    loadHljs().then((hljs) => {
      if (alive && hljs.getLanguage(lang)) setHtml(hljs.highlight(data.content, { language: lang, ignoreIllegals: true }).value)
    })
    return () => {
      alive = false
    }
  }, [data])

  const lines = useMemo(() => (data ? data.content.split('\n').length : 0), [data])

  useLayoutEffect(() => {
    if (data && line && scrollRef.current) scrollRef.current.scrollTop = Math.max(0, (line - 6) * LINE_H)
  }, [data, line])

  const crumbs = (data?.rel ?? path).split('/').filter(Boolean)
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-9 shrink-0 items-center gap-1.5 border-b px-3 text-xs text-muted-foreground">
        <FileTextIcon className="size-3.5 shrink-0" />
        <div className="flex min-w-0 flex-1 items-center gap-1 overflow-hidden" title={data?.path ?? path}>
          {crumbs.map((c, i) => (
            <span key={i} className={cn('shrink-0 truncate', i === crumbs.length - 1 && 'min-w-0 shrink font-medium text-foreground')}>
              {i > 0 && <span className="mr-1 opacity-50">/</span>}
              {c}
            </span>
          ))}
        </div>
        {line && <span className="shrink-0 font-mono">:{line}</span>}
        <Button
          size="icon-xs"
          variant="ghost"
          aria-label="复制路径"
          onClick={() => copyText(data?.path ?? path).then(() => toast.success('路径已复制'))}
        >
          <CopyIcon />
        </Button>
        <Button size="icon-xs" variant="ghost" aria-label="关闭文件" onClick={closePanel}>
          <XIcon />
        </Button>
      </div>

      {error && <p className="p-4 text-sm text-destructive">{error}</p>}
      {!data && !error && (
        <div className="space-y-2 p-4">
          {[0, 1, 2, 3, 4].map((i) => (
            <Skeleton key={i} className="h-4" style={{ width: `${40 + ((i * 17) % 50)}%` }} />
          ))}
        </div>
      )}
      {data?.binary && <p className="p-4 text-sm text-muted-foreground">二进制文件（{data.size} 字节），不显示内容。</p>}
      {data && !data.binary && (
        <div ref={scrollRef} className="relative min-h-0 flex-1 overflow-auto font-mono text-xs leading-5">
          {line && line <= lines && (
            <div className="pointer-events-none absolute right-0 left-0 bg-primary/10" style={{ top: 8 + (line - 1) * LINE_H, height: LINE_H }} />
          )}
          <div className="flex min-w-max py-2">
            <pre className="sticky left-0 shrink-0 bg-background pr-3 pl-3 text-right text-muted-foreground/60 select-none">
              {Array.from({ length: lines }, (_, i) => i + 1).join('\n')}
            </pre>
            {html !== null ? <pre className="pr-6" dangerouslySetInnerHTML={{ __html: html }} /> : <pre className="pr-6">{data.content}</pre>}
          </div>
          {data.truncated && <p className="px-4 pb-3 font-sans text-xs text-muted-foreground">文件太大，只显示了开头一部分。</p>}
        </div>
      )}
    </div>
  )
}
