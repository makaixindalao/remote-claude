import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { SquareTerminalIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { wsURL } from '@/lib/api'
import { fontReady, fontStack, MONO_FALLBACK, termFontSize, useAppearance } from '@/lib/appearance'
import { isTouch } from '@/lib/format'
import { termStatusLabel, type TermStatus } from '@/lib/term-status'
import { termTheme } from '@/lib/term-theme'
import { cn } from '@/lib/utils'

// 终端：xterm.js ↔ WebSocket ↔ PTY ↔ tmux attach（见 ../../term.go）。
// 断线恢复交给 tmux：重连就是再 attach 一次，里面的程序一直在跑。
// 整页终端（跑 Claude TUI）和对话页右侧的 shell 面板共用这一个。
// xterm 占了前端包的一大半，这个文件只许被懒加载（App.tsx、side-terminal.tsx），别在首屏静态 import。

export { termStatusLabel, type TermStatus }

// 手机键盘上没有的键
const KEYS: [string, string][] = [
  ['Esc', '\x1b'],
  ['Tab', '\t'],
  ['⇧Tab', '\x1b[Z'],
  ['↑', '\x1b[A'],
  ['↓', '\x1b[B'],
  ['←', '\x1b[D'],
  ['→', '\x1b[C'],
  ['^C', '\x03'],
  ['^D', '\x04'],
  ['⏎', '\r'],
]

type Props = {
  session: string
  mode?: string // claude（默认）/ codex / grok / shell / attach
  project?: string
  resume?: string
  dir?: string
  statusOff?: boolean // 关掉 tmux 状态栏
  follow?: 'app' | 'dark' // 配色跟页面主题走，还是固定深色
  reconnectSignal?: number // 变一下就重连
  onStatus?: (s: TermStatus) => void
  className?: string
}

export function TerminalPane({ session, mode, project, resume, dir, statusOff, follow = 'dark', reconnectSignal = 0, onStatus, className }: Props) {
  const boxRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const firstRef = useRef(true)
  const forceMode = useRef<string | null>(null) // 「新建」按钮用：下一次连接改成新建
  const [status, setStatusState] = useState<TermStatus>('connecting')
  const [nonce, setNonce] = useState(0)
  const touch = isTouch()
  const look = useAppearance()
  const fontFamily = fontStack('mono', look)
  const fontSize = termFontSize(look, touch)
  const onStatusRef = useRef(onStatus)
  onStatusRef.current = onStatus
  const setStatus = (s: TermStatus) => {
    setStatusState(s)
    onStatusRef.current?.(s)
  }

  const send = (data: string | Uint8Array<ArrayBuffer>) => {
    const ws = wsRef.current
    if (ws?.readyState === WebSocket.OPEN) ws.send(typeof data === 'string' ? new TextEncoder().encode(data) : data)
  }

  // 终端实例：每个 tmux 会话一个
  useEffect(() => {
    // 先用系统等宽字体，选的字体由下面那个 effect 换上：得等字体文件到位再量字宽
    const term = new Terminal({
      cursorBlink: true,
      fontSize,
      fontFamily: MONO_FALLBACK,
      scrollback: 5000,
      macOptionIsMeta: true,
      theme: termTheme(follow),
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(boxRef.current!)
    fit.fit()
    termRef.current = term
    fitRef.current = fit
    firstRef.current = true

    const subs = [
      term.onData((d) => send(d)),
      term.onBinary((d) => send(Uint8Array.from(d, (c) => c.charCodeAt(0) & 0xff))),
      term.onResize(({ cols, rows }) => {
        const ws = wsRef.current
        if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'resize', cols, rows }))
      }),
    ]
    const ro = new ResizeObserver(() => fit.fit())
    ro.observe(boxRef.current!)
    return () => {
      subs.forEach((s) => s.dispose())
      ro.disconnect()
      term.dispose()
      termRef.current = null
      fitRef.current = null
    }
  }, [session, touch])

  // 设置页改了代码字体 / 终端字号也当场换。xterm 只在 fontFamily / fontSize 变了时量格子宽度，
  // 字体没下载完就设上会量错、下完了也不会重量，所以等它到位再设
  useEffect(() => {
    let stale = false
    fontReady('mono', look, fontSize).then(() => {
      const term = termRef.current
      if (stale || !term) return
      term.options.fontFamily = fontFamily
      term.options.fontSize = fontSize
      fitRef.current?.fit()
    })
    return () => {
      stale = true
    }
  }, [session, touch, look, fontFamily, fontSize])

  // 切换浅色 / 深色时跟着换。盯 <html> 的 class 而不是 resolvedTheme：next-themes 改 class 在它之后，
  // 这时读 CSS 变量还是旧主题的颜色
  useEffect(() => {
    if (follow !== 'app') return
    const apply = () => {
      if (termRef.current) termRef.current.options.theme = termTheme(follow)
    }
    const mo = new MutationObserver(apply)
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
    return () => mo.disconnect()
  }, [follow])

  useEffect(() => {
    if (reconnectSignal) setNonce((n) => n + 1)
  }, [reconnectSignal])

  // 连接：首次按 mode 建 / 接会话，之后的重连一律只 attach —— 会话结束后别悄悄新建一个
  useEffect(() => {
    const term = termRef.current
    if (!term) return
    let stopped = false
    let opened = false
    const q = new URLSearchParams({ session, cols: String(term.cols), rows: String(term.rows) })
    q.set('mode', forceMode.current ?? (firstRef.current ? mode || 'claude' : 'attach'))
    forceMode.current = null
    if (project) q.set('project', project)
    if (dir) q.set('dir', dir)
    if (statusOff) q.set('status', 'off')
    if (resume && firstRef.current) q.set('resume', resume)
    firstRef.current = false

    setStatus('connecting')
    const ws = new WebSocket(wsURL(`/ws/term?${q}`))
    ws.binaryType = 'arraybuffer'
    wsRef.current = ws
    ws.onopen = () => {
      opened = true
      setStatus('open')
      ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
      if (!touch) term.focus()
    }
    ws.onmessage = (e) => term.write(typeof e.data === 'string' ? e.data : new Uint8Array(e.data))
    ws.onclose = (e) => {
      if (stopped) return
      wsRef.current = null
      if (!opened) return setStatus('failed')
      // 1000 = 服务端说 tmux 客户端退出了（会话结束或被 detach）；其余是网络断了，自动重连
      if (e.code === 1000) return setStatus('ended')
      setStatus('retrying')
      setTimeout(() => !stopped && setNonce((n) => n + 1), 1500)
    }
    return () => {
      stopped = true
      ws.close()
    }
  }, [session, nonce, mode, project, resume, dir, statusOff, touch])

  const bg = follow === 'app' ? 'bg-background' : 'bg-[#1f1e1d]'
  // 「新建」按钮起的程序：跟着进来时的 mode 走；从 tmux 列表 attach 进来的不知道原来跑的是啥，按 claude
  const newMode = mode && mode !== 'attach' ? mode : 'claude'
  return (
    <div className={cn('flex min-h-0 flex-1 flex-col', className)}>
      <div className={cn('relative min-h-0 flex-1', bg)}>
        <div ref={boxRef} className="absolute inset-0 overflow-hidden p-1.5" />
        {(status === 'ended' || status === 'failed') && (
          <div className="absolute inset-0 flex items-center justify-center bg-black/50 p-6">
            <div className="max-w-sm rounded-xl bg-background p-5 text-center shadow-lg">
              <SquareTerminalIcon className="mx-auto size-6 text-muted-foreground" />
              <p className="mt-3 text-sm">
                {status === 'ended' ? 'tmux 会话已经结束，或者被 detach 了。' : '连不上这个终端：tmux 会话可能已经不在了。'}
              </p>
              <div className="mt-4 flex justify-center gap-2">
                <Button size="sm" variant="outline" onClick={() => setNonce((n) => n + 1)}>
                  重新接回
                </Button>
                <Button
                  size="sm"
                  onClick={() => {
                    forceMode.current = newMode
                    setNonce((n) => n + 1)
                  }}
                >
                  {newMode === 'shell' ? '新开一个 shell' : `新建并启动 ${newMode}`}
                </Button>
              </div>
            </div>
          </div>
        )}
      </div>
      {touch && (
        <div className="flex shrink-0 gap-1.5 overflow-x-auto border-t bg-background px-2 pt-2 pb-[max(0.5rem,env(safe-area-inset-bottom))]">
          {KEYS.map(([label, seq]) => (
            <Button
              key={label}
              size="sm"
              variant="outline"
              className="shrink-0 font-mono"
              onPointerDown={(e) => e.preventDefault()}
              onClick={() => send(seq)}
            >
              {label}
            </Button>
          ))}
        </div>
      )}
    </div>
  )
}
