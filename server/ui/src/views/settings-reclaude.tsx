import { useCallback, useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { ChevronRightIcon, GaugeIcon, Loader2Icon, RefreshCwIcon, RotateCcwIcon, TriangleAlertIcon } from 'lucide-react'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { SettingsSection as Section } from '@/components/settings-section'
import { api, type GatewayResult, type KV, type ReclaudeInfo, type ReclaudeOrg } from '@/lib/api'
import { href } from '@/lib/route'
import { cn } from '@/lib/utils'

// 设置页的 reclaude 一页：开关（起 claude 的地方换成 reclaude）、状态、组织（reclaude org）、网关（reclaude config gateway）。
// 都是 VPS 上的状态，后端每次现问 reclaude（见 ../../reclaude.go）。几块分开取：
// 网关那条要先同步配置，慢的时候四五十秒，不能让整页等它

const get = (list: KV[] | undefined, key: string) => list?.find((x) => x.key === key)?.value

// 还没登录 reclaude 时，引导去网页终端里 `reclaude login`（设备码登录，要开浏览器授权）
const loginTerm = href.term('sh-reclaude', { mode: 'shell' })

type Load<T> = { data: T | null; error: string; loading: boolean; reload: () => void; set: (data: T) => void }

// path 为 null 时不取。连着 reload 时只认最后一次的结果
function useLoad<T>(path: string | null): Load<T> {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const seq = useRef(0)
  const reload = useCallback(() => {
    if (!path) return
    const n = ++seq.current
    setLoading(true)
    setError('')
    api<T>(path)
      .then((d) => n === seq.current && setData(d))
      .catch((e) => n === seq.current && setError(e.message))
      .finally(() => n === seq.current && setLoading(false))
  }, [path])
  useEffect(reload, [reload])
  return { data, error, loading, reload, set: setData }
}

export function ReclaudeSettings() {
  const base = useLoad<ReclaudeInfo>('/api/reclaude')
  const info = base.data
  const installed = !!info?.installed // 装了才去问下面几块
  const status = useLoad<{ status: KV[] }>(installed ? '/api/reclaude/status' : null)
  const gateway = useLoad<{ gateway: KV[] }>(installed ? '/api/reclaude/gateway' : null)
  const orgs = useLoad<{ orgs: ReclaudeOrg[] }>(installed ? '/api/reclaude/orgs' : null)

  if (!info) {
    return base.error ? (
      <Problem>
        读不到 reclaude 的状态：{base.error}
        <Button size="xs" variant="outline" className="ml-2" onClick={base.reload}>
          重试
        </Button>
      </Problem>
    ) : (
      <Skeleton className="h-24 w-full" />
    )
  }

  const busy = status.loading || gateway.loading || orgs.loading
  const refresh = (
    <Button
      size="sm"
      variant="ghost"
      className="text-muted-foreground"
      disabled={busy}
      onClick={() => {
        status.reload()
        gateway.reload()
        orgs.reload()
      }}
    >
      <RefreshCwIcon className={cn(busy && 'animate-spin')} /> 刷新
    </Button>
  )

  return (
    <>
      <EnableSwitch info={info} onChange={(enabled) => base.set({ ...info, enabled })} />
      {info.installed && (
        <>
          <Section title="状态" desc={<code>reclaude status</code>} action={refresh}>
            <Loaded load={status} skeleton="h-32">
              {(d) => <StatusCard status={d.status} />}
            </Loaded>
          </Section>
          <Section title="组织" desc={<code>reclaude org</code>}>
            <Loaded load={orgs} skeleton="h-24" onError={(e) => <OrgsError error={e} />}>
              {(d) => (
                <Orgs
                  orgs={d.orgs}
                  onChanged={() => {
                    orgs.reload()
                    status.reload()
                  }}
                />
              )}
            </Loaded>
          </Section>
          <Section title="网关" desc={<code>reclaude config gateway</code>}>
            <Gateway
              load={gateway}
              live={get(status.data?.status, 'gateway_url')}
              onChanged={() => {
                gateway.reload()
                status.reload()
              }}
            />
          </Section>
        </>
      )}
    </>
  )
}

// 一块数据的三种样子：头一回取时是骨架，出错是红框，有了就画（之后刷新时先留着旧的）
function Loaded<T>({
  load,
  skeleton,
  onError,
  hint,
  children,
}: {
  load: Load<T>
  skeleton: string
  onError?: (error: string) => ReactNode
  hint?: ReactNode
  children: (data: T) => ReactNode
}) {
  if (load.error) return onError ? onError(load.error) : <Problem>{load.error}</Problem>
  if (!load.data)
    return (
      <div className="space-y-2">
        <Skeleton className={cn('w-full', skeleton)} />
        {hint}
      </div>
    )
  return children(load.data)
}

function Problem({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className={cn('flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2.5 text-xs text-destructive', className)}>
      <TriangleAlertIcon className="mt-px size-3.5 shrink-0" />
      <div className="min-w-0 break-words whitespace-pre-wrap">{children}</div>
    </div>
  )
}

function EnableSwitch({ info, onChange }: { info: ReclaudeInfo; onChange: (enabled: boolean) => void }) {
  const [saving, setSaving] = useState(false)
  const id = useId()

  async function toggle(enabled: boolean) {
    setSaving(true)
    try {
      await api('/api/reclaude/enabled', { method: 'POST', body: { enabled } })
      onChange(enabled)
      toast.success(enabled ? '之后新开的对话和终端都用 reclaude' : '换回 claude 了，之后新开的对话和终端用 claude')
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="space-y-3">
      <div className="flex items-start gap-4 rounded-lg border px-3 py-3">
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <label htmlFor={id} className="cursor-pointer text-sm font-medium">
              用 reclaude 代替 claude
            </label>
            {info.version && (
              <Badge variant="secondary" className="font-mono">
                {info.version}
              </Badge>
            )}
          </div>
          <p id={`${id}-desc`} className="text-xs text-muted-foreground">
            网页对话、网页终端、输入框的命令菜单里起 claude 的地方都换成 <code>reclaude</code>，参数不变。
            只影响之后新开的，已经在跑的对话和终端不变。
          </p>
          {info.path && <p className="truncate font-mono text-2xs text-muted-foreground">{info.path}</p>}
        </div>
        <Switch
          id={id}
          aria-describedby={`${id}-desc`}
          className="mt-0.5"
          checked={info.enabled}
          disabled={saving || (!info.installed && !info.enabled)}
          onCheckedChange={toggle}
        />
      </div>
      {!info.installed && (
        <Problem>
          VPS 上没找到 reclaude（按 RCWEB_RECLAUDE_BIN、PATH、~/.local/bin 的顺序找）。在 VPS 上装好后执行一次 <code>reclaude setup</code>，再回来刷新。
          {info.enabled && '\n开关还开着：现在新开对话会失败，关掉就换回 claude。'}
        </Problem>
      )}
    </div>
  )
}

function Dot({ ok }: { ok: boolean | undefined }) {
  return <span className={cn('inline-block size-2 shrink-0 rounded-full', ok ? 'bg-emerald-500' : ok === false ? 'bg-destructive' : 'bg-muted-foreground/40')} />
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-baseline gap-3 px-3 py-2">
      <div className="w-16 shrink-0 text-xs text-muted-foreground">{label}</div>
      <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1 text-sm">{children}</div>
    </div>
  )
}

function StatusCard({ status: st }: { status: KV[] }) {
  const running = get(st, 'daemon_running')
  const reachable = get(st, 'daemon_reachable')
  const healthy = get(st, 'gateway_healthy')
  return (
    <div className="rounded-lg border">
      <div className="divide-y">
        <Row label="daemon">
          <Dot ok={running === undefined ? undefined : running === 'true' && reachable !== 'false'} />
          <span>{running === 'true' ? (reachable === 'false' ? '在跑，但连不上' : '运行中') : running === 'false' ? '没在跑（下次起 reclaude 时自动启动）' : '未知'}</span>
          {get(st, 'daemon_version') && <span className="font-mono text-xs text-muted-foreground">{get(st, 'daemon_version')}</span>}
          {get(st, 'daemon_started_at') && <span className="text-xs text-muted-foreground">启动于 {get(st, 'daemon_started_at')}</span>}
        </Row>
        <Row label="网关">
          <Dot ok={healthy === undefined ? undefined : healthy === 'true'} />
          <span className="min-w-0 truncate font-mono text-xs">{get(st, 'gateway_url') ?? '—'}</span>
          {healthy !== undefined && <span className="text-xs text-muted-foreground">{healthy === 'true' ? '正常' : '不通'}</span>}
        </Row>
        <Row label="claude">
          <span className="min-w-0 truncate font-mono text-xs">{get(st, 'claude_path') ?? '—'}</span>
        </Row>
      </div>
      {st.length > 0 && (
        <Collapsible className="group/raw border-t">
          <CollapsibleTrigger className="flex w-full items-center gap-1 px-3 py-2 text-xs text-muted-foreground hover:text-foreground">
            <ChevronRightIcon className="size-3.5 transition-transform group-data-[state=open]/raw:rotate-90" /> 全部输出
          </CollapsibleTrigger>
          <CollapsibleContent>
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 px-3 pb-3 font-mono text-2xs">
              {st.map((x) => (
                <div key={x.key} className="contents">
                  <dt className="text-muted-foreground">{x.key}</dt>
                  <dd className="min-w-0 break-all">{x.value}</dd>
                </div>
              ))}
            </dl>
          </CollapsibleContent>
        </Collapsible>
      )}
    </div>
  )
}

function OrgsError({ error }: { error: string }) {
  return (
    <Problem>
      {error}
      {'\n'}
      还没登录的话，到
      <a href={loginTerm} className="mx-1 underline underline-offset-2">
        网页终端
      </a>
      里执行 <code>reclaude login</code>。
    </Problem>
  )
}

function Orgs({ orgs, onChanged }: { orgs: ReclaudeOrg[]; onChanged: () => void }) {
  const [switching, setSwitching] = useState('')

  async function use(o: ReclaudeOrg) {
    if (!confirm(`切换到组织「${o.name}」（${o.id}）？\n\n之后新起的 reclaude 用这个组织的账号和额度。`)) return
    setSwitching(o.id)
    try {
      const r = await api<{ output: string }>('/api/reclaude/org', { method: 'POST', body: { id: o.id } })
      toast.success(r.output || `已切换到「${o.name}」`)
      onChanged()
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setSwitching('')
    }
  }

  if (orgs.length === 0) return <p className="text-xs text-muted-foreground">reclaude 没列出任何组织。</p>
  return (
    <div className="divide-y rounded-lg border">
      {orgs.map((o) => (
        <div key={o.id} className={cn('flex items-center gap-3 px-3 py-2.5', o.current && 'bg-primary/5')}>
          <div className="min-w-0 flex-1">
            <div className="flex min-w-0 items-center gap-2">
              <span className="truncate text-sm">{o.name}</span>
              {o.type && (
                <Badge variant="outline" className="shrink-0">
                  {o.type}
                </Badge>
              )}
            </div>
            <div className="truncate text-xs text-muted-foreground">
              <span className="font-mono">{o.id}</span>
              {o.email && ` · ${o.email}`}
            </div>
          </div>
          {o.current ? (
            <Badge className="shrink-0">当前</Badge>
          ) : (
            <Button size="sm" variant="outline" className="shrink-0" disabled={!!switching} onClick={() => use(o)}>
              {switching === o.id && <Loader2Icon className="animate-spin" />} 切换
            </Button>
          )}
        </div>
      ))}
    </div>
  )
}

const modeLabel: Record<string, string> = { manual: '手动指定', auto: '自动' }

function Gateway({ load, live, onChanged }: { load: Load<{ gateway: KV[] }>; live?: string; onChanged: () => void }) {
  const [results, setResults] = useState<GatewayResult[] | null>(null)
  const [busy, setBusy] = useState('') // test / test-custom / reset / 正在设成的地址
  const [custom, setCustom] = useState('')

  const gw = load.data?.gateway
  const current = get(gw, 'url')
  const source = get(gw, 'source')
  const mode = get(gw, 'mode')

  async function test(url?: string) {
    setBusy(url ? 'test-custom' : 'test')
    try {
      const r = await api<{ results: GatewayResult[] }>('/api/reclaude/gateway/test', { method: 'POST', body: { url: url ?? '' } })
      if (url) {
        const g = r.results[0]
        if (g?.ok) toast.success(`${g.url} 通，${g.detail}`)
        else toast.error(g ? `${g.url} 不通：${g.detail || g.status}` : '没测出结果')
      } else setResults(r.results)
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  async function use(url: string, force = false) {
    setBusy(url)
    try {
      const r = await api<{ output: string }>('/api/reclaude/gateway', { method: 'POST', body: { url, force } })
      toast.success(r.output || `网关换成了 ${url}`)
      if (url === custom.trim()) setCustom('')
      onChanged()
    } catch (e) {
      // 测不通时 reclaude 不让设，--force 可以硬设
      toast.error((e as Error).message, force ? undefined : { action: { label: '仍然使用', onClick: () => void use(url, true) } })
    } finally {
      setBusy('')
    }
  }

  async function reset() {
    if (!confirm('网关恢复成 reclaude 的默认选择？')) return
    setBusy('reset')
    try {
      const r = await api<{ output: string }>('/api/reclaude/gateway/reset', { method: 'POST' })
      toast.success(r.output || '网关已恢复默认')
      onChanged()
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  const customValid = /^https?:\/\/\S+$/.test(custom.trim())
  const slowHint = <p className="text-xs text-muted-foreground">reclaude 先要同步一遍配置，慢的时候要等半分钟以上…</p>

  return (
    <div className="space-y-3">
      <Loaded load={load} skeleton="h-10" hint={slowHint}>
        {() => (
          <div className="rounded-lg border">
            <Row label="当前">
              <span className="min-w-0 truncate font-mono text-xs">{current ?? '—'}</span>
              {mode && <Badge variant="secondary">{modeLabel[mode] ?? mode}</Badge>}
              {source && <span className="text-xs text-muted-foreground">来自 {source}</span>}
            </Row>
            {live && current && live !== current && (
              <p className="border-t px-3 py-2 text-xs text-amber-600 dark:text-amber-400">
                正在跑的 daemon 还在用 <code>{live}</code>，reclaude 说它可能要重启后才换过去。
              </p>
            )}
          </div>
        )}
      </Loaded>

      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="outline" disabled={!!busy} onClick={() => test()}>
          {busy === 'test' ? <Loader2Icon className="animate-spin" /> : <GaugeIcon />} {results ? '重新测速' : '测速'}
        </Button>
        <Button size="sm" variant="ghost" className="text-muted-foreground" disabled={!!busy} onClick={reset}>
          {busy === 'reset' ? <Loader2Icon className="animate-spin" /> : <RotateCcwIcon />} 恢复默认
        </Button>
      </div>

      {busy === 'test' && !results && <p className="text-xs text-muted-foreground">挨个连一遍，要几秒钟…</p>}
      {results && (
        <div className="divide-y rounded-lg border">
          {results.length === 0 && <p className="px-3 py-2.5 text-xs text-muted-foreground">没测出结果。</p>}
          {results.map((g, i) => (
            <div key={g.url} className="flex items-center gap-3 px-3 py-2">
              <Dot ok={g.ok} />
              <div className="min-w-0 flex-1">
                <div className="truncate font-mono text-xs">{g.url}</div>
                <div className={cn('truncate text-xs', g.ok ? 'text-muted-foreground' : 'text-destructive')}>
                  {g.ok ? g.detail : `${g.status} ${g.detail}`.trim()}
                  {g.ok && i === 0 && ' · 最快'}
                </div>
              </div>
              {g.url === current ? (
                <Badge className="shrink-0">当前</Badge>
              ) : (
                <Button size="sm" variant="outline" className="shrink-0" disabled={!!busy} onClick={() => use(g.url)}>
                  {busy === g.url && <Loader2Icon className="animate-spin" />} 使用
                </Button>
              )}
            </div>
          ))}
        </div>
      )}

      <div className="space-y-1.5">
        <div className="text-xs text-muted-foreground">自定义地址</div>
        <form
          className="flex gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            if (customValid) void use(custom.trim())
          }}
        >
          <Input value={custom} onChange={(e) => setCustom(e.target.value)} placeholder="https://…" className="font-mono text-xs" spellCheck={false} autoCapitalize="off" />
          <Button type="button" variant="outline" className="shrink-0" disabled={!customValid || !!busy} onClick={() => test(custom.trim())}>
            {busy === 'test-custom' && <Loader2Icon className="animate-spin" />} 测试
          </Button>
          <Button type="submit" className="shrink-0" disabled={!customValid || !!busy}>
            {busy === custom.trim() && <Loader2Icon className="animate-spin" />} 使用
          </Button>
        </form>
      </div>
    </div>
  )
}
