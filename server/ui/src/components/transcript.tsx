import { createContext, memo, useContext, useEffect, useMemo, useState } from 'react'
import { BrainIcon, CheckIcon, ChevronRightIcon, CircleDotIcon, CpuIcon, Loader2Icon, XIcon } from 'lucide-react'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { AskedQuestion } from '@/components/ask-question'
import { AttachmentList } from '@/components/attachments'
import { ChangeCard } from '@/components/change-card'
import { Markdown } from '@/components/markdown'
import { ClaudeSpinner } from '@/components/state-dot'
import { hasHunks, Pre, ToolInput, ToolResult, toolIcon, toolSummary } from '@/components/tool-view'
import type { Block, Entry, ToolDetail } from '@/lib/api'
import { splitAttachments, type AttachmentRef } from '@/lib/attachments'
import type { TurnChange } from '@/lib/file-panel'
import { modelName, sameModel } from '@/lib/models'
import type { ToolDetails } from '@/lib/tool-details'
import type { ToolMode } from '@/lib/view'
import { cn } from '@/lib/utils'

// 会话记录的渲染。历史回看和实时聊天共用：Entry 流 → 行（Row）→ 每行一个 memo 组件。
// tool_result 不单独成行，按 tool_use_id 挂回它的工具卡片上。
// 打开会话时拿到的只是最后一页（offset 是第一条在整个会话里的下标，行的 key 按它算，往上插一页不会让下面的重建），
// 工具卡片是精简版（block.lazy），展开时才取参数和输出的全文。

type Row = RowBody & { ei: number }
type RowBody =
  | { kind: 'user'; key: string; text: string; files?: AttachmentRef[]; images?: string[] } // 附件见 lib/attachments.ts
  | { kind: 'command'; key: string; text: string }
  | { kind: 'output'; key: string; text: string }
  | { kind: 'text'; key: string; text: string }
  | { kind: 'thinking'; key: string; text: string }
  | { kind: 'tool'; key: string; block: Block }
  | { kind: 'result'; key: string; block: Block }
  | { kind: 'image'; key: string; src: string; user?: boolean }
  | { kind: 'note'; key: string; text: string }
  | { kind: 'model'; key: string; from: string; to: string }
  | { kind: 'changes'; key: string; files: TurnChange[]; partial?: boolean }

// 用户消息里有不少 CLI 自己包的标签（斜杠命令、! 命令、本地命令输出、系统提醒），分别处理
function userRow(key: string, text: string): RowBody | null {
  const t = text.trim()
  if (t.startsWith('[Request interrupted by user')) return { kind: 'note', key, text: '已中断' }
  const cmd = t.match(/<command-name>([\s\S]*?)<\/command-name>/)
  if (cmd) {
    const args = t.match(/<command-args>([\s\S]*?)<\/command-args>/)?.[1]?.trim()
    return { kind: 'command', key, text: [cmd[1].trim(), args].filter(Boolean).join(' ') }
  }
  // 网页里直接打的斜杠命令（/model sonnet、/compact …）也显示成命令
  if (/^\/[\w:.-]+(\s[^\n]*)?$/.test(t)) return { kind: 'command', key, text: t }
  // 整条都是标签：可能只有一段，也可能好几段连着（task-notification 后面跟一段 system-reminder）
  const tags = [...t.matchAll(/<([a-z-]+)>([\s\S]*?)<\/\1>/g)]
  if (tags.length && !t.replace(/<([a-z-]+)>[\s\S]*?<\/\1>/g, '').trim()) {
    const bash = tags.find((m) => m[1] === 'bash-input')
    if (bash) return { kind: 'command', key, text: `! ${bash[2].trim()}` }
    const out = tags
      .filter((m) => /stdout|stderr/.test(m[1]))
      .map((m) => m[2].trim())
      .filter(Boolean)
      .join('\n')
    return out ? { kind: 'output', key, text: out } : null // system-reminder、local-command-caveat、task-notification 之类
  }
  return { kind: 'user', key, text }
}

function toRows(entries: Entry[], offset: number) {
  const rows: Row[] = []
  const results = new Map<string, Block>()
  const toolIds = new Set<string>()
  let model = '' // 上一条回复是哪个模型：换了（自己切的，或者被降级）就插一行
  entries.forEach((e, i) => {
    const ei = offset + i
    const push = (r: RowBody) => rows.push({ ...r, ei })
    if (e.role === 'assistant' && e.model) {
      if (model && !sameModel(model, e.model)) push({ kind: 'model', key: `${ei}-model`, from: model, to: e.model })
      model = e.model
    }
    // 网页发的消息，附件列在正文的 <attachments> 里；会话记录里 claude 那条还自带图片（base64），跟着附件一起显示
    const user = e.role === 'user'
    const images = user ? e.blocks.filter((b) => b.t === 'image' && b.text).map((b) => b.text!) : []
    const tagged = user && e.blocks.some((b) => b.t === 'text' && b.text?.includes('<attachments>\n'))
    e.blocks.forEach((b, bi) => {
      const key = `${ei}-${bi}`
      if (e.role === 'note') return push({ kind: 'note', key, text: b.text ?? '' })
      switch (b.t) {
        case 'text': {
          if (e.role === 'assistant') {
            push({ kind: 'text', key, text: b.text ?? '' })
            break
          }
          const att = splitAttachments(b.text ?? '')
          const r = userRow(key, att ? att.text : (b.text ?? ''))
          if (!att) {
            if (r) push(r)
          } else if (r?.kind === 'user') push({ ...r, files: att.files, images })
          else {
            if (r) push(r) // 带附件的斜杠命令之类：命令照常显示，附件另起一行
            push({ kind: 'user', key: `${key}-att`, text: '', files: att.files, images })
          }
          break
        }
        case 'thinking':
          push({ kind: 'thinking', key, text: b.text ?? '' })
          break
        case 'tool_use':
          toolIds.add(b.id ?? '')
          push({ kind: 'tool', key, block: b })
          break
        case 'tool_result':
          if (b.id && toolIds.has(b.id)) results.set(b.id, b)
          else push({ kind: 'result', key, block: b })
          break
        case 'image':
          if (b.text && !tagged) push({ kind: 'image', key, src: b.text, user })
          break
      }
    })
  })
  return { rows, results }
}

// ---- 每轮末尾的改动卡片 ----

// 在每轮对话的末尾（下一句用户消息之前、或者最后）插一张卡片，汇总这一轮改了哪些文件。
// 行数优先用工具结果里的（Claude 给的 diff），没有就用参数算的；没跑完的、出错的不算
// partialFirst：第一轮的开头在还没加载的更早一页里，它的卡片可能不全
function withChanges(rows: Row[], results: Map<string, Block>, partialFirst: boolean): Row[] {
  const out: Row[] = []
  let turn: ToolRow[] = []
  let first = partialFirst
  const flush = () => {
    const byPath = new Map<string, TurnChange>()
    for (const t of turn) {
      const res = results.get(t.block.id ?? '')
      if (!res || res.isError) continue
      for (const f of res.files?.length ? res.files : (t.block.files ?? [])) {
        const c = byPath.get(f.path) ?? { path: f.path, add: 0, del: 0, tools: [] }
        c.add += f.add
        c.del += f.del
        if (!c.status && f.status) c.status = f.status
        if (t.block.id && !c.tools.includes(t.block.id)) c.tools.push(t.block.id)
        byPath.set(f.path, c)
      }
    }
    // key 跟着这一轮第一次改动走：一轮还在跑时卡片原地更新，不会重新入场
    if (byPath.size)
      out.push({ kind: 'changes', key: `changes-${turn[0].key}`, ei: turn[turn.length - 1].ei, files: [...byPath.values()], partial: first })
    turn = []
  }
  for (const r of rows) {
    if (r.kind === 'user') {
      flush()
      first = false
    }
    out.push(r)
    if (r.kind === 'tool' && r.block.files?.length) turn.push(r)
  }
  flush()
  return out
}

// ---- 合并 / 半开：连续的工具调用合成一行摘要，像 Claude App 里的「Ran 4 commands >」 ----
// 合并是摘要收着；半开是摘要点开了的样子：下面一个调用一行，参数和输出还收着

type ToolRow = Extract<Row, { kind: 'tool' }>
type Group = { key: string; ei: number; tools: ToolRow[] }

function groupTools(rows: Row[]): (Row | Group)[] {
  const out: (Row | Group)[] = []
  for (const r of rows) {
    const last = out[out.length - 1]
    // 向你提的问题不合进去：它是一次对话，要一直看得到当时选了什么
    if (r.kind !== 'tool' || r.block.name === 'AskUserQuestion') out.push(r)
    else if (last && 'tools' in last) last.tools.push(r)
    else out.push({ key: r.key, ei: r.ei, tools: [r] }) // key 用第一条的：后面续上的调用不会让这一组重新入场
  }
  return out
}

const KINDS: [string[], (n: number) => string][] = [
  [['Bash', 'BashOutput', 'KillShell'], (n) => `运行了 ${n} 个命令`],
  [['Read'], (n) => `读了 ${n} 个文件`],
  [['Edit', 'MultiEdit', 'Write', 'NotebookEdit'], (n) => `改了 ${n} 个文件`],
  [['Grep', 'Glob'], (n) => `搜索了 ${n} 次`],
  [['WebFetch', 'WebSearch'], (n) => `查了 ${n} 次网页`],
  [['Task', 'Agent'], (n) => `派了 ${n} 个子代理`],
  [['TodoWrite'], () => '更新了待办'],
  [['Skill'], (n) => `用了 ${n} 个技能`],
]

function groupLabel(tools: ToolRow[], cwd?: string) {
  if (tools.length === 1) {
    const b = tools[0].block
    const desc = b.name === 'Bash' && typeof b.input?.description === 'string' ? b.input.description : ''
    return desc || `${b.name} ${toolSummary(b.name ?? '', b.input, cwd)}`
  }
  const counts = new Map<string, number>()
  let other = 0
  for (const t of tools) {
    const k = KINDS.find(([names]) => names.includes(t.block.name ?? ''))
    if (k) counts.set(k[0][0], (counts.get(k[0][0]) ?? 0) + 1)
    else other++
  }
  const parts = KINDS.filter(([names]) => counts.has(names[0])).map(([names, fmt]) => fmt(counts.get(names[0])!))
  if (other) parts.push(`调用了 ${other} 次工具`)
  return parts.join('，')
}

const ToolGroup = memo(function ToolGroup({
  tools,
  results,
  cwd,
  live,
  defaultOpen,
}: {
  tools: ToolRow[]
  results: Map<string, Block>
  cwd?: string
  live?: boolean
  defaultOpen?: boolean
}) {
  const running = live ? tools.find((t) => !results.has(t.block.id ?? '')) : undefined
  const errors = tools.filter((t) => results.get(t.block.id ?? '')?.isError).length
  return (
    <Collapsible defaultOpen={defaultOpen}>
      <CollapsibleTrigger className="group/tg flex max-w-full min-w-0 items-center gap-1.5 py-0.5 text-left text-sm text-muted-foreground transition-colors hover:text-foreground">
        {running && <ClaudeSpinner />}
        <span className="truncate">{groupLabel(tools, cwd)}</span>
        {running && tools.length > 1 && (
          <span className="min-w-0 truncate font-mono text-xs opacity-70">· {toolSummary(running.block.name ?? '', running.block.input, cwd)}</span>
        )}
        {errors > 0 && (
          <span className="flex shrink-0 items-center gap-0.5 text-xs text-destructive">
            <XIcon className="size-3" />
            {errors}
          </span>
        )}
        <ChevronRightIcon className="size-3.5 shrink-0 transition-transform group-data-[state=open]/tg:rotate-90" />
      </CollapsibleTrigger>
      <CollapsibleContent className="mt-1.5 space-y-1.5 border-l-2 pl-3">
        {tools.map((t) => (
          <ToolCard
            key={t.key}
            mode="merged"
            block={t.block}
            result={results.get(t.block.id ?? '')}
            cwd={cwd}
            live={live && !results.has(t.block.id ?? '')}
          />
        ))}
      </CollapsibleContent>
    </Collapsible>
  )
})

const ToolDetailsContext = createContext<ToolDetails | undefined>(undefined)

// animateFrom：从第几条 Entry 起算「新来的」（和 offset 一样按整个会话的下标），做一个淡入上浮；打开页面时带来的历史不动
export const Transcript = memo(function Transcript({
  entries,
  offset = 0,
  tools,
  cwd,
  live,
  animateFrom,
  thinkingOpen = false,
  toolMode = 'merged',
}: {
  entries: Entry[]
  offset?: number
  tools?: ToolDetails // 精简过的工具卡片从这里取全文
  cwd?: string
  live?: boolean
  animateFrom?: number
  thinkingOpen?: boolean // 右上角「思考」开关
  toolMode?: ToolMode // 右上角「工具调用」三档
}) {
  const { rows, results } = useMemo(() => {
    const t = toRows(entries, offset)
    return { rows: withChanges(t.rows, t.results, offset > 0 && t.rows[0]?.kind !== 'user'), results: t.results }
  }, [entries, offset])
  const items = useMemo(() => (toolMode === 'expanded' ? rows : groupTools(rows)), [rows, toolMode])
  return (
    <ToolDetailsContext.Provider value={tools}>
      {items.map((r) => (
        <div key={r.key} className={cn(animateFrom !== undefined && r.ei >= animateFrom && 'animate-in duration-300 fade-in slide-in-from-bottom-2')}>
          {'tools' in r ? (
            // key 跟着档位变：切换开关时每一组回到统一状态（单独点开、收起过的也复原）
            <ToolGroup key={toolMode} tools={r.tools} results={results} cwd={cwd} live={live} defaultOpen={toolMode === 'compact'} />
          ) : r.kind === 'changes' ? (
            <ChangeCard files={r.files} cwd={cwd} tools={tools} partial={r.partial} />
          ) : r.kind === 'tool' && r.block.name === 'AskUserQuestion' ? (
            <AskedQuestion
              key={toolMode}
              block={r.block}
              result={results.get(r.block.id ?? '')}
              live={live && !results.has(r.block.id ?? '')}
              tools={tools}
              defaultOpen={toolMode !== 'merged'}
            />
          ) : r.kind === 'tool' ? (
            // key 跟着档位变：切换开关时每张卡片回到统一状态（单独点开过的也收回去）
            <ToolCard
              key={toolMode}
              mode={toolMode}
              block={r.block}
              result={results.get(r.block.id ?? '')}
              cwd={cwd}
              live={live && !results.has(r.block.id ?? '')}
            />
          ) : (
            <RowView row={r} thinkingOpen={thinkingOpen} />
          )}
        </div>
      ))}
    </ToolDetailsContext.Provider>
  )
})

const RowView = memo(function RowView({ row, thinkingOpen }: { row: Row; thinkingOpen: boolean }) {
  switch (row.kind) {
    case 'user':
      return (
        <div className="flex flex-col items-end gap-1.5">
          {row.files && row.files.length > 0 && <AttachmentList files={row.files} images={row.images} />}
          {row.text && (
            <div className="max-w-[85%] rounded-2xl rounded-br-md bg-muted px-3.5 py-2 text-sm whitespace-pre-wrap break-words text-foreground">
              {row.text}
            </div>
          )}
        </div>
      )
    case 'command':
      return (
        <div className="flex justify-end">
          <code className="rounded-md border bg-muted px-2 py-1 font-mono text-xs">{row.text}</code>
        </div>
      )
    case 'output':
      return <Pre clamp={12}>{row.text}</Pre>
    case 'text':
      return <Markdown text={row.text} className="text-sm leading-relaxed" />
    case 'thinking':
      return <Thinking key={String(thinkingOpen)} text={row.text} open={thinkingOpen} />
    case 'result':
      return <ToolResult text={row.block.text ?? ''} isError={row.block.isError} />
    case 'image':
      return row.user ? (
        <div className="flex justify-end">
          <img src={row.src} alt="" className="max-h-40 max-w-[85%] rounded-xl border" />
        </div>
      ) : (
        <img src={row.src} alt="" className="max-h-80 rounded-md border" />
      )
    case 'note':
      return (
        <div className="flex items-center gap-3 py-1 text-xs text-muted-foreground">
          <div className="h-px flex-1 bg-border" />
          {row.text}
          <div className="h-px flex-1 bg-border" />
        </div>
      )
    case 'model':
      return (
        <div className="flex items-center gap-3 py-1 text-xs text-muted-foreground">
          <div className="h-px flex-1 bg-border" />
          <span className="flex items-center gap-1.5" title={`${row.from} → ${row.to}`}>
            <CpuIcon className="size-3" />
            下面由 <span className="font-medium text-foreground">{modelName(row.to)}</span> 回复（之前是 {modelName(row.from)}）
          </span>
          <div className="h-px flex-1 bg-border" />
        </div>
      )
  }
  return null
})

function Thinking({ text, open }: { text: string; open: boolean }) {
  return (
    <Collapsible defaultOpen={open}>
      <CollapsibleTrigger className="group flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground">
        <BrainIcon className="size-3.5" />
        思考过程
        <ChevronRightIcon className="size-3.5 transition-transform group-data-[state=open]:rotate-90" />
      </CollapsibleTrigger>
      <CollapsibleContent>
        <Markdown text={text} className="mt-2 border-l-2 pl-3 text-xs text-muted-foreground" />
      </CollapsibleContent>
    </Collapsible>
  )
}

const ToolCard = memo(function ToolCard({
  block,
  result,
  cwd,
  live,
  mode,
}: {
  block: Block
  result?: Block
  cwd?: string
  live?: boolean
  mode: ToolMode
}) {
  const name = block.name ?? '工具'
  // 组里的卡片（合并、半开）先收着，只看一行摘要；出错的直接展开
  const [open, setOpen] = useState(mode !== 'merged' || !!result?.isError)
  useEffect(() => {
    if (result?.isError) setOpen(true)
  }, [result?.isError])
  return (
    <Collapsible open={open} onOpenChange={setOpen} className="rounded-lg border bg-card">
      <CollapsibleTrigger className="flex w-full min-w-0 items-center gap-2 px-3 py-1.5 text-left text-xs">
        {toolIcon(name)}
        <span className="shrink-0 font-medium">{name}</span>
        <span className="min-w-0 flex-1 truncate font-mono text-muted-foreground">{toolSummary(name, block.input, cwd)}</span>
        {result ? (
          result.isError ? (
            <XIcon className="size-3.5 shrink-0 text-destructive" />
          ) : (
            <CheckIcon className="size-3.5 shrink-0 text-emerald-500" />
          )
        ) : live ? (
          <Loader2Icon className="size-3.5 shrink-0 animate-spin text-muted-foreground" />
        ) : (
          <CircleDotIcon className="size-3.5 shrink-0 text-muted-foreground/50" />
        )}
        <ChevronRightIcon className={cn('size-3.5 shrink-0 text-muted-foreground transition-transform', open && 'rotate-90')} />
      </CollapsibleTrigger>
      <CollapsibleContent className="space-y-2 border-t px-3 py-2">
        <ToolBody name={name} block={block} result={result} full={mode === 'expanded'} />
      </CollapsibleContent>
    </Collapsible>
  )
})

// 卡片展开后的参数和输出。CollapsibleContent 收着时不挂载，所以挂上了就是展开了：精简过的这时才去取全文。
// 改文件的工具，结果里没带 diff 的也去取一次：服务端会在文件里找到改动的位置，补上行号和上下文
function ToolBody({ name, block, result, full }: { name: string; block: Block; result?: Block; full: boolean }) {
  const tools = useContext(ToolDetailsContext)
  const [detail, setDetail] = useState<ToolDetail | 'failed'>()
  const resultDiff = result && !result.lazy && hasHunks(result.files) ? result.files : undefined
  const needDiff = !!block.files?.length && !resultDiff && !!result && !result.isError
  useEffect(() => {
    if ((!block.lazy && !needDiff) || !block.id || !tools) return
    let alive = true
    tools.load(block.id).then((d) => alive && setDetail(d ?? 'failed'))
    return () => {
      alive = false
    }
  }, [block, tools, needDiff])

  if (block.lazy && detail === undefined)
    return (
      <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <Loader2Icon className="size-3 animate-spin" /> 加载中…
      </p>
    )
  if (block.lazy && detail === 'failed') return <p className="text-xs text-destructive">取不到这次调用的详情，收起再展开试试</p>
  const got = detail === 'failed' ? undefined : detail // 只为补 diff 取的，取不到就按参数显示
  // 结果如果是之后实时推来的（没精简过），用它；否则用取回来的全文
  const res = result && !result.lazy ? { text: result.text ?? '', isError: result.isError } : got?.result
  return (
    <>
      <ToolInput name={name} input={got?.input ?? block.input} full={full} files={resultDiff ?? got?.files} />
      {res && <ToolResult text={res.text} isError={res.isError} full={full} />}
    </>
  )
}
