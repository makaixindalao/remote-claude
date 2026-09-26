import { useEffect, useId, useState, type CSSProperties, type ReactNode } from 'react'
import { MinusIcon, MonitorIcon, MoonIcon, PlusIcon, SunIcon } from 'lucide-react'
import { useTheme } from 'next-themes'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Markdown } from '@/components/markdown'
import { PageHeader } from '@/components/page-header'
import { SettingsSection as Section } from '@/components/settings-section'
import {
  canListLocalFonts,
  CUSTOM,
  DEFAULTS,
  FONTS,
  fontAvailable,
  fontStack,
  listLocalFonts,
  setAppearance,
  TERM_SIZE,
  termFontSize,
  TEXT_SIZE,
  useAppearance,
  type FontKind,
} from '@/lib/appearance'
import { isTouch } from '@/lib/format'
import { href, type SettingsTab } from '@/lib/route'
import { cn } from '@/lib/utils'
import { ChatSettings } from '@/views/settings-chat'
import { NotifySettings } from '@/views/settings-notify'
import { RemoteControlSettings } from '@/views/settings-rc'
import { ReclaudeSettings } from '@/views/settings-reclaude'

// 设置页分几页：外观（主题、字体、字号，只存在这个浏览器里，改了当场生效），
// 对话（新对话的默认模型 / effort / 权限）、通知、reclaude 和 Remote Control（VPS 上的，所有浏览器共用）

const SAMPLE = `字号和字体改了马上生效，**整个页面**都跟着变。The quick brown fox jumps over the lazy dog.

\`\`\`ts
// 0O 1lI {} [] => !== 代码里的中文注释
const total = items.reduce((sum, x) => sum + x.price, 0)
\`\`\``

const TABS: [SettingsTab, string, string][] = [
  ['appearance', '外观', '只对这个浏览器生效'],
  ['chat', '对话', 'VPS 上的设置，所有浏览器共用'],
  ['notify', '通知', '推送到哪、通知哪些存在 VPS 上；这个浏览器收不收只管这个浏览器'],
  ['reclaude', 'reclaude', 'VPS 上的设置，所有浏览器共用'],
  ['rc', 'Remote Control', 'VPS 上的设置，所有浏览器共用'],
]

export function SettingsView({ tab }: { tab: SettingsTab }) {
  return (
    <>
      <PageHeader title="设置" subtitle={TABS.find(([t]) => t === tab)?.[2]} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-3xl space-y-8 p-4 md:p-6">
          <nav className="inline-flex rounded-lg border p-0.5">
            {TABS.map(([t, label]) => (
              <Button
                key={t}
                asChild
                size="sm"
                variant="ghost"
                aria-current={tab === t ? 'page' : undefined}
                className={cn('aria-[current=page]:bg-muted aria-[current=page]:text-foreground', tab !== t && 'text-muted-foreground')}
              >
                <a href={href.settings(t)}>{label}</a>
              </Button>
            ))}
          </nav>
          {tab === 'chat' ? (
            <ChatSettings />
          ) : tab === 'notify' ? (
            <NotifySettings />
          ) : tab === 'reclaude' ? (
            <ReclaudeSettings />
          ) : tab === 'rc' ? (
            <RemoteControlSettings />
          ) : (
            <AppearanceSettings />
          )}
        </div>
      </div>
    </>
  )
}

function AppearanceSettings() {
  // 预设字体的名字用它自己的字体显示：编进来的几款先都挂上（只挂 @font-face，用到的字形才下载）
  useEffect(() => {
    for (const f of [...FONTS.sans, ...FONTS.mono]) void f.load?.()
  }, [])

  return (
    <>
      <Section title="主题">
        <ThemePicker />
      </Section>
      <Section title="界面字体" desc="菜单、对话正文">
        <FontPicker kind="sans" />
      </Section>
      <Section title="代码字体" desc="代码块、工具输出、终端">
        <FontPicker kind="mono" />
      </Section>
      <Section title="字号">
        <SizeSettings />
      </Section>
      <Section title="预览">
        <Preview />
      </Section>
      <Button variant="outline" onClick={() => setAppearance(DEFAULTS)}>
        字体和字号恢复默认
      </Button>
    </>
  )
}

function ThemePicker() {
  const { theme, setTheme } = useTheme()
  const options = [
    ['system', '跟随系统', MonitorIcon],
    ['light', '浅色', SunIcon],
    ['dark', '深色', MoonIcon],
  ] as const
  return (
    <div className="inline-flex rounded-lg border p-0.5">
      {options.map(([value, label, Icon]) => (
        <Button
          key={value}
          size="sm"
          variant="ghost"
          aria-pressed={theme === value}
          className={cn('aria-pressed:bg-muted aria-pressed:text-foreground', theme !== value && 'text-muted-foreground')}
          onClick={() => setTheme(value)}
        >
          <Icon /> {label}
        </Button>
      ))}
    </div>
  )
}

// 一块能点的选项：单选圈 + 名字 + 说明
function Choice({ value, title, hint, style }: { value: string; title: ReactNode; hint: string; style?: CSSProperties }) {
  return (
    <label className="flex cursor-pointer items-center gap-3 rounded-lg border px-3 py-2.5 transition-colors hover:bg-muted/50 has-data-[state=checked]:border-primary has-data-[state=checked]:bg-primary/5">
      <RadioGroupItem value={value} />
      <div className="min-w-0">
        <div className="truncate text-sm" style={style}>
          {title}
        </div>
        <div className="truncate text-xs text-muted-foreground">{hint}</div>
      </div>
    </label>
  )
}

function FontPicker({ kind }: { kind: FontKind }) {
  const look = useAppearance()
  return (
    <div className="space-y-3">
      <RadioGroup value={look[kind]} onValueChange={(v) => setAppearance({ [kind]: v })} className="grid-cols-1 sm:grid-cols-2">
        {FONTS[kind].map((f) => (
          <Choice key={f.id} value={f.id} title={f.label} hint={f.hint} style={{ fontFamily: fontStack(kind, { ...look, [kind]: f.id }) }} />
        ))}
        <Choice value={CUSTOM} title="自定义" hint="用本机装的字体" />
      </RadioGroup>
      {look[kind] === CUSTOM && <CustomFont kind={kind} />}
    </div>
  )
}

function CustomFont({ kind }: { kind: FontKind }) {
  const field = kind === 'sans' ? 'sansCustom' : 'monoCustom'
  const value = useAppearance()[field]
  const [local, setLocal] = useState<string[]>([])
  const listId = useId()
  const missing = value.trim() !== '' && !fontAvailable(value)

  async function loadLocal() {
    try {
      const fonts = await listLocalFonts()
      setLocal(fonts)
      toast.success(`找到 ${fonts.length} 款本机字体，在输入框里挑`)
    } catch (e) {
      toast.error(`读不到本机字体：${(e as Error).message}`)
    }
  }

  return (
    <div className="space-y-2 rounded-lg border bg-muted/30 p-3">
      <div className="flex gap-2">
        <Input
          value={value}
          list={listId}
          placeholder={kind === 'sans' ? '比如 LXGW WenKai, Sarasa UI SC' : '比如 Sarasa Mono SC, Maple Mono'}
          onChange={(e) => setAppearance({ [field]: e.target.value })}
          className="bg-background"
          style={{ fontFamily: fontStack(kind) }}
        />
        {canListLocalFonts() && (
          <Button variant="outline" className="shrink-0" onClick={loadLocal}>
            列出本机字体
          </Button>
        )}
        <datalist id={listId}>
          {local.map((f) => (
            <option key={f} value={f} />
          ))}
        </datalist>
      </div>
      <p className="text-xs text-muted-foreground">填字体名，逗号隔开可以写好几个，前面的没有就用后面的；都没有就用系统默认。</p>
      {missing && (
        <p className="text-xs text-destructive">这个浏览器里找不到「{value.trim()}」。字体名要和本机装的一致；Safari 不让网页用自己装的字体，换 Chrome / Edge 试试。</p>
      )}
    </div>
  )
}

function SizeSettings() {
  const look = useAppearance()
  const touch = isTouch()
  const termSize = termFontSize(look, touch)
  return (
    <div className="divide-y rounded-lg border">
      <SizeRow
        label="正文字号"
        hint="界面上其余的字按同样比例放大缩小"
        value={look.textSize}
        min={TEXT_SIZE.min}
        max={TEXT_SIZE.max}
        isDefault={look.textSize === DEFAULTS.textSize}
        onChange={(n) => setAppearance({ textSize: n })}
        onReset={() => setAppearance({ textSize: DEFAULTS.textSize })}
      />
      <SizeRow
        label="终端字号"
        hint={`网页终端和对话页右侧的 shell · 默认 ${touch ? 12 : 13}px`}
        value={termSize}
        min={TERM_SIZE.min}
        max={TERM_SIZE.max}
        isDefault={look.termSize === 0}
        onChange={(n) => setAppearance({ termSize: n })}
        onReset={() => setAppearance({ termSize: 0 })}
      />
    </div>
  )
}

function SizeRow(props: {
  label: string
  hint: string
  value: number
  min: number
  max: number
  isDefault: boolean
  onChange: (n: number) => void
  onReset: () => void
}) {
  const { label, hint, value, min, max, isDefault, onChange, onReset } = props
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 px-3 py-2.5">
      <div className="min-w-0 flex-1">
        <div className="text-sm">{label}</div>
        <div className="text-xs text-muted-foreground">{hint}</div>
      </div>
      <div className="flex items-center gap-1">
        {!isDefault && (
          <Button size="sm" variant="ghost" className="text-muted-foreground" onClick={onReset}>
            默认
          </Button>
        )}
        <Button size="icon-sm" variant="outline" disabled={value <= min} onClick={() => onChange(value - 1)} aria-label="小一号">
          <MinusIcon />
        </Button>
        <span className="w-12 text-center text-sm tabular-nums">{value}px</span>
        <Button size="icon-sm" variant="outline" disabled={value >= max} onClick={() => onChange(value + 1)} aria-label="大一号">
          <PlusIcon />
        </Button>
      </div>
    </div>
  )
}

// 照对话页的样子摆一段：用户气泡、回复正文、代码块，再加一行终端
function Preview() {
  const look = useAppearance()
  return (
    <div className="space-y-3 rounded-xl border bg-background p-4">
      <div className="flex justify-end">
        <div className="max-w-[85%] rounded-2xl rounded-br-md bg-muted px-3.5 py-2 text-sm">帮我把字体换成 JetBrains Mono，字号调大一点</div>
      </div>
      <Markdown text={SAMPLE} className="text-sm leading-relaxed" />
      <pre
        className="overflow-x-auto rounded-lg bg-[#1f1e1d] px-3 py-2 text-[#ecebe6]"
        style={{ fontFamily: fontStack('mono', look), fontSize: termFontSize(look, isTouch()) }}
      >
        <span className="text-[#7ecf8a]">~/remote-claude</span> $ claude{'\n'}
        <span className="text-[#d97757]">✻</span> Welcome to Claude Code!
      </pre>
    </div>
  )
}
