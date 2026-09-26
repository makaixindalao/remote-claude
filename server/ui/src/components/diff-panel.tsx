import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { ArrowRightIcon, ChevronDownIcon, ChevronRightIcon, CodeXmlIcon, GitCompareArrowsIcon, RotateCcwIcon, XIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Skeleton } from '@/components/ui/skeleton'
import { DiffStat, DiffView } from '@/components/diff-view'
import { api, type DiffSummary, type FileChange, type Hunk } from '@/lib/api'
import { closePanel, type DiffTarget, type TurnChange } from '@/lib/file-panel'
import { shortPath } from '@/lib/format'
import { cn } from '@/lib/utils'

// 右侧的改动面板。两种范围，标题上的下拉切换：
//   工作区：git diff HEAD，未提交的全部改动（和 Claude App 里的「main → working tree」一样）
//   这一轮：点开的那张卡片对应的那轮对话里，工具改了什么（改动已经提交了、或者不是 git 仓库时也看得到）
// 默认看工作区；不是 git 仓库、或者点的那个文件在工作区里已经没有改动，就看这一轮

type Scope = 'worktree' | 'turn'

const STATUS: Record<string, string> = { add: '新文件', delete: '删除', rename: '改名', untracked: '未跟踪' }

export function DiffPanel({ target, cwd, project }: { target: DiffTarget; cwd?: string; project: string }) {
  const [summary, setSummary] = useState<DiffSummary | null>(null)
  const [error, setError] = useState('')
  const [picked, setPicked] = useState<Scope | null>(null)
  const [nonce, setNonce] = useState(0)

  const query = useMemo(() => {
    const q = new URLSearchParams({ project })
    if (cwd) q.set('cwd', cwd)
    return q.toString()
  }, [cwd, project])

  useEffect(() => {
    setSummary(null)
    setError('')
    api<DiffSummary>(`/api/diff?${query}`)
      .then(setSummary)
      .catch((e) => setError(e.message))
  }, [query, nonce])

  const focusRel = summary && target.focus ? matchPath(summary, target.focus, cwd) : undefined
  const auto: Scope | null = error || summary?.notRepo || (summary && target.focus && !focusRel) ? 'turn' : summary ? 'worktree' : null
  const scope = picked ?? auto

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-9 shrink-0 items-center gap-1.5 border-b px-3 text-xs text-muted-foreground">
        <GitCompareArrowsIcon className="size-3.5 shrink-0" />
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button type="button" className="flex min-w-0 items-center gap-1 rounded-md px-1.5 py-0.5 hover:bg-muted hover:text-foreground">
              {scope === 'turn' ? (
                <span className="truncate text-foreground">这一轮的改动</span>
              ) : (
                <>
                  <span className="truncate">{summary?.branch || 'HEAD'}</span>
                  <ArrowRightIcon className="size-3 shrink-0" />
                  <span className="shrink-0 text-foreground">工作区</span>
                </>
              )}
              <ChevronDownIcon className="size-3 shrink-0" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            <DropdownMenuRadioGroup value={scope ?? ''} onValueChange={(v) => setPicked(v as Scope)}>
              <DropdownMenuRadioItem value="worktree" disabled={!summary || !!summary.notRepo}>
                工作区 · 未提交的全部改动
                {summary?.notRepo && <span className="text-muted-foreground">（{summary.notRepo}）</span>}
              </DropdownMenuRadioItem>
              <DropdownMenuRadioItem value="turn">这一轮 · {target.turn.length} 个文件</DropdownMenuRadioItem>
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
        <span className="flex-1" />
        {scope === 'worktree' && (
          <Button size="icon-xs" variant="ghost" aria-label="刷新" onClick={() => setNonce((n) => n + 1)}>
            <RotateCcwIcon />
          </Button>
        )}
        <Button size="icon-xs" variant="ghost" aria-label="关闭" onClick={closePanel}>
          <XIcon />
        </Button>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {scope === null && (
          <div className="space-y-2 p-4">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-8" />
            ))}
          </div>
        )}
        {scope === 'worktree' && summary && <Worktree key={nonce} summary={summary} query={query} focus={focusRel} target={target} onTurn={() => setPicked('turn')} />}
        {scope === 'turn' && <Turn target={target} cwd={cwd} note={picked ? undefined : error || summary?.notRepo || (target.focus && summary ? '这个文件在工作区里没有未提交的改动（可能已经提交了）' : undefined)} />}
      </div>
    </div>
  )
}

function Worktree({ summary, query, focus, target, onTurn }: { summary: DiffSummary; query: string; focus?: string; target: DiffTarget; onTurn: () => void }) {
  const files = summary.files
  const open = defaultOpen(files, focus)
  if (files.length === 0)
    return (
      <div className="space-y-2 p-4 text-sm text-muted-foreground">
        <p>工作区没有未提交的改动。</p>
        <Button size="sm" variant="outline" onClick={onTurn}>
          看这一轮改了什么
        </Button>
      </div>
    )
  return (
    <>
      {target.focus && !focus && (
        <p className="border-b px-3 py-2 text-xs text-muted-foreground">
          「{target.focus.split('/').pop()}」在工作区里没有未提交的改动（可能已经提交了），
          <button type="button" className="underline underline-offset-2 hover:text-foreground" onClick={onTurn}>
            看这一轮的
          </button>
        </p>
      )}
      {files.some((f) => !open(f.path)) && <p className="px-3 py-2 text-xs text-muted-foreground">改动多的文件先收着，点文件名展开。</p>}
      {files.map((f) => (
        <FileSection
          key={f.path}
          file={f}
          label={f.path}
          defaultOpen={open(f.path)}
          focus={f.path === focus}
          load={() =>
            api<FileChange>(
              `/api/diff/file?${query}&${new URLSearchParams({ path: f.path, status: f.status ?? '', ...(f.oldPath ? { oldPath: f.oldPath } : {}) })}`,
            )
          }
        />
      ))}
      {summary.more && <p className="px-3 py-2 text-xs text-muted-foreground">文件太多，只列了前 {files.length} 个。</p>}
    </>
  )
}

function Turn({ target, cwd, note }: { target: DiffTarget; cwd?: string; note?: string }) {
  const focus = target.focus
  const open = defaultOpen(target.turn, focus)
  return (
    <>
      {note && <p className="border-b px-3 py-2 text-xs text-muted-foreground">{note}，下面是这一轮对话里的改动。</p>}
      {target.turn.map((t) => (
        <FileSection
          key={t.path}
          file={{ path: t.path, add: t.add, del: t.del, status: t.status as FileChange['status'] }}
          label={shortPath(t.path, cwd)}
          defaultOpen={open(t.path)}
          focus={t.path === focus}
          load={() => turnDiff(t, target)}
        />
      ))}
    </>
  )
}

// 有点名的文件就只展开它（其余的展开后会把它往下挤）；没点名、改动又不多就全展开
function defaultOpen(files: { path: string; add: number; del: number }[], focus?: string) {
  const small = files.length <= 8 && files.reduce((n, f) => n + f.add + f.del, 0) <= 600
  return (path: string) => (focus ? path === focus : small)
}

// 这一轮里对这个文件的每次改动，按工具 id 取带 hunks 的 diff，按先后接起来
async function turnDiff(t: TurnChange, target: DiffTarget): Promise<FileChange> {
  if (!target.tools) throw new Error('取不到这一轮的改动详情')
  const details = await Promise.all(t.tools.map((id) => target.tools!.load(id)))
  const hunks: Hunk[] = []
  let truncated = false
  for (const d of details) {
    for (const f of d?.files ?? []) {
      if (f.path !== t.path) continue
      hunks.push(...(f.hunks ?? []))
      truncated ||= !!f.truncated
    }
  }
  if (details.some((d) => !d)) throw new Error('有几次改动取不到详情，收起再展开试试')
  return { path: t.path, status: t.status as FileChange['status'], add: t.add, del: t.del, hunks, truncated }
}

type Loaded = FileChange | 'loading' | { error: string }

function FileSection({
  file,
  label,
  defaultOpen,
  focus,
  load,
}: {
  file: FileChange
  label: string
  defaultOpen: boolean
  focus: boolean
  load: () => Promise<FileChange>
}) {
  const [open, setOpen] = useState(defaultOpen)
  const [data, setData] = useState<Loaded>()
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open || data) return
    setData('loading')
    load()
      .then(setData)
      .catch((e) => setData({ error: e.message }))
  }, [open, data, load])

  useLayoutEffect(() => {
    if (focus) ref.current?.scrollIntoView({ block: 'start' })
  }, [focus])

  const slash = label.lastIndexOf('/')
  const name = label.slice(slash + 1)
  const dir = slash > 0 ? label.slice(0, slash) : ''
  return (
    <div ref={ref} className="border-b">
      <button
        type="button"
        className={cn(
          'sticky top-0 z-10 flex w-full min-w-0 items-center gap-2 bg-muted/80 px-3 py-2 text-left backdrop-blur transition-colors hover:bg-muted',
          focus && 'bg-primary/8',
        )}
        onClick={() => setOpen((o) => !o)}
        title={file.oldPath ? `${file.oldPath} → ${file.path}` : file.path}
      >
        <ChevronRightIcon className={cn('size-3.5 shrink-0 text-muted-foreground transition-transform', open && 'rotate-90')} />
        <CodeXmlIcon className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="shrink-0 truncate text-sm font-medium">{name}</span>
        {dir && <span className="min-w-0 truncate text-xs text-muted-foreground">{dir}</span>}
        {file.status && STATUS[file.status] && (
          <span className="shrink-0 rounded bg-background px-1.5 text-3xs leading-4 text-muted-foreground ring-1 ring-border">{STATUS[file.status]}</span>
        )}
        <DiffStat add={file.add} del={file.del} binary={file.binary} className="ml-auto pl-2" />
      </button>
      {open && <FileBody data={data} path={file.path} />}
    </div>
  )
}

function FileBody({ data, path }: { data?: Loaded; path: string }) {
  if (!data || data === 'loading')
    return (
      <div className="space-y-1.5 p-3">
        {[70, 45, 60].map((w) => (
          <Skeleton key={w} className="h-3.5" style={{ width: `${w}%` }} />
        ))}
      </div>
    )
  if ('error' in data) return <p className="px-3 py-2 text-xs text-destructive">{data.error}</p>
  if (data.binary) return <p className="px-3 py-2 text-xs text-muted-foreground">二进制文件，不显示内容。</p>
  return (
    <>
      <DiffView hunks={data.hunks ?? []} path={path} />
      {data.truncated && <p className="px-3 py-2 text-xs text-muted-foreground">改动太长，只显示了前面一部分。</p>}
    </>
  )
}

// 工具给的路径（一般是绝对路径）对到仓库里的相对路径；符号链接让前缀对不上时按结尾匹配
function matchPath(summary: DiffSummary, focus: string, cwd?: string) {
  const abs = focus.startsWith('/') || !cwd ? focus : `${cwd}/${focus}`
  const root = summary.root
  if (root && abs.startsWith(root + '/')) {
    const rel = abs.slice(root.length + 1)
    if (summary.files.some((f) => f.path === rel)) return rel
  }
  let best: string | undefined
  for (const f of summary.files) {
    if ((abs === f.path || abs.endsWith('/' + f.path)) && f.path.length > (best?.length ?? 0)) best = f.path
  }
  return best
}
