import { useCallback, useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { Loader2Icon, RefreshCwIcon, RotateCwIcon, SquareTerminalIcon, TriangleAlertIcon } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { SettingsSection as Section } from '@/components/settings-section'
import { useData } from '@/hooks/use-data'
import { modeHint } from '@/lib/agents'
import { api, type RCStatus, type RemoteControl } from '@/lib/api'
import { href } from '@/lib/route'
import { cn } from '@/lib/utils'

// 设置页的 Remote Control 一页：开关（在这台机器上常驻 `claude remote-control`）、它的参数、守护脚本的行为，
// 再加上它在 tmux 里最后说了什么（会话链接就在里面）。都存在 VPS 上（见 ../../remote_control.go）

const MODES = ['default', 'acceptEdits', 'plan', 'auto', 'dontAsk', 'bypassPermissions']
const ROOT = '__root__' // Select 的选项值不能是空串，项目根目录用它代替

const SPAWNS: [string, string][] = [
  ['same-dir', '都在选的这个目录里'],
  ['worktree', '每个新会话一个独立的 git worktree'],
  ['session', ''],
]

// 表单和存着的设置一样吗（开关单独存，不算）
const same = (form: RemoteControl, saved: RemoteControl) => JSON.stringify({ ...form, enabled: saved.enabled }) === JSON.stringify(saved)

const stateText: Record<RCStatus['state'], string> = {
  stopped: '没在跑',
  running: '运行中',
  restarting: '退出了，守护脚本等着重启',
  exited: '退出了',
}

export function RemoteControlSettings() {
  const [st, setSt] = useState<RCStatus | null>(null)
  const [form, setForm] = useState<RemoteControl | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('') // save / toggle / restart
  const saved = useRef<RemoteControl | null>(null)

  // 没动过的表单跟着服务端走（别的浏览器改了设置这边也能看到），改了一半的留着
  const apply = useCallback((s: RCStatus) => {
    const prev = saved.current // setForm 的回调晚些才跑，先把旧的记下来
    saved.current = s.config
    setSt(s)
    setForm((f) => (!f || !prev || same(f, prev) ? s.config : f))
  }, [])

  const load = useCallback(() => {
    api<RCStatus>('/api/rc')
      .then((s) => {
        apply(s)
        setError('')
      })
      .catch((e) => setError(e.message))
  }, [apply])

  useEffect(load, [load])
  // 跑着的时候隔几秒刷一下：刚起来时会话链接要过一会儿才打出来，退出、重启也能及时看到
  const live = st?.state === 'running' || st?.state === 'restarting'
  useEffect(() => {
    if (!live) return
    const t = setInterval(() => document.visibilityState === 'visible' && load(), 4000)
    return () => clearInterval(t)
  }, [live, load])

  async function save(next: RemoteControl, what: string) {
    setBusy(what)
    try {
      const s = await api<RCStatus>('/api/rc', { method: 'POST', body: next })
      saved.current = null // 存下去的就是表单，直接换成服务端补过默认值的那份
      apply(s)
      toast.success(!next.enabled ? 'Remote Control 已关掉' : what === 'toggle' ? 'Remote Control 起来了' : '已保存，按新设置重启了')
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  async function restart() {
    setBusy('restart')
    try {
      apply(await api<RCStatus>('/api/rc/restart', { method: 'POST' }))
      toast.success('重启了')
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  if (!st || !form) {
    return error ? <Problem>读不到 Remote Control 的状态：{error}</Problem> : <Skeleton className="h-40 w-full" />
  }

  const config = st.config
  const dirty = !same(form, config)
  const set = (patch: Partial<RemoteControl>) => setForm({ ...form, ...patch })

  return (
    <>
      <EnableCard st={st} busy={busy} onToggle={(enabled) => save({ ...form, enabled }, 'toggle')} onRestart={restart} />

      <Section title="claude rc 的参数" desc={<code>claude remote-control [选项]</code>}>
        <div className="divide-y rounded-lg border">
          <Field label="目录" hint="在这里跑，开出来的会话也在这里">
            <ProjectSelect value={form.project} onChange={(project) => set({ project })} />
          </Field>
          <Field label="名字" hint="--name，claude.ai/code 里看到的名字">
            <Input value={form.name} maxLength={64} placeholder="不填用它自己起的（主机名）" onChange={(e) => set({ name: e.target.value })} />
          </Field>
          <Field label="权限模式" hint="--permission-mode，它开出来的会话用">
            <Select value={form.permissionMode} onValueChange={(permissionMode) => set({ permissionMode })}>
              {/* 下拉项里带了说明，触发器上只显示名字 */}
              <SelectTrigger className={cn('w-full font-mono', form.permissionMode === 'bypassPermissions' && 'text-destructive')}>
                {form.permissionMode}
              </SelectTrigger>
              <SelectContent>
                {MODES.map((m) => (
                  <SelectItem key={m} value={m}>
                    <span className="grid">
                      <span className={cn('font-mono', m === 'bypassPermissions' && 'text-destructive')}>{m}</span>
                      {modeHint.claude[m] && <span className="text-xs text-muted-foreground">{modeHint.claude[m]}</span>}
                    </span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field label="会话放哪" hint="--spawn">
            <Select value={form.spawn} onValueChange={(spawn) => set({ spawn })}>
              <SelectTrigger className="w-full font-mono">{form.spawn}</SelectTrigger>
              <SelectContent>
                {SPAWNS.map(([v, hint]) => (
                  <SelectItem key={v} value={v}>
                    <span className="grid">
                      <span className="font-mono">{v}</span>
                      {hint && <span className="text-xs text-muted-foreground">{hint}</span>}
                    </span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field label="最多同时" hint="--capacity，同时开着的会话数">
            <Input
              type="number"
              min={0}
              max={256}
              value={form.capacity || ''}
              placeholder="默认 32"
              onChange={(e) => set({ capacity: Math.max(0, Math.min(256, Number(e.target.value) || 0)) })}
            />
          </Field>
        </div>
      </Section>

      <Section title="守护脚本" desc={<code className="break-all">{st.script}</code>}>
        <div className="divide-y rounded-lg border">
          <SwitchRow
            label="退出后自动重启"
            hint={form.autoRestart ? `claude rc 断线、出错退出后，隔 ${form.restartDelay} 秒再起` : '退出了就停着，下面留着它最后的输出'}
            checked={form.autoRestart}
            onChange={(autoRestart) => set({ autoRestart })}
          />
          {form.autoRestart && (
            <Field label="隔几秒" hint="1–3600">
              <Input
                type="number"
                min={1}
                max={3600}
                value={form.restartDelay}
                onChange={(e) => set({ restartDelay: Math.max(1, Math.min(3600, Number(e.target.value) || 1)) })}
              />
            </Field>
          )}
        </div>
        <p className="text-xs text-muted-foreground">
          开着时 rcweb 启动会检查一遍，没在跑就拉起来（VPS 重启后也能自己回来）。起的程序：<code className="break-all">{st.bin}</code>
          （设置里打开 reclaude 后是 reclaude；换了要重启才生效）。
        </p>
      </Section>

      <div className="flex flex-wrap items-center gap-2">
        <Button disabled={!dirty || !!busy} onClick={() => save(form, 'save')}>
          {busy === 'save' && <Loader2Icon className="animate-spin" />} {config.enabled ? '保存并重启' : '保存'}
        </Button>
        {dirty && (
          <Button variant="ghost" className="text-muted-foreground" disabled={!!busy} onClick={() => setForm(config)}>
            撤销改动
          </Button>
        )}
        {dirty && config.enabled && <span className="text-xs text-muted-foreground">重启会断开现在连着的远程会话</span>}
      </div>

      {st.state !== 'stopped' && (
        <Section
          title="输出"
          desc={`tmux 会话 ${st.session} 最后几十行`}
          action={
            <Button size="sm" variant="ghost" className="text-muted-foreground" onClick={() => load()}>
              <RefreshCwIcon /> 刷新
            </Button>
          }
        >
          <Output text={st.output} />
        </Section>
      )}
    </>
  )
}

function Problem({ children }: { children: ReactNode }) {
  return (
    <div className="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2.5 text-xs text-destructive">
      <TriangleAlertIcon className="mt-px size-3.5 shrink-0" />
      <div className="min-w-0 break-words whitespace-pre-wrap">{children}</div>
    </div>
  )
}

function EnableCard({ st, busy, onToggle, onRestart }: { st: RCStatus; busy: string; onToggle: (on: boolean) => void; onRestart: () => void }) {
  const id = useId()
  const dot =
    st.state === 'running' ? 'bg-emerald-500' : st.state === 'restarting' ? 'animate-pulse bg-amber-500' : st.state === 'exited' ? 'bg-destructive' : 'border border-muted-foreground/50'
  return (
    <div className="space-y-3 rounded-lg border px-3 py-3">
      <div className="flex items-start gap-4">
        <div className="min-w-0 flex-1 space-y-1">
          <label htmlFor={id} className="cursor-pointer text-sm font-medium">
            Remote Control（claude rc）
          </label>
          <p id={`${id}-desc`} className="text-xs text-muted-foreground">
            在这台机器上常驻 <code>claude remote-control</code>，手机上的 Claude App、claude.ai/code 就能连过来开会话、在这里干活。
          </p>
        </div>
        <Switch
          id={id}
          aria-describedby={`${id}-desc`}
          className="mt-0.5"
          checked={st.config.enabled}
          disabled={!!busy}
          onCheckedChange={onToggle}
        />
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 text-sm">
        <span className="flex items-center gap-2">
          <span className={cn('inline-block size-2 shrink-0 rounded-full', dot)} />
          {stateText[st.state]}
          {st.exitCode !== undefined && <span className="text-xs text-muted-foreground">退出码 {st.exitCode}</span>}
        </span>
        <span className="ml-auto flex gap-1.5">
          {st.state !== 'stopped' && (
            <Button size="sm" variant="outline" asChild>
              <a href={href.term(st.session, { mode: 'attach' })}>
                <SquareTerminalIcon /> 终端里看
              </a>
            </Button>
          )}
          {st.config.enabled && (
            <Button size="sm" variant="outline" disabled={!!busy} onClick={onRestart}>
              {busy === 'restart' ? <Loader2Icon className="animate-spin" /> : <RotateCwIcon />} 重启
            </Button>
          )}
        </span>
      </div>
      {st.state !== 'stopped' && (
        <p className="text-xs text-muted-foreground">第一次跑、换了目录时它可能在终端里问你问题（开不开 Remote Control、信不信任这个目录），输出停住了就到终端里答一下。</p>
      )}
    </div>
  )
}

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-2 px-3 py-2.5 sm:flex-row sm:items-center sm:gap-4">
      <div className="min-w-0 sm:w-40 sm:shrink-0">
        <div className="text-sm">{label}</div>
        {hint && <div className="text-xs text-muted-foreground">{hint}</div>}
      </div>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  )
}

function SwitchRow({ label, hint, checked, onChange }: { label: string; hint: string; checked: boolean; onChange: (v: boolean) => void }) {
  const id = useId()
  return (
    <div className="flex items-center gap-4 px-3 py-2.5">
      <div className="min-w-0 flex-1">
        <label htmlFor={id} className="cursor-pointer text-sm">
          {label}
        </label>
        <div className="text-xs text-muted-foreground">{hint}</div>
      </div>
      <Switch id={id} checked={checked} onCheckedChange={onChange} />
    </div>
  )
}

function ProjectSelect({ value, onChange }: { value: string; onChange: (project: string) => void }) {
  const { projects } = useData()
  const known = projects.some((p) => p.name === value)
  return (
    <Select value={value || ROOT} onValueChange={(v) => onChange(v === ROOT ? '' : v)}>
      <SelectTrigger className="w-full">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={ROOT}>项目根目录</SelectItem>
        {value && !known && <SelectItem value={value}>{value}</SelectItem>}
        {projects
          .filter((p) => p.exists)
          .map((p) => (
            <SelectItem key={p.name} value={p.name}>
              {p.name}
            </SelectItem>
          ))}
      </SelectContent>
    </Select>
  )
}

// 输出里的链接（Remote Control 打出来的会话地址）做成能点的
function Output({ text }: { text: string }) {
  if (!text.trim()) return <p className="text-xs text-muted-foreground">还没有输出。</p>
  const parts = text.split(/(https?:\/\/[^\s'"<>]+)/g)
  return (
    <pre className="max-h-96 overflow-auto rounded-lg bg-[#1f1e1d] px-3 py-2 font-mono text-2xs leading-relaxed whitespace-pre-wrap text-[#ecebe6]">
      {parts.map((p, i) =>
        i % 2 ? (
          <a key={i} href={p} target="_blank" rel="noreferrer" className="text-[#d97757] underline underline-offset-2">
            {p}
          </a>
        ) : (
          p
        ),
      )}
    </pre>
  )
}
