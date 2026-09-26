import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { MessageCircleQuestionIcon, ShieldQuestionIcon, TriangleAlertIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import { Textarea } from '@/components/ui/textarea'
import { QuestionForm, questionsOf } from '@/components/ask-question'
import { ToolInput, toolIcon } from '@/components/tool-view'
import { agentLabel, type Agent } from '@/lib/agents'
import type { PermissionAnswer, PermissionReq } from '@/lib/api'
import { cn } from '@/lib/utils'

// CLI 要用工具、等你批准时弹这张卡：允许 / 总是允许 / 拒绝（claude 的 can_use_tool、codex 的 requestApproval、
// grok 的 request_permission 都翻成同一种请求）。
// 两个特例：AskUserQuestion 是向你提问（答案随 allow 一起回），ExitPlanMode 是批准计划。

// autoFocus：同时有好几张卡时只让第一张抢焦点
type Props = { req: PermissionReq; onAnswer: (a: PermissionAnswer) => boolean; agent?: Agent; autoFocus?: boolean }

export function PermissionCard({ req, onAnswer, agent, autoFocus }: Props) {
  if (req.tool === 'AskUserQuestion') return <QuestionCard req={req} onAnswer={onAnswer} agent={agent} />
  return <ToolPermissionCard req={req} onAnswer={onAnswer} agent={agent} autoFocus={autoFocus} />
}

function ToolPermissionCard({ req, onAnswer, agent = 'claude', autoFocus }: Props) {
  const who = agentLabel[agent]
  const [busy, setBusy] = useState(false)
  const [feedback, setFeedback] = useState('')
  // 拒绝时附一句理由（只有 claude 的协议能把理由带给模型）；null = 没展开
  const [why, setWhy] = useState<string | null>(null)
  const plan = req.tool === 'ExitPlanMode'
  const answer = (a: Omit<PermissionAnswer, 'id'>) => setBusy(onAnswer({ id: req.id, ...a }))
  const deny = () => answer({ choice: 'deny', message: why?.trim() || undefined })

  // 和 TUI 的编号一致：1 允许、2 总是允许（有的话）、最后一个拒绝；计划卡是 1 批准、2 继续完善
  const keys: [string, () => void][] = plan
    ? [
        ['批准并执行', () => answer({ choice: 'allow' })],
        ['继续完善', () => answer({ choice: 'deny', message: feedback.trim() || '用户希望继续完善计划，先别执行。' })],
      ]
    : [['允许', () => answer({ choice: 'allow' })], ...(req.canAlways ? [['总是允许', () => answer({ choice: 'always' })] as [string, () => void]] : []), ['拒绝', deny]]
  const keyOf = (label: string) => keys.findIndex(([l]) => l === label) + 1

  const root = useRef<HTMLDivElement>(null)
  // 出现时如果没在打字就把焦点拿过来，直接按数字选（和提问卡一样）
  useEffect(() => {
    if (!autoFocus) return
    const a = document.activeElement
    if (!a || a === document.body || (a instanceof HTMLTextAreaElement && !a.value)) root.current?.focus({ preventScroll: true })
  }, [autoFocus])
  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (busy || e.metaKey || e.ctrlKey || e.altKey) return
    if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return
    const k = keys[Number(e.key) - 1]
    if (k) {
      e.preventDefault()
      k[1]()
    }
  }

  // 触屏上按钮加高，拇指好按
  const btn = 'pointer-coarse:h-10 pointer-coarse:px-4'
  const kbd = (label: string) => (
    <kbd className="ml-0.5 hidden rounded border border-current/25 px-1 font-mono text-3xs leading-4 opacity-70 pointer-fine:inline">{keyOf(label)}</kbd>
  )

  return (
    <Card ref={root} tabIndex={-1} onKeyDown={onKeyDown} size="sm" className="border-amber-500/40 ring-amber-500/20 outline-none focus-visible:ring-2">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-sm">
          <ShieldQuestionIcon className="size-4 text-amber-600 dark:text-amber-400" />
          {plan ? `${who} 提交了计划，等你批准` : (
            <>
              {who} 想用 <Badge variant="outline" className="gap-1 font-mono">{toolIcon(req.tool)}{req.tool}</Badge>
            </>
          )}
        </CardTitle>
        {/* Bash 的参数视图里已经显示了 description，别重复 */}
        {req.description && !plan && req.description !== req.input.description && <CardDescription>{req.description}</CardDescription>}
      </CardHeader>
      <CardContent className="max-h-[40vh] space-y-2 overflow-y-auto">
        {/* 越出工作目录是这张卡里最要紧的信息，放最上面 */}
        {req.blockedPath && (
          <div role="alert" className="flex items-start gap-2 rounded-md bg-amber-500/12 px-2.5 py-2 text-xs text-amber-800 dark:text-amber-200">
            <TriangleAlertIcon className="mt-px size-3.5 shrink-0" />
            <span>
              涉及工作目录之外的路径：<code className="font-mono break-all">{req.blockedPath}</code>
            </span>
          </div>
        )}
        <ToolInput name={req.tool} input={req.input} />
        {plan && (
          <Textarea
            placeholder={`想让它改哪里？（选「继续完善」时发给 ${who}，可不填）`}
            value={feedback}
            onChange={(e) => setFeedback(e.target.value)}
            className="min-h-16 text-sm"
          />
        )}
        {!plan && why !== null && (
          <Textarea
            autoFocus
            placeholder={`为什么不行、想让 ${who} 换成怎么做？（会随「拒绝」一起发给它）`}
            value={why}
            onChange={(e) => setWhy(e.target.value)}
            className="min-h-16 text-sm"
          />
        )}
      </CardContent>
      <CardFooter className="flex-wrap items-center gap-2">
        {plan ? (
          <>
            <Button variant="outline" size="sm" className={cn('ml-auto', btn)} disabled={busy} onClick={keys[1][1]}>
              继续完善{kbd('继续完善')}
            </Button>
            <Button size="sm" className={btn} disabled={busy} onClick={keys[0][1]}>
              批准并执行{kbd('批准并执行')}
            </Button>
          </>
        ) : (
          <>
            {/* 「总是允许」会写一条规则，说清楚是哪条；放在左边，和「允许」隔开，免得手滑 */}
            {req.canAlways && (
              <Button
                variant="ghost"
                size="sm"
                className={cn('max-w-full min-w-0 text-muted-foreground', btn)}
                disabled={busy}
                onClick={() => answer({ choice: 'always' })}
                title={req.always ? `以后自动允许 ${req.always.label}${req.always.where ? `（记在${req.always.where}）` : ''}` : undefined}
              >
                <span className="truncate">
                  总是允许{req.always && <span className="font-mono"> {req.always.label}</span>}
                </span>
                {kbd('总是允许')}
              </Button>
            )}
            <div className="ml-auto flex items-center gap-2">
              {agent === 'claude' && why === null && (
                <Button variant="link" size="sm" className={cn('px-1 text-muted-foreground', btn)} disabled={busy} onClick={() => setWhy('')}>
                  说明理由
                </Button>
              )}
              <Button variant="outline" size="sm" className={btn} disabled={busy} onClick={deny}>
                {why !== null ? '拒绝并发送' : '拒绝'}
                {kbd('拒绝')}
              </Button>
              <Button size="sm" className={btn} disabled={busy} onClick={() => answer({ choice: 'allow' })}>
                允许{kbd('允许')}
              </Button>
            </div>
          </>
        )}
      </CardFooter>
    </Card>
  )
}

// 选项按钮和键盘操作在 ask-question.tsx，会话记录里显示历史选择也用它
function QuestionCard({ req, onAnswer, agent = 'claude' }: Props) {
  const questions = questionsOf(req.input)
  const [busy, setBusy] = useState(false)
  return (
    <Card size="sm" className="border-primary/30 ring-primary/15">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-sm">
          <MessageCircleQuestionIcon className="size-4 text-primary" />
          {agentLabel[agent]} 有问题想问你
        </CardTitle>
      </CardHeader>
      <CardContent>
        <QuestionForm
          questions={questions}
          busy={busy}
          onSubmit={(answers) => setBusy(onAnswer({ id: req.id, choice: 'allow', answers }))}
          onSkip={() => setBusy(onAnswer({ id: req.id, choice: 'deny', message: 'The user declined to answer.' }))}
        />
      </CardContent>
    </Card>
  )
}
