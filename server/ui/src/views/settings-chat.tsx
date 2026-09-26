import type { ReactNode } from 'react'
import { TriangleAlertIcon } from 'lucide-react'
import { toast } from 'sonner'
import { Select, SelectContent, SelectItem, SelectSeparator, SelectTrigger } from '@/components/ui/select'
import { effectiveEffort, modeText, optionHint } from '@/components/composer'
import { SettingsSection as Section } from '@/components/settings-section'
import { useCatalog } from '@/hooks/use-catalog'
import { useData } from '@/hooks/use-data'
import { agentLabel, agentModes, dangerMode, modeHint, type Agent } from '@/lib/agents'
import type { Catalog, ChatChoice, ChatPrefs } from '@/lib/api'
import { optionName } from '@/lib/models'
import { EFFORT_BY_MODEL, newChatSettings, saveDefaults, useChatPrefs, type Settings } from '@/lib/prefs'
import { cn } from '@/lib/utils'
import { Field } from '@/views/settings-rc'

// 设置页的「对话」一页：新对话一开始用什么模型 / effort / 权限模式，按 CLI 各设各的。
// 每一项都可以选「和上次一致」—— 跟着上次在输入框里选的走。都存在 VPS 上（见 ../../prefs.go），手机和电脑共用。

const LAST = '__last__' // Select 的选项值不能是空串，「和上次一致」用它代替
const CLAUDE_EFFORTS = ['low', 'medium', 'high', 'xhigh', 'max']

export function ChatSettings() {
  const { agents } = useData()
  const prefs = useChatPrefs()
  return (
    <>
      <p className="text-sm text-muted-foreground">
        新对话一开始用什么。每一项都可以选「和上次一致」：跟着上次在输入框里选的走。开始以后在输入框下面照样能换。
      </p>
      {agents.map((a) => (
        <AgentDefaults key={a} agent={a} prefs={prefs?.[a]} loaded={!!prefs} />
      ))}
    </>
  )
}

function AgentDefaults({ agent, prefs, loaded }: { agent: Agent; prefs?: ChatPrefs; loaded: boolean }) {
  const catalog = useCatalog('', agent)
  const models = catalog?.models ?? []
  const d = prefs?.defaults ?? {}
  const last = newChatSettings(agent, { defaults: {}, last: prefs?.last }) // 只看上次的
  const next = newChatSettings(agent, prefs) // 新对话实际会用的
  const levels = [...new Set(models.flatMap((m) => m.supportedEffortLevels ?? []))]
  if (!levels.length && agent === 'claude' && !catalog) levels.push(...CLAUDE_EFFORTS)

  async function save(patch: ChatChoice) {
    try {
      await saveDefaults(agent, { ...d, ...patch })
    } catch (e) {
      toast.error(`没存上：${(e as Error).message}`)
    }
  }

  // 「跟着模型」具体是哪一档：按新对话会用的模型算
  const def = effortOf(catalog, { ...next, effort: '' })
  const byModelNow = `跟着模型${def ? `（${def}）` : ''}`
  const byModel = models
    .filter((m) => m.value !== 'default' && m.defaultEffort !== undefined)
    .map((m) => `${optionName(m)} ${m.defaultEffort || '不支持'}`)
    .join(' · ')

  return (
    <Section title={agentLabel[agent]} desc={<>新对话会用：{summary(agent, catalog, next)}</>}>
      <div className="divide-y rounded-lg border">
        <Field label="模型">
          <Select disabled={!loaded} value={d.model || LAST} onValueChange={(v) => save({ model: v === LAST ? '' : v })}>
            <SelectTrigger className="w-full">
              <span className="truncate">{d.model ? modelLabel(catalog, d.model) : lastLabel(modelLabel(catalog, last.model))}</span>
            </SelectTrigger>
            <SelectContent>
              <Option value={LAST} title="和上次一致" hint={`上次：${modelLabel(catalog, last.model)}`} />
              <SelectSeparator />
              {models.map((m) => (
                <Option key={m.value} value={m.value} title={optionName(m)} hint={optionHint(agent, m)} />
              ))}
              {!catalog && <div className="px-2 py-1.5 text-xs text-muted-foreground">正在问 {agentLabel[agent]} 有哪些模型…</div>}
              {d.model && catalog && !models.some((m) => m.value === d.model) && <Option value={d.model} title={d.model} hint="这台机器上的 CLI 没报这个模型" />}
            </SelectContent>
          </Select>
        </Field>
        {levels.length > 0 && (
          <Field label="思考" hint="effort">
            <Select disabled={!loaded} value={d.effort || LAST} onValueChange={(v) => save({ effort: v === LAST ? '' : v })}>
              <SelectTrigger className="w-full">
                <span className="truncate">{d.effort === EFFORT_BY_MODEL ? byModelNow : d.effort || lastLabel(last.effort || byModelNow)}</span>
              </SelectTrigger>
              <SelectContent>
                <Option value={LAST} title="和上次一致" hint={`上次：${last.effort || '跟着模型'}`} />
                <Option value={EFFORT_BY_MODEL} title="跟着模型" hint={byModel || '不指定，各模型用自己的那一档'} />
                <SelectSeparator />
                {levels.map((e) => (
                  <Option key={e} value={e} title={e} />
                ))}
              </SelectContent>
            </Select>
          </Field>
        )}
        <Field label="权限模式">
          <Select disabled={!loaded} value={d.mode || LAST} onValueChange={(v) => save({ mode: v === LAST ? '' : v })}>
            <SelectTrigger className={cn('w-full', d.mode === dangerMode[agent] && 'text-destructive')}>
              <span className="truncate">{d.mode ? modeText(agent, d.mode) : lastLabel(modeText(agent, last.mode))}</span>
            </SelectTrigger>
            <SelectContent>
              <Option value={LAST} title="和上次一致" hint={`上次：${modeText(agent, last.mode)}`} />
              <SelectSeparator />
              {agentModes[agent].map((m) => (
                <Option key={m} value={m} title={<span className="font-mono">{m}</span>} hint={modeHint[agent][m]} danger={m === dangerMode[agent]} />
              ))}
            </SelectContent>
          </Select>
        </Field>
      </div>
    </Section>
  )
}

function Option({ value, title, hint, danger }: { value: string; title: ReactNode; hint?: string; danger?: boolean }) {
  return (
    <SelectItem value={value} className={cn(danger && 'text-destructive focus:text-destructive')}>
      <span className="grid">
        <span className="flex items-center gap-1">
          {danger && <TriangleAlertIcon className="size-3 text-destructive" />}
          {title}
        </span>
        {hint && <span className={cn('text-xs', danger ? 'text-destructive/80' : 'text-muted-foreground')}>{hint}</span>}
      </span>
    </SelectItem>
  )
}

const lastLabel = (v: string) => `和上次一致 · ${v}`

// 模型选项的值 → 具体的模型名（default 也写成具体是哪个）；目录还没到先写原样
function modelLabel(catalog: Catalog | undefined, value: string) {
  const m = catalog?.models?.find((x) => x.value === value)
  return m ? optionName(m) : catalog || value !== 'default' ? value : '…'
}

// 这套设置实际是哪一档 effort：选了就是选的（模型不支持就没有），没选是模型自己的那一档
function effortOf(catalog: Catalog | undefined, s: Settings) {
  const m = catalog?.models?.find((x) => x.value === s.model)
  if (catalog?.models && !m?.supportedEffortLevels?.length) return ''
  return effectiveEffort(catalog, s) || m?.defaultEffort || ''
}

function summary(agent: Agent, catalog: Catalog | undefined, s: Settings) {
  return [modelLabel(catalog, s.model), effortOf(catalog, s), modeText(agent, s.mode)].filter(Boolean).join(' · ')
}
