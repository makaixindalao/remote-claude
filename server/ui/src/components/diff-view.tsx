import { Fragment, useEffect, useMemo, useState } from 'react'
import { ChevronsUpDownIcon } from 'lucide-react'
import type { Hunk } from '@/lib/api'
import { langFromPath, loadHljs } from '@/lib/highlight'
import { cn } from '@/lib/utils'

// 一个文件的 diff：行号、加删底色、语法高亮。没改的长段收成「N 行未改动」，点一下展开。
// 工作区的 diff 上下文是整个文件，所以能一直展开到头；这一轮的 diff 只有改动附近几行

const CTX = 3 // 改动上下各留几行没改的

type Line = { k: ' ' | '+' | '-'; text: string; no?: number; idx: number }
type Piece = { t: 'lines'; lines: Line[] } | { t: 'fold'; id: string; lines: Line[] } | { t: 'gap' }

// 统计栏里那种「+12 -3」
export function DiffStat({ add, del, binary, className }: { add: number; del: number; binary?: boolean; className?: string }) {
  if (binary) return <span className={cn('shrink-0 text-xs text-muted-foreground', className)}>二进制</span>
  return (
    <span className={cn('shrink-0 font-mono text-xs tabular-nums', className)}>
      <span className="text-emerald-600 dark:text-emerald-400">+{add}</span> <span className="text-red-600 dark:text-red-400">-{del}</span>
    </span>
  )
}

export function DiffView({ hunks, path }: { hunks: Hunk[]; path: string }) {
  const { pieces, lines, numbered } = useMemo(() => layout(hunks), [hunks])
  const [open, setOpen] = useState<Set<string>>(() => new Set())
  const html = useHighlight(lines, path)

  if (lines.length === 0) return <p className="px-3 py-2 text-xs text-muted-foreground">没有可显示的改动</p>
  return (
    <div className="font-mono text-xs leading-5">
      {pieces.map((p, i) => {
        if (p.t === 'gap') return <div key={i} className="h-5 border-y border-dashed bg-muted/40" />
        if (p.t === 'lines') return <Fragment key={i}>{p.lines.map((l) => <Row key={l.idx} line={l} numbered={numbered} html={html?.[l.idx]} />)}</Fragment>
        if (open.has(p.id)) return <Fragment key={i}>{p.lines.map((l) => <Row key={l.idx} line={l} numbered={numbered} html={html?.[l.idx]} />)}</Fragment>
        return (
          <button
            key={i}
            type="button"
            className="flex w-full items-center gap-2 bg-muted/50 py-1 pl-3 text-left font-sans text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            onClick={() => setOpen((s) => new Set(s).add(p.id))}
          >
            <ChevronsUpDownIcon className="size-3.5" />
            {p.lines.length} 行未改动
          </button>
        )
      })}
    </div>
  )
}

function Row({ line, numbered, html }: { line: Line; numbered: boolean; html?: string }) {
  const { k } = line
  return (
    <div className={cn('flex', k === '+' && 'bg-emerald-500/12', k === '-' && 'bg-red-500/12')}>
      <span
        className={cn(
          'shrink-0 border-l-2 border-transparent text-right text-muted-foreground/60 tabular-nums select-none',
          numbered ? 'w-12 pr-2' : 'w-1',
          k === '+' && 'border-emerald-500 text-emerald-700 dark:text-emerald-400',
          k === '-' && 'border-red-500 text-red-700 dark:text-red-400',
        )}
      >
        {numbered ? line.no : ''}
      </span>
      <span className={cn('w-4 shrink-0 text-center select-none', k === '+' ? 'text-emerald-700 dark:text-emerald-400' : 'text-red-700 dark:text-red-400')}>
        {k === ' ' ? '' : k}
      </span>
      {html !== undefined ? (
        <span className="min-w-0 flex-1 pr-3 break-words whitespace-pre-wrap" dangerouslySetInnerHTML={{ __html: html || ' ' }} />
      ) : (
        <span className="min-w-0 flex-1 pr-3 break-words whitespace-pre-wrap">{line.text || ' '}</span>
      )}
    </div>
  )
}

// 把 hunks 摊成一行行，标上行号（删掉的行用旧行号，其余用新行号），再把没改的长段收起来
function layout(hunks: Hunk[]) {
  const numbered = hunks.some((h) => h.oldStart > 0 || h.newStart > 0)
  const lines: Line[] = []
  const pieces: Piece[] = []
  hunks.forEach((h, hi) => {
    let o = h.oldStart
    let n = h.newStart
    const hl: Line[] = []
    for (const raw of h.lines) {
      const k = raw === '' ? ' ' : raw[0]
      if (k !== ' ' && k !== '+' && k !== '-') continue // \ No newline at end of file
      const line: Line = { k, text: raw.slice(1), idx: lines.length, no: k === '-' ? o : n }
      if (k !== '+') o++
      if (k !== '-') n++
      lines.push(line)
      hl.push(line)
    }
    // 没改的一段：开头那段只留贴着改动的后几行，结尾那段只留前几行，中间的两头都留
    const first = hl.findIndex((l) => l.k !== ' ')
    let last = -1
    for (let i = hl.length - 1; i >= 0; i--)
      if (hl[i].k !== ' ') {
        last = i
        break
      }
    let i = 0
    while (i < hl.length) {
      let j = i
      if (hl[i].k === ' ') {
        while (j < hl.length && hl[j].k === ' ') j++
        const run = hl.slice(i, j)
        const head = first >= 0 && i > first ? CTX : 0
        const tail = last >= 0 && j - 1 < last ? CTX : 0
        if (first >= 0 && run.length > head + tail + 1) {
          push(pieces, run.slice(0, head))
          pieces.push({ t: 'fold', id: `${hi}-${i}`, lines: run.slice(head, run.length - tail) })
          push(pieces, run.slice(run.length - tail))
        } else push(pieces, run)
      } else {
        while (j < hl.length && hl[j].k !== ' ') j++
        push(pieces, hl.slice(i, j))
      }
      i = j
    }
    // 两段 hunk 之间隔着没给出来的行
    if (hi < hunks.length - 1) pieces.push({ t: 'gap' })
  })
  return { pieces, lines, numbered }
}

function push(pieces: Piece[], lines: Line[]) {
  if (lines.length === 0) return
  const last = pieces[pieces.length - 1]
  if (last?.t === 'lines') last.lines.push(...lines)
  else pieces.push({ t: 'lines', lines: [...lines] })
}

// 逐行高亮：跨行的注释、字符串会断开，换来的是折叠 / 展开时不用重算整个文件
function useHighlight(lines: Line[], path: string) {
  const [html, setHtml] = useState<string[] | null>(null)
  useEffect(() => {
    setHtml(null)
    const lang = langFromPath(path)
    if (!lang || lines.length === 0 || lines.length > 8000) return
    let alive = true
    loadHljs().then((hljs) => {
      if (!alive || !hljs.getLanguage(lang)) return
      setHtml(lines.map((l) => (l.text ? hljs.highlight(l.text, { language: lang, ignoreIllegals: true }).value : '')))
    })
    return () => {
      alive = false
    }
  }, [lines, path])
  return html
}
