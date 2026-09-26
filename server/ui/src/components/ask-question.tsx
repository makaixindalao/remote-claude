import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { CheckIcon, ChevronRightIcon, Loader2Icon, MessageCircleQuestionIcon, PenLineIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import type { Block, ToolDetail } from '@/lib/api'
import type { ToolDetails } from '@/lib/tool-details'
import { cn } from '@/lib/utils'

// AskUserQuestion（codex 的 request_user_input 也翻成它）：
//   - QuestionForm：等你回答时的选项按钮。单选点一下就选中，多选可勾多个；最后一行「其他」自己写。
//     卡片有焦点时按 1–9 选当前这题的选项，Enter 提交
//   - AskedQuestion：会话记录里的这次提问，同样的选项按钮（只读），标出当时选了哪个
// 答案格式跟 CLI 的约定走：answers[问题原文] = 选中的 label，多选用 ", " 连起来，「其他」的自填文本也放进去

export type AskOption = { label: string; description?: string; preview?: string }
export type AskQuestion = { question: string; header?: string; multiSelect?: boolean; options?: AskOption[] }

export const questionsOf = (input?: Record<string, unknown>): AskQuestion[] =>
  (Array.isArray(input?.questions) ? input.questions : []).filter((q): q is AskQuestion => typeof q?.question === 'string')

// ---- 选项：两边共用，照 claude 网页的样子做成一颗颗圆润的「鹅卵石」 ----
// 没选的是暖沙色，选中的是实心橙色；只有标题的选项是胶囊形，带说明的圆角大一点的卵石形

function OptionPebble({
  index,
  option,
  selected,
  multi,
  readOnly,
  onClick,
}: {
  index: number
  option: AskOption
  selected: boolean
  multi?: boolean
  readOnly?: boolean
  onClick?: () => void
}) {
  // 模型有时把 label 原样抄一遍当说明，那就不显示了
  const desc = !!option.description && option.description.trim() !== option.label.trim()
  return (
    <button
      type="button"
      role={multi ? 'checkbox' : 'radio'}
      aria-checked={selected}
      aria-disabled={readOnly}
      tabIndex={readOnly ? -1 : undefined}
      onClick={readOnly ? undefined : onClick}
      className={cn(
        'inline-flex max-w-full gap-2 text-left text-sm ring-1 transition-all duration-150 ring-inset',
        desc ? 'items-start rounded-[1.35rem] py-2 pr-4 pl-2' : 'items-center rounded-full py-1.5 pr-4 pl-1.5',
        selected ? 'bg-primary text-primary-foreground shadow-sm ring-primary' : 'bg-secondary text-secondary-foreground ring-border/70',
        !readOnly && !selected && 'hover:bg-primary/10 hover:ring-primary/45',
        !readOnly && 'active:scale-[0.97] focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none',
        readOnly && 'cursor-default',
        readOnly && !selected && 'opacity-50',
      )}
    >
      <span
        className={cn(
          'flex size-5 shrink-0 items-center justify-center font-mono text-2xs tabular-nums',
          desc && 'mt-px',
          multi ? 'rounded-[6px]' : 'rounded-full',
          selected ? 'bg-primary-foreground/25' : 'bg-background/80 text-muted-foreground',
        )}
      >
        {selected ? <CheckIcon className="size-3.5" strokeWidth={3} /> : index + 1}
      </span>
      <span className="grid min-w-0 gap-0.5">
        <span className={cn('break-words', selected && 'font-medium')}>{option.label}</span>
        {desc && (
          <span className={cn('text-xs break-words', selected ? 'text-primary-foreground/80' : 'text-muted-foreground')}>{option.description}</span>
        )}
      </span>
    </button>
  )
}

function QuestionHead({ q, index, total }: { q: AskQuestion; index: number; total: number }) {
  return (
    <div className="flex items-start gap-2 text-sm font-medium">
      {total > 1 && <span className="mt-px shrink-0 font-mono text-xs text-muted-foreground tabular-nums">{index + 1}.</span>}
      {q.header && <span className="shrink-0 rounded-full bg-primary/12 px-2 py-px text-xs font-medium text-primary">{q.header}</span>}
      <span className="min-w-0 break-words">{q.question}</span>
    </div>
  )
}

// 单选时选项可以带 preview（界面草图、代码片段），选中哪个就在下面显示哪个的
function Preview({ q, picked }: { q: AskQuestion; picked: string[] }) {
  const p = q.options?.find((o) => picked.includes(o.label))?.preview
  if (!p) return null
  return <pre className="max-h-64 overflow-auto rounded-md border bg-muted/40 p-2.5 font-mono text-xs leading-relaxed whitespace-pre">{p}</pre>
}

// ---- 等你回答 ----

export function QuestionForm({
  questions,
  busy,
  onSubmit,
  onSkip,
}: {
  questions: AskQuestion[]
  busy?: boolean
  onSubmit: (answers: Record<string, string>) => void
  onSkip: () => void
}) {
  const [picked, setPicked] = useState<Record<number, string[]>>({})
  const [other, setOther] = useState<Record<number, string>>({})
  const [active, setActive] = useState(0) // 数字键作用在哪一题
  const root = useRef<HTMLDivElement>(null)

  const answered = (qi: number) => (picked[qi]?.length ?? 0) > 0 || !!other[qi]?.trim()
  const done = questions.filter((_, i) => answered(i)).length

  function pick(qi: number, label: string) {
    const multi = questions[qi]?.multiSelect
    setActive(qi)
    setPicked((p) => {
      const cur = p[qi] ?? []
      if (!multi) return { ...p, [qi]: cur[0] === label ? [] : [label] }
      return { ...p, [qi]: cur.includes(label) ? cur.filter((l) => l !== label) : [...cur, label] }
    })
    // 单选：选了选项就不用「其他」了
    if (!multi) setOther((o) => ({ ...o, [qi]: '' }))
    // 单选题选完自动跳到下一题，数字键接着用
    if (!multi && qi + 1 < questions.length) setActive(qi + 1)
  }

  function typeOther(qi: number, v: string) {
    setActive(qi)
    setOther((o) => ({ ...o, [qi]: v }))
    if (!questions[qi]?.multiSelect && v.trim()) setPicked((p) => ({ ...p, [qi]: [] }))
  }

  function submit() {
    if (busy || !done) return
    const answers: Record<string, string> = {}
    questions.forEach((q, i) => {
      const parts = [...(picked[i] ?? [])]
      const o = other[i]?.trim()
      if (o) parts.push(o)
      if (parts.length) answers[q.question] = parts.join(', ')
    })
    onSubmit(answers)
  }

  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) {
      if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
        e.preventDefault()
        submit()
      }
      return
    }
    if (e.metaKey || e.ctrlKey || e.altKey) return
    const n = Number(e.key)
    const opts = questions[active]?.options ?? []
    if (n >= 1 && n <= opts.length) {
      e.preventDefault()
      pick(active, opts[n - 1].label)
    } else if (e.key === 'Enter') {
      e.preventDefault()
      submit()
    }
  }

  // 出现的时候如果没在打字（焦点在空白处，或者输入框是空的），就把焦点拿过来，直接按数字就能选
  useEffect(() => {
    const a = document.activeElement
    const idle = !a || a === document.body || (a instanceof HTMLTextAreaElement && !a.value)
    if (idle) root.current?.focus({ preventScroll: true })
  }, [])

  return (
    <div ref={root} tabIndex={-1} onKeyDown={onKeyDown} className="space-y-4 outline-none">
      {questions.map((q, qi) => (
        <div
          key={qi}
          role={q.multiSelect ? 'group' : 'radiogroup'}
          aria-label={q.question}
          onFocusCapture={() => setActive(qi)}
          // 多个问题时左边一道竖线标出数字键现在作用在哪题
          className={cn(
            'space-y-2',
            questions.length > 1 && 'border-l-2 pl-3',
            questions.length > 1 && (active === qi ? 'border-primary/60' : 'border-transparent'),
          )}
        >
          <QuestionHead q={q} index={qi} total={questions.length} />
          <div className="flex flex-wrap gap-2">
            {q.options?.map((o, oi) => (
              <OptionPebble
                key={o.label}
                index={oi}
                option={o}
                multi={q.multiSelect}
                selected={(picked[qi] ?? []).includes(o.label)}
                onClick={() => pick(qi, o.label)}
              />
            ))}
            <label
              className={cn(
                'flex min-w-60 flex-1 items-center gap-2 rounded-full py-1.5 pr-4 pl-3 text-sm ring-1 transition-colors ring-inset focus-within:ring-primary/60',
                other[qi]?.trim() ? 'bg-primary/10 ring-primary/60' : 'bg-secondary/50 ring-border/70',
              )}
            >
              <PenLineIcon className="size-4 shrink-0 text-muted-foreground" />
              <input
                value={other[qi] ?? ''}
                onChange={(e) => typeOther(qi, e.target.value)}
                placeholder={q.multiSelect ? '其他（可以和上面的一起选）' : '其他：自己写…'}
                className="h-6 min-w-0 flex-1 bg-transparent outline-none placeholder:text-muted-foreground"
              />
            </label>
          </div>
          <Preview q={q} picked={picked[qi] ?? []} />
        </div>
      ))}
      <div className="flex flex-wrap items-center justify-end gap-2 pt-1">
        <span className="mr-auto text-xs text-muted-foreground">
          {questions.length > 1 && `已答 ${done}/${questions.length}`}
          {/* 分隔点跟着键盘提示一起藏，手机上别剩个孤零零的「·」 */}
          <span className="hidden sm:inline">
            {questions.length > 1 && ' · '}按 1–{Math.min(9, Math.max(1, ...questions.map((q) => q.options?.length ?? 0)))} 选择，Enter 提交
          </span>
        </span>
        <Button variant="ghost" size="sm" className="rounded-full" disabled={busy} onClick={onSkip}>
          跳过
        </Button>
        <Button size="sm" className="rounded-full px-4" disabled={busy || !done} onClick={submit}>
          {done < questions.length && done > 0 ? `提交（${questions.length - done} 题没选）` : '提交回答'}
        </Button>
      </div>
    </div>
  )
}

// ---- 会话记录里：当时问了什么、选了什么 ----

// tool_result 的正文像这样（两种措辞都见过）：
//   User has answered your questions: "问题1"="答案1", "问题2"="答案2". You can now continue with the user's answers in mind.
//   Your questions have been answered: "问题"="答案". You can now continue with these answers in mind.
// 问题原文是知道的，按它定位，答案里有引号、逗号也不会切错
export function parseAnswers(text: string, questions: AskQuestion[]): Record<string, string> {
  const found = questions
    .map((q) => {
      const key = `"${q.question}"="`
      const at = text.indexOf(key)
      return { q: q.question, at, start: at + key.length }
    })
    .filter((f) => f.at >= 0)
    .sort((a, b) => a.at - b.at)
  const out: Record<string, string> = {}
  found.forEach((f, k) => {
    const next = found[k + 1]
    let seg = text.slice(f.start, next ? next.at : undefined)
    if (next) seg = seg.replace(/"\s*,\s*$/, '')
    else {
      const end = seg.search(/"\.\s+(You can|用户)/)
      seg = end >= 0 ? seg.slice(0, end) : seg.slice(0, Math.max(0, seg.lastIndexOf('"')))
    }
    out[f.q] = seg
  })
  return out
}

// 一个答案拆回选项：先整串比（label 里本身可能带逗号），再按 ", " 拆；对不上的就是「其他」里写的
function splitAnswer(q: AskQuestion, answer: string) {
  const labels = (q.options ?? []).map((o) => o.label)
  if (labels.includes(answer)) return { chosen: [answer], other: '' }
  const parts = q.multiSelect ? answer.split(', ') : [answer]
  const chosen = parts.filter((p) => labels.includes(p))
  return { chosen, other: parts.filter((p) => !labels.includes(p)).join(', ') }
}

type Outcome = { kind: 'pending' } | { kind: 'skipped' } | { kind: 'answered'; answers: Record<string, string>; raw: string }

export function AskedQuestion({
  block,
  result,
  live,
  tools,
  defaultOpen,
}: {
  block: Block
  result?: Block
  live?: boolean
  tools?: ToolDetails
  defaultOpen: boolean
}) {
  // 精简过的（老服务端 / 分页边界）按 id 取全文
  const [detail, setDetail] = useState<ToolDetail>()
  const needs = (block.lazy || result?.lazy) && !!block.id && !!tools
  useEffect(() => {
    if (!needs) return
    let alive = true
    tools!.load(block.id!).then((d) => alive && d && setDetail(d))
    return () => {
      alive = false
    }
  }, [needs, tools, block.id])

  const questions = questionsOf(detail?.input ?? block.input)
  const res = result && !result.lazy ? { text: result.text ?? '', isError: result.isError } : detail?.result
  const outcome: Outcome = !res
    ? { kind: 'pending' }
    : res.isError
      ? { kind: 'skipped' }
      : { kind: 'answered', answers: parseAnswers(res.text, questions), raw: res.text }
  const [open, setOpen] = useState(defaultOpen)

  const summary = outcome.kind === 'answered' ? Object.values(outcome.answers).filter(Boolean) : []
  // 认不出格式（CLI 改了措辞之类）就把原文摆出来，别显示成「没选」
  const unparsed = outcome.kind === 'answered' && summary.length === 0

  return (
    <Collapsible open={open} onOpenChange={setOpen} className="rounded-xl border border-primary/25 bg-card">
      <CollapsibleTrigger className="flex w-full min-w-0 items-center gap-2 px-3 py-1.5 text-left text-xs">
        <MessageCircleQuestionIcon className="size-3.5 shrink-0 text-primary" />
        <span className="shrink-0 font-medium">{questions.length > 1 ? `问了你 ${questions.length} 个问题` : '问了你'}</span>
        <span className="min-w-0 flex-1 truncate text-muted-foreground">
          {outcome.kind === 'pending'
            ? questions[0]?.question
            : outcome.kind === 'skipped'
              ? '已跳过'
              : summary.length
                ? summary.map((s) => `「${s}」`).join(' ')
                : questions[0]?.question}
        </span>
        {outcome.kind === 'pending' && live && <Loader2Icon className="size-3.5 shrink-0 animate-spin text-muted-foreground" />}
        <ChevronRightIcon className={cn('size-3.5 shrink-0 text-muted-foreground transition-transform', open && 'rotate-90')} />
      </CollapsibleTrigger>
      <CollapsibleContent className="space-y-4 border-t px-3 py-3">
        {questions.length === 0 && needs && !detail && (
          <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <Loader2Icon className="size-3 animate-spin" /> 加载中…
          </p>
        )}
        {questions.map((q, qi) => {
          const ans = outcome.kind === 'answered' ? outcome.answers[q.question] : undefined
          const { chosen, other } = ans !== undefined ? splitAnswer(q, ans) : { chosen: [], other: '' }
          return (
            <div key={qi} className="space-y-2">
              <QuestionHead q={q} index={qi} total={questions.length} />
              <div className="flex flex-wrap gap-2">
                {q.options?.map((o, oi) => (
                  <OptionPebble key={o.label} index={oi} option={o} multi={q.multiSelect} selected={chosen.includes(o.label)} readOnly />
                ))}
                {other && (
                  <div className="inline-flex max-w-full items-start gap-2 rounded-[1.35rem] bg-primary py-1.5 pr-4 pl-3 text-sm text-primary-foreground shadow-sm">
                    <PenLineIcon className="mt-0.5 size-4 shrink-0" />
                    <span className="shrink-0 opacity-80">其他</span>
                    <span className="min-w-0 font-medium break-words">{other}</span>
                  </div>
                )}
              </div>
              <Preview q={q} picked={chosen} />
              {outcome.kind === 'answered' && !unparsed && ans === undefined && <p className="text-xs text-muted-foreground">这题没选</p>}
            </div>
          )
        })}
        {unparsed && <p className="text-xs break-words whitespace-pre-wrap text-muted-foreground">{outcome.raw}</p>}
        {outcome.kind === 'skipped' && <p className="text-xs text-muted-foreground">没回答，跳过了</p>}
        {outcome.kind === 'pending' && <p className="text-xs text-muted-foreground">{live ? '等你在下面的卡片里回答…' : '这次提问没有收到回答'}</p>}
      </CollapsibleContent>
    </Collapsible>
  )
}
