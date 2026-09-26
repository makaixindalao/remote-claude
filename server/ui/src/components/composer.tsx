import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import {
  ArrowUpIcon,
  BotIcon,
  ChevronDownIcon,
  CpuIcon,
  GaugeIcon,
  Loader2Icon,
  PaperclipIcon,
  ShieldIcon,
  SlidersHorizontalIcon,
  SquareIcon,
  TriangleAlertIcon,
} from 'lucide-react'
import { AttachmentTray, DropOverlay, useAttachments, useFileDrop } from '@/components/attachments'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectSeparator, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle, SheetTrigger } from '@/components/ui/sheet'
import { Textarea } from '@/components/ui/textarea'
import { agentLabel, agentModes, dangerMode, modeHint, type Agent } from '@/lib/agents'
import type { Catalog, CommandInfo, ModelInfo } from '@/lib/api'
import { isTouch } from '@/lib/format'
import { modelName, optionName, sameModel } from '@/lib/models'
import type { Settings } from '@/lib/prefs'
import { cn } from '@/lib/utils'

// 输入框：打 / 弹出斜杠命令菜单（CLI 报的全部命令 + 网页自己的几个），
// 下面一排是模型 / effort / 权限模式（新对话前面还有选哪个 CLI）。新对话、续会话、聊天中共用这一个。
// 文件和图片可以粘贴、拖进窗口、点回形针选，选中就开始传，发的时候等它们传完（见 attachments.tsx）。
// 三样都显示具体的值：模型是 Opus 5.5 (1M) 而不是「Default」，effort 没选也写出实际用的那一档；
// 对话里实际回复的模型和选的不一样（被降级）时，模型那一格标黄。

// 网页自己处理的命令（TUI 专有、headless 下没有的）
export type WebCommand = { name: string; description: string; argumentHint?: string; run: (args: string) => void }

type Item = CommandInfo & { web?: WebCommand }

const DEFAULT_EFFORT = '__default__' // Radix Select 不收空字符串
const ALL_EFFORTS = ['low', 'medium', 'high', 'xhigh', 'max']

// 对话里 CLI 报上来的模型：resolved 是请求发给谁，actual 是最近一条回复实际是谁
export type LiveModel = { resolved?: string; actual?: string }

// 权限模式的名字不翻译，但 claude / grok 的 default 读着像「默认」，后面补一句它具体是什么
export const modeText = (agent: Agent, mode: string) => (mode === 'default' && modeHint[agent].default ? `default · ${modeHint[agent].default}` : mode)

// 下拉项的第二行：claude 的 default 是「Claude Code 推荐的那个」，名字已经写成具体的模型，这里交代一句
export const optionHint = (agent: Agent, m: ModelInfo) =>
  m.value === 'default' && agent === 'claude' ? ['Claude Code 推荐', m.description].filter(Boolean).join(' · ') : m.description

type Props = {
  agent?: Agent
  // 给了 onAgent 就在最前面放一个选 CLI 的下拉框（只有新对话能换）
  agents?: Agent[]
  onAgent?: (a: Agent) => void
  catalog?: Catalog
  settings: Settings
  onSettings: (s: Settings) => void
  live?: LiveModel // 对话里才有
  onSend: (text: string, attachments: string[]) => boolean | Promise<boolean> // attachments：传好的附件 id
  webCommands?: WebCommand[]
  running?: boolean
  onInterrupt?: () => void
  disabled?: boolean
  // 聊天进行中不能把 effort 改回「默认」（没有对应的命令），新对话才给这个选项
  allowDefaultEffort?: boolean
  placeholder?: string
  autoFocus?: boolean
  suggestion?: string // CLI 预测的下一句：输入框空着时淡色显示，Tab 采纳
  history?: string[] // 以前发过的消息，最新的在前：↑ / ↓ 翻
}

export function Composer({
  agent = 'claude',
  agents = [],
  onAgent,
  catalog,
  settings,
  onSettings,
  live,
  onSend,
  webCommands = [],
  running,
  onInterrupt,
  disabled,
  allowDefaultEffort,
  placeholder,
  autoFocus,
  suggestion,
  history = [],
}: Props) {
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [sel, setSel] = useState(0)
  const [dismissed, setDismissed] = useState(false)
  const ref = useRef<HTMLTextAreaElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const touch = isTouch()
  // ↑ / ↓ 翻历史：-1 表示在编辑自己的草稿，翻走前先存下来
  const [sent, setSent] = useState<string[]>([])
  const [hIdx, setHIdx] = useState(-1)
  const draft = useRef('')
  const hist = useMemo(() => [...sent, ...history].filter((h, i, a) => h !== a[i - 1]), [sent, history])
  const att = useAttachments()
  const attRef = useRef(att)
  attRef.current = att
  const fileRef = useRef<HTMLInputElement>(null)
  const showSuggestion = !text && !!suggestion && !att.items.length

  const dragging = useFileDrop((files) => {
    attRef.current.add(files)
    if (!touch) ref.current?.focus()
  })

  // 粘贴文件、截图：输入框里，或者焦点不在任何输入框上的时候（别的输入框里粘贴照常）
  useEffect(() => {
    const onPaste = (e: ClipboardEvent) => {
      const t = e.target as HTMLElement | null
      if (t !== ref.current && t?.closest('input, textarea, select, [contenteditable]')) return
      const cd = e.clipboardData
      if (!cd?.files.length) return
      // 从表格、网页、Word 里复制的一段：浏览器顺带给一张渲染出来的图，要的其实是文字
      if (cd.types.includes('text/html') && cd.getData('text/plain').trim()) return
      e.preventDefault()
      attRef.current.add(Array.from(cd.files), true)
      if (!touch) ref.current?.focus()
    }
    document.addEventListener('paste', onPaste)
    return () => document.removeEventListener('paste', onPaste)
  }, [touch])

  function recall(value: string) {
    setText(value)
    setDismissed(true) // 翻出来的 /命令 不要弹菜单
    requestAnimationFrame(() => {
      const el = ref.current
      if (el) el.selectionStart = el.selectionEnd = el.value.length
    })
  }

  // 只在敲命令名的时候弹菜单：以 / 开头、还没打空格
  const query = /^\/(\S*)$/.exec(text)?.[1]
  const items = useMemo<Item[]>(() => {
    if (query === undefined) return []
    const q = query.toLowerCase()
    const all: Item[] = [
      ...webCommands.map((w) => ({ name: w.name, description: w.description, argumentHint: w.argumentHint, web: w })),
      ...(catalog?.commands ?? []).filter((c) => !webCommands.some((w) => w.name === c.name)),
    ]
    return all
      .filter((c) => c.name.toLowerCase().includes(q))
      .sort((a, b) => Number(!a.name.toLowerCase().startsWith(q)) - Number(!b.name.toLowerCase().startsWith(q)))
  }, [query, catalog, webCommands])
  const menuOpen = items.length > 0 && !dismissed

  useEffect(() => {
    setSel(0)
  }, [query])
  useEffect(() => {
    listRef.current?.querySelector(`[data-index="${sel}"]`)?.scrollIntoView({ block: 'nearest' })
  }, [sel])

  async function submit(value = text) {
    const t = value.trim()
    if ((!t && !att.items.length) || disabled || busy) return
    const m = /^\/(\S+)\s*([\s\S]*)$/.exec(t)
    const web = m && webCommands.find((w) => w.name === m[1])
    if (web) {
      web.run(m[2].trim())
      setText('')
      return
    }
    setBusy(true)
    try {
      const files = await att.settle() // 还在传的等它传完；有传失败的这条先不发
      if (!files) return
      if (await onSend(value, files.map((f) => f.id))) {
        setText('')
        att.clear()
        if (t) setSent((s) => [value, ...s])
        setHIdx(-1)
      }
    } finally {
      setBusy(false)
    }
  }

  // Tab 补全；回车补全后，不带参数的命令直接执行（和 TUI 一样）
  function pick(item: Item, execute: boolean) {
    const value = `/${item.name}`
    if (execute && !item.argumentHint) {
      submit(value)
      return
    }
    setText(value + ' ')
    ref.current?.focus()
  }

  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.nativeEvent.isComposing || e.keyCode === 229) return // 输入法选词
    if (menuOpen) {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault()
        setSel((i) => (i + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length)
        return
      }
      if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) {
        e.preventDefault()
        pick(items[sel], e.key === 'Enter')
        return
      }
      if (e.key === 'Escape') {
        e.preventDefault()
        setDismissed(true)
        return
      }
    }
    if (e.key === 'Tab' && !e.shiftKey && showSuggestion) {
      e.preventDefault()
      recall(suggestion!)
      return
    }
    // 光标在第一行按 ↑ / 最后一行按 ↓ 才翻历史，多行编辑时照常移动光标（和 TUI 一样）
    if ((e.key === 'ArrowUp' || e.key === 'ArrowDown') && !e.shiftKey && !e.altKey && !e.metaKey && !e.ctrlKey) {
      const el = e.currentTarget
      if (e.key === 'ArrowUp' && !el.value.slice(0, el.selectionStart).includes('\n') && hIdx < hist.length - 1) {
        e.preventDefault()
        if (hIdx === -1) draft.current = text
        setHIdx(hIdx + 1)
        recall(hist[hIdx + 1])
        return
      }
      if (e.key === 'ArrowDown' && !el.value.slice(el.selectionEnd).includes('\n') && hIdx >= 0) {
        e.preventDefault()
        setHIdx(hIdx - 1)
        recall(hIdx - 1 < 0 ? draft.current : hist[hIdx - 1])
        return
      }
    }
    if (e.key !== 'Enter') return
    if (e.metaKey || e.ctrlKey || (!e.shiftKey && !touch)) {
      e.preventDefault()
      submit()
    }
  }

  const pickerProps: PickerProps = { agent, agents: onAgent ? agents : [], onAgent, catalog, settings, onSettings, live, allowDefaultEffort }

  return (
    <div className="shrink-0 border-t bg-background px-3 pt-3 pb-[max(0.75rem,env(safe-area-inset-bottom))]">
      <div className="relative mx-auto max-w-3xl">
        {menuOpen && (
          <div
            ref={listRef}
            className="absolute right-0 bottom-full left-0 z-20 mb-2 max-h-72 overflow-y-auto rounded-xl border bg-popover p-1 text-popover-foreground shadow-lg"
          >
            {items.map((c, i) => (
              <button
                key={c.name}
                data-index={i}
                onMouseDown={(e) => e.preventDefault()} // 别抢走输入框的焦点
                onMouseEnter={() => setSel(i)}
                onClick={() => pick(c, true)}
                className={cn('flex w-full items-baseline gap-2 rounded-lg px-2.5 py-1.5 text-left text-sm', i === sel && 'bg-muted')}
              >
                <span className="shrink-0 font-mono font-medium">/{c.name}</span>
                {c.argumentHint && <span className="shrink-0 font-mono text-xs text-muted-foreground">{c.argumentHint}</span>}
                <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">{c.description}</span>
                {c.web && (
                  <Badge variant="secondary" className="shrink-0 text-3xs">
                    网页
                  </Badge>
                )}
              </button>
            ))}
          </div>
        )}

        {dragging && <DropOverlay agent={agent} />}
        {/* 输入框一进页面就聚焦，焦点态常驻，所以只换边框色，不加一圈光晕 */}
        <div className="rounded-xl border bg-card shadow-xs transition-colors focus-within:border-ring/70">
          <AttachmentTray att={att} />
          <Textarea
            ref={ref}
            value={text}
            autoFocus={autoFocus && !touch}
            onChange={(e) => {
              setText(e.target.value)
              setDismissed(false)
              setHIdx(-1)
            }}
            onKeyDown={onKeyDown}
            rows={1}
            placeholder={
              showSuggestion
                ? suggestion
                : att.items.length
                  ? '说说要拿它们做什么，也可以直接发'
                  : (placeholder ?? (touch ? `给 ${agentLabel[agent]} 发消息，/ 看命令` : `给 ${agentLabel[agent]} 发消息（Enter 发送，Shift+Enter 换行，/ 看命令）`))
            }
            className="max-h-[40dvh] min-h-11 resize-none border-0 bg-transparent px-3 pt-2.5 shadow-none focus-visible:ring-0 dark:bg-transparent"
          />
          <div className="flex items-center gap-1 px-1.5 pb-1.5">
            <Button
              size="icon-sm"
              variant="ghost"
              className="shrink-0 text-muted-foreground"
              onClick={() => fileRef.current?.click()}
              aria-label="添加文件或图片"
              title="添加文件或图片（也可以直接粘贴、拖进来）"
            >
              <PaperclipIcon />
            </Button>
            <input
              ref={fileRef}
              type="file"
              multiple
              hidden
              onChange={(e) => {
                att.add(Array.from(e.target.files ?? []))
                e.target.value = '' // 同一个文件去掉后还能再选
              }}
            />
            {/* 窄屏上一排放不下，收成一个按钮，点开从底下弹出来选 */}
            <div className="hidden min-w-0 sm:block">
              <Pickers {...pickerProps} />
            </div>
            <PickerSheet {...pickerProps} />
            <div className="ml-auto flex shrink-0 items-center gap-1.5">
              {showSuggestion && (
                <button
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => recall(suggestion!)}
                  className="flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs text-muted-foreground hover:bg-muted hover:text-foreground"
                >
                  <kbd className="rounded border bg-muted px-1 font-mono text-3xs">Tab</kbd> 采纳建议
                </button>
              )}
              {running && onInterrupt && (
                <Button size="icon-sm" variant="outline" onClick={onInterrupt} aria-label="中断">
                  <SquareIcon />
                </Button>
              )}
              <Button
                size="icon-sm"
                onClick={() => submit()}
                disabled={disabled || busy || (!text.trim() && !att.items.length)}
                aria-label={busy && att.uploading ? '等附件传完就发' : '发送'}
              >
                {busy ? <Loader2Icon className="animate-spin" /> : <ArrowUpIcon />}
              </Button>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

const triggerCls = 'h-7 gap-1 border-0 bg-transparent px-2 text-xs text-muted-foreground shadow-none hover:bg-muted hover:text-foreground dark:bg-transparent dark:hover:bg-muted'

type PickerProps = {
  agent: Agent
  agents: Agent[]
  onAgent?: (a: Agent) => void
  catalog?: Catalog
  settings: Settings
  onSettings: (s: Settings) => void
  live?: LiveModel
  allowDefaultEffort?: boolean
}

function pickerOptions(agent: Agent, catalog: Catalog | undefined, settings: Settings, live?: LiveModel) {
  const models = catalog?.models ?? []
  const current = models.find((m) => m.value === settings.model)
  // 目录还没到时 claude 先给全部档位（codex / grok 的档位随模型变，等目录）；Haiku 这种不支持 effort 的就不显示
  const efforts = catalog?.models ? (current?.supportedEffortLevels ?? []) : agent === 'claude' ? ALL_EFFORTS : []
  // 模型：对话里以最近一条回复实际用的为准，其次是 CLI 解析成的全名；还没开的新对话看目录里这一项是哪个模型
  const requested = live?.resolved || current?.resolvedModel
  const shown = live?.actual || requested
  const model = shown ? modelName(shown, catalog) : current ? optionName(current) : catalog || settings.model !== 'default' ? settings.model : '…'
  const degraded = !!(live?.actual && requested && !sameModel(live.actual, requested))
  const degradedText = degraded ? `选的是 ${modelName(requested!, catalog)}，最近一条回复实际来自 ${modelName(live!.actual!, catalog)}` : ''
  // effort：没指定就是模型自己的那一档（对话里 CLI 会报具体值，新对话看目录里问来的）
  const effort = (settings.effort && efforts.includes(settings.effort) ? settings.effort : '') || current?.defaultEffort || ''
  const effortLabel = effort || (catalog ? 'effort' : '…')
  return { models, current, efforts, model, degraded, degradedText, effortLabel }
}

// 窄屏：一个按钮上写着现在的选择，点开从底下弹出同一套下拉框
function PickerSheet(props: PickerProps) {
  const { agent, agents, onAgent, catalog, settings, live } = props
  const { efforts, model, degraded, effortLabel } = pickerOptions(agent, catalog, settings, live)
  const summary = [onAgent && agents.length > 1 && agentLabel[agent], model, efforts.length > 0 && effortLabel, modeText(agent, settings.mode)]
    .filter(Boolean)
    .join(' · ')

  return (
    <Sheet>
      <SheetTrigger asChild>
        <button
          type="button"
          className={cn(
            triggerCls,
            'flex min-w-0 items-center rounded-md sm:hidden',
            settings.mode === dangerMode[agent] ? 'text-destructive' : degraded && 'text-amber-600 dark:text-amber-400',
          )}
        >
          {degraded ? <TriangleAlertIcon className="size-3.5 shrink-0" /> : <SlidersHorizontalIcon className="size-3.5 shrink-0" />}
          <span className="truncate">{summary}</span>
          <ChevronDownIcon className="size-3.5 shrink-0 opacity-50" />
        </button>
      </SheetTrigger>
      <SheetContent side="bottom" className="gap-0 pb-[max(1rem,env(safe-area-inset-bottom))]">
        <SheetHeader>
          <SheetTitle>对话设置</SheetTitle>
          <SheetDescription>当场生效，下次打开还是这套</SheetDescription>
        </SheetHeader>
        <div className="px-4">
          <Pickers {...props} stacked />
        </div>
      </SheetContent>
    </Sheet>
  )
}

function Pickers({
  agent,
  agents,
  onAgent,
  catalog,
  settings,
  onSettings,
  live,
  allowDefaultEffort,
  stacked,
}: PickerProps & { stacked?: boolean }) {
  const { models, current, efforts, model, degraded, degradedText, effortLabel } = pickerOptions(agent, catalog, settings, live)
  const set = (patch: Partial<Settings>) => onSettings({ ...settings, ...patch })
  // stacked：弹出面板里一项一行，前面带名字
  const trigger = stacked ? 'w-full data-[size=sm]:h-10 [&>span]:flex-1 [&>span]:text-left' : triggerCls
  const field = (label: string, control: ReactNode) =>
    stacked ? (
      <label className="grid gap-1.5">
        <span className="text-xs text-muted-foreground">{label}</span>
        {control}
      </label>
    ) : (
      control
    )

  return (
    <div className={stacked ? 'grid gap-4' : 'flex min-w-0 flex-wrap items-center'}>
      {onAgent &&
        agents.length > 1 &&
        field(
          'CLI',
          <Select value={agent} onValueChange={(v) => onAgent(v as Agent)}>
            <SelectTrigger size="sm" className={cn(trigger, 'font-medium text-foreground')} aria-label="CLI">
              <BotIcon className="size-3.5" />
              <SelectValue />
            </SelectTrigger>
            <SelectContent position="popper" side="top" align="start">
              {agents.map((a) => (
                <SelectItem key={a} value={a}>
                  {agentLabel[a]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>,
        )}

      {field(
        '模型',
        <Select value={settings.model} onValueChange={(v) => set({ model: v })}>
          <SelectTrigger
            size="sm"
            className={cn(trigger, degraded && 'text-amber-600 hover:text-amber-600 dark:text-amber-400 dark:hover:text-amber-400')}
            aria-label={degraded ? `模型：${degradedText}` : `模型：${model}`}
            title={degradedText || undefined}
          >
            {degraded ? <TriangleAlertIcon className="size-3.5" /> : <CpuIcon className="size-3.5" />}
            {/* 下拉项里带了说明文字，触发器上只显示名字 */}
            <span className="truncate">{model}</span>
          </SelectTrigger>
          <SelectContent position="popper" side="top" align="start">
            {/* 手机上没有悬停提示，降级的说明放在下拉的最上面 */}
            {degraded && (
              <div className="flex w-0 min-w-full items-start gap-1.5 px-2 py-1.5 text-xs text-amber-700 dark:text-amber-300">
                <TriangleAlertIcon className="mt-0.5 size-3 shrink-0" />
                {degradedText}
              </div>
            )}
            {!current && <SelectItem value={settings.model}>{model}</SelectItem>}
            {models.map((m) => {
              const hint = optionHint(agent, m)
              return (
                <SelectItem key={m.value} value={m.value}>
                  <span className="grid">
                    <span>{optionName(m)}</span>
                    {hint && <span className="text-xs text-muted-foreground">{hint}</span>}
                  </span>
                </SelectItem>
              )
            })}
          </SelectContent>
        </Select>,
      )}
      {stacked && degraded && <p className="-mt-2 text-xs text-amber-700 dark:text-amber-300">{degradedText}</p>}

      {efforts.length > 0 &&
        field(
          'effort',
          <Select
            value={settings.effort && efforts.includes(settings.effort) ? settings.effort : DEFAULT_EFFORT}
            onValueChange={(v) => v !== DEFAULT_EFFORT && set({ effort: v })}
          >
            <SelectTrigger size="sm" className={trigger} aria-label={`effort：${effortLabel}`}>
              {/* 大脑图标留给页头的「思考过程」开关 */}
              <GaugeIcon className="size-3.5" />
              <span className="truncate">{effortLabel}</span>
            </SelectTrigger>
            <SelectContent position="popper" side="top" align="start">
              {/* 不指定：各模型用自己的那一档（换模型时跟着变）。只有新对话能选回来，聊天中没有对应的命令 */}
              {(allowDefaultEffort || !settings.effort) && (
                <SelectItem value={DEFAULT_EFFORT}>跟着模型{current?.defaultEffort && `（${current.defaultEffort}）`}</SelectItem>
              )}
              {efforts.map((e) => (
                <SelectItem key={e} value={e}>
                  {e}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>,
        )}

      {field(
        '权限模式',
        <Select value={settings.mode} onValueChange={(v) => set({ mode: v })}>
          <SelectTrigger size="sm" className={cn(trigger, settings.mode === dangerMode[agent] && 'text-destructive')} aria-label="权限模式">
            <ShieldIcon className="size-3.5" />
            {/* 下拉项里带了说明，触发器上只显示名字（default 除外，见 modeText） */}
            <span className="truncate">{modeText(agent, settings.mode)}</span>
          </SelectTrigger>
          <SelectContent position="popper" side="top" align="start">
            {agentModes[agent].map((m) => {
              // 不再逐个确认的那一档和别的隔开、标红（选它不弹确认，这是有意的）
              const danger = m === dangerMode[agent]
              return [
                danger && <SelectSeparator key={`${m}-sep`} />,
                <SelectItem key={m} value={m} className={cn(danger && 'text-destructive focus:text-destructive')}>
                  <span className="grid">
                    <span className="flex items-center gap-1 font-mono text-xs">
                      {danger && <TriangleAlertIcon className="size-3 text-destructive" />}
                      {m}
                    </span>
                    {modeHint[agent][m] && (
                      <span className={cn('text-xs', danger ? 'text-destructive/80' : 'text-muted-foreground')}>{modeHint[agent][m]}</span>
                    )}
                  </span>
                </SelectItem>,
              ]
            })}
          </SelectContent>
        </Select>,
      )}
    </div>
  )
}

// 模型不支持当前 effort 时就不带 effort，免得 CLI 拒绝启动
export function effectiveEffort(catalog: Catalog | undefined, s: Settings) {
  const m = catalog?.models?.find((x) => x.value === s.model)
  if (catalog?.models && !m?.supportedEffortLevels?.includes(s.effort)) return ''
  return s.effort
}
