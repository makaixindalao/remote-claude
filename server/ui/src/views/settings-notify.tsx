import { useEffect, useId, useState } from 'react'
import { Loader2Icon } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { SettingsSection as Section } from '@/components/settings-section'
import { api } from '@/lib/api'
import {
  disableNotify,
  enableNotify,
  notifyUnsupported,
  testNotify,
  useNotifyMode,
  type NoticeKind,
  type NotifyConfig,
  type NotifyInfo,
} from '@/lib/notify'
import { Field } from '@/views/settings-rc'

// 设置页的「通知」一页：这个浏览器收不收（Web Push），推到手机 App（ntfy / Bark），以及哪些事要通知。
// 后两样存在 VPS 上（见 ../../notify.go），所有浏览器共用；浏览器订阅记在服务器上，开关状态记在这个浏览器里

const KINDS: [NoticeKind, string, string][] = [
  ['done', '完成', '这一轮做完了（后台命令、子代理也都跑完了）'],
  ['waiting', '等你回答', '要你批准命令、回答问题、审批计划'],
  ['error', '出错', '这一轮出错，或者 CLI 进程异常退出'],
]

export function NotifySettings() {
  const [info, setInfo] = useState<NotifyInfo>()
  const [error, setError] = useState('')
  const load = () =>
    api<NotifyInfo>('/api/notify')
      .then(setInfo)
      .catch((e) => setError(e.message))
  useEffect(() => {
    void load()
  }, [])

  async function save(next: NotifyConfig) {
    try {
      const config = await api<NotifyConfig>('/api/notify', { method: 'POST', body: next })
      setInfo((i) => i && { ...i, config })
      return true
    } catch (e) {
      toast.error(`没存上：${(e as Error).message}`)
      return false
    }
  }

  if (error) return <p className="text-sm text-destructive">{error}</p>
  if (!info) return <Skeleton className="h-40 w-full" />
  const off = info.config.off ?? []

  return (
    <>
      <Section title="这个浏览器" desc="系统通知。订阅了推送的话，页面关了、手机锁屏也收得到">
        <BrowserNotify subscriptions={info.subscriptions} onChange={load} />
      </Section>

      <Section title="推送到手机 App" desc="服务器直接发给 App：页面关着、用 http 打开 rcweb 也能收到">
        <AppPush config={info.config} onSave={save} />
      </Section>

      <Section title="通知哪些" desc="上面几条路都按这个来。有人正开着那个对话的页面（在前台）时不发">
        <div className="divide-y rounded-lg border">
          {KINDS.map(([k, label, hint]) => (
            <SwitchRow
              key={k}
              label={label}
              hint={hint}
              checked={!off.includes(k)}
              onChange={(on) => save({ ...info.config, off: on ? off.filter((x) => x !== k) : [...off, k] })}
            />
          ))}
        </div>
      </Section>
    </>
  )
}

function BrowserNotify({ subscriptions, onChange }: { subscriptions: number; onChange: () => void }) {
  const mode = useNotifyMode()
  const unsupported = notifyUnsupported()
  const [busy, setBusy] = useState('')
  const id = useId()
  const on = mode !== 'off' && !unsupported

  async function run(what: string, f: () => Promise<void>, ok?: string) {
    setBusy(what)
    try {
      await f()
      if (ok) toast.success(ok)
      onChange()
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-3">
        <Switch
          id={id}
          checked={on}
          disabled={!!unsupported || !!busy}
          onCheckedChange={(next) => run('toggle', next ? enableNotify : disableNotify)}
        />
        <label htmlFor={id} className="text-sm">
          在这个浏览器上收通知
        </label>
        {busy === 'toggle' && <Loader2Icon className="size-4 animate-spin text-muted-foreground" />}
        {on && (
          <Button
            size="sm"
            variant="ghost"
            className="ml-auto text-muted-foreground"
            disabled={!!busy}
            onClick={() => run('test', testNotify, mode === 'push' ? '服务器发出去了，几秒内会弹出来' : undefined)}
          >
            {busy === 'test' && <Loader2Icon className="animate-spin" />} 试一下
          </Button>
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        {unsupported ??
          (mode === 'push'
            ? `已订阅推送：页面关了、手机锁屏也能收到。现在一共 ${subscriptions} 个浏览器订阅了。`
            : mode === 'local'
              ? '这个浏览器订阅不了推送，只在页面开着（在后台标签页里也行）时提醒。'
              : '打开后，对话完成、等你回答、出错时发系统通知。手机上的 Chrome 可以直接开；iPhone 要先把网页加到主屏幕。')}
      </p>
    </div>
  )
}

function AppPush({ config, onSave }: { config: NotifyConfig; onSave: (c: NotifyConfig) => Promise<boolean> }) {
  const [ntfy, setNtfy] = useState(config.ntfy ?? '')
  const [bark, setBark] = useState(config.bark ?? '')
  const [busy, setBusy] = useState('')
  const dirty = ntfy.trim() !== (config.ntfy ?? '') || bark.trim() !== (config.bark ?? '')

  async function save() {
    setBusy('save')
    // 点通知打开哪里：就用现在地址栏里的
    if (await onSave({ ...config, ntfy: ntfy.trim(), bark: bark.trim(), publicUrl: location.origin })) toast.success('存好了')
    setBusy('')
  }

  async function test(channel: 'ntfy' | 'bark') {
    setBusy(channel)
    try {
      await api('/api/notify/test', { method: 'POST', body: { channel } })
      toast.success('发出去了，看看手机')
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  const testButton = (channel: 'ntfy' | 'bark', saved?: string) =>
    saved && (
      <Button variant="outline" className="shrink-0" disabled={!!busy || dirty} onClick={() => test(channel)}>
        {busy === channel && <Loader2Icon className="animate-spin" />} 试一下
      </Button>
    )

  return (
    <div className="space-y-3">
      <div className="divide-y rounded-lg border">
        <Field label="ntfy" hint="安卓 / iPhone 装 ntfy App，订阅同一个主题">
          <div className="flex gap-2">
            <Input value={ntfy} placeholder="https://ntfy.sh/起一个别人猜不到的主题名" onChange={(e) => setNtfy(e.target.value)} />
            {testButton('ntfy', config.ntfy)}
          </div>
        </Field>
        <Field label="Bark" hint="iPhone 装 Bark，复制它首页的地址">
          <div className="flex gap-2">
            <Input value={bark} placeholder="https://api.day.app/你的 key" onChange={(e) => setBark(e.target.value)} />
            {testButton('bark', config.bark)}
          </div>
        </Field>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Button disabled={!dirty || !!busy} onClick={save}>
          {busy === 'save' && <Loader2Icon className="animate-spin" />} 保存
        </Button>
        <span className="text-xs text-muted-foreground">
          通知里带着对话标题和几句内容，会经过 ntfy / Bark 的服务器。点通知打开 {config.publicUrl || location.origin}
        </span>
      </div>
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
