import { useCallback, useEffect, useMemo, useReducer, useRef } from 'react'
import { toast } from 'sonner'
import { api, wsURL, type Catalog, type ChatInfo, type Entry, type Page, type PermissionAnswer, type PermissionReq } from '@/lib/api'
import { publishChat, type ContextUsage } from '@/lib/status'
import { toolDetails } from '@/lib/tool-details'

// 一个网页对话的 WebSocket 状态机。协议见 ../../chat.go：
// 连上先收 snapshot（最后一页 Entry + 待批准的请求），之后收增量；断了就重连，重连又从 snapshot 开始，
// 所以前端不用关心断线期间漏了什么。
// 快照里的工具卡片是精简版，往上翻的更早几页、卡片全文都走 HTTP 另取。

export type Conn = 'connecting' | 'open' | 'retrying' | 'gone'

type Result = { ok: boolean; subtype: string; cost: number; durationMs: number }

type State = {
  info?: ChatInfo
  catalog?: Catalog // 这个 claude 进程报的模型列表和斜杠命令
  entries: Entry[]
  start: number // entries[0] 在整个对话里的下标
  more: boolean // 前面还有更早的
  pending: PermissionReq[]
  stderr: string[]
  live: string // 正在流式输出、还没落成完整 Entry 的文字
  suggestion: string // CLI 预测的下一句，输入框里淡色显示
  context?: ContextUsage // 上下文用量（claude 的 get_context_usage），底部状态栏显示
  baseline: number // 快照带来的到哪条为止（整个对话的下标）：之后新来的才做入场动画
  conn: Conn
  result?: Result
}

type Msg =
  | {
      type: 'snapshot'
      chat: ChatInfo
      catalog?: Catalog
      suggestion?: string
      entries: Entry[]
      start?: number
      more?: boolean
      pending: PermissionReq[]
      stderr: string[]
      context?: ContextUsage
    }
  | { type: 'older'; before: number; page: Page }
  | { type: 'activity'; activity: string }
  | { type: 'suggestion'; text: string }
  | { type: 'context'; context: ContextUsage }
  | { type: 'catalog'; catalog: Catalog }
  | { type: 'entry'; entry: Entry }
  | { type: 'delta'; text: string }
  | { type: 'state'; chat: ChatInfo }
  | { type: 'permission'; req: PermissionReq }
  | { type: 'permission_done'; id: string }
  | ({ type: 'result' } & Result)
  | { type: 'stderr'; line: string }
  | { type: 'conn'; conn: Conn }
  | { type: 'reset' }

const initial: State = { entries: [], start: 0, more: false, pending: [], stderr: [], live: '', suggestion: '', baseline: 0, conn: 'connecting' }

function reducer(s: State, m: Msg): State {
  switch (m.type) {
    case 'reset':
      return initial
    case 'snapshot':
      return {
        info: m.chat,
        catalog: m.catalog ?? s.catalog,
        entries: m.entries ?? [],
        start: m.start ?? 0,
        more: !!m.more,
        pending: m.pending ?? [],
        stderr: m.stderr ?? [],
        live: '',
        suggestion: m.suggestion ?? '',
        context: m.context ?? s.context,
        baseline: (m.start ?? 0) + (m.entries ?? []).length,
        conn: 'open',
      }
    case 'older':
      // 取的时候又来了快照（重连、/clear）就作废
      if (s.start !== m.before) return s
      return { ...s, entries: [...m.page.entries, ...s.entries], start: m.page.start, more: m.page.more }
    case 'activity':
      return s.info ? { ...s, info: { ...s.info, activity: m.activity } } : s
    case 'suggestion':
      return { ...s, suggestion: m.text }
    case 'context':
      return { ...s, context: m.context }
    case 'catalog':
      return { ...s, catalog: m.catalog }
    case 'entry': {
      // 完整的文字记录到了，流式的那段就可以收起来；用户发了新消息也意味着上一轮结束
      const clearLive = m.entry.role === 'user' || m.entry.blocks.some((b) => b.t === 'text')
      return {
        ...s,
        entries: [...s.entries, m.entry],
        live: clearLive ? '' : s.live,
        suggestion: m.entry.role === 'user' ? '' : s.suggestion,
        result: m.entry.role === 'user' ? undefined : s.result,
      }
    }
    case 'delta':
      return { ...s, live: s.live + m.text }
    case 'state':
      return { ...s, info: m.chat }
    case 'permission':
      return s.pending.some((p) => p.id === m.req.id) ? s : { ...s, pending: [...s.pending, m.req] }
    case 'permission_done':
      return { ...s, pending: s.pending.filter((p) => p.id !== m.id) }
    case 'result':
      return { ...s, live: '', result: { ok: m.ok, subtype: m.subtype, cost: m.cost, durationMs: m.durationMs } }
    case 'stderr':
      return { ...s, stderr: [...s.stderr.slice(-99), m.line] }
    case 'conn':
      return { ...s, conn: m.conn }
  }
  return s
}

const watching = () => document.visibilityState === 'visible' && document.hasFocus()

export function useChat(id: string) {
  const [state, dispatch] = useReducer(reducer, initial)
  const wsRef = useRef<WebSocket | null>(null)

  useEffect(() => {
    let stopped = false
    let retry = 0
    let timer: ReturnType<typeof setTimeout> | undefined
    dispatch({ type: 'reset' })

    function connect() {
      const ws = new WebSocket(wsURL(`/ws/chats/${id}`))
      wsRef.current = ws
      ws.onopen = () => {
        retry = 0
        sendPresence()
      }
      ws.onmessage = (e) => {
        const msg = JSON.parse(e.data)
        if (msg.type === 'error') toast.error(msg.message)
        else if (msg.type === 'closed') {
          stopped = true
          dispatch({ type: 'conn', conn: 'gone' })
        } else dispatch(msg)
      }
      ws.onclose = async () => {
        if (stopped) return
        wsRef.current = null
        // 先确认对话还在：rcweb 重启过的话它已经没了，别无限重连
        const exists = await api<ChatInfo[]>('/api/chats')
          .then((list) => list.some((c) => c.id === id))
          .catch(() => true)
        if (stopped) return
        if (!exists) return dispatch({ type: 'conn', conn: 'gone' })
        dispatch({ type: 'conn', conn: 'retrying' })
        timer = setTimeout(connect, Math.min(500 * 2 ** retry++, 8000))
      }
    }
    // 告诉服务端这个页面是不是在前台：有人盯着这个对话就不发通知（见 ../../../notify.go）
    function sendPresence() {
      const ws = wsRef.current
      if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'presence', visible: watching() }))
    }
    const events = ['visibilitychange', 'focus', 'blur'] as const
    events.forEach((e) => (e === 'visibilitychange' ? document : window).addEventListener(e, sendPresence))
    connect()
    return () => {
      stopped = true
      clearTimeout(timer)
      events.forEach((e) => (e === 'visibilitychange' ? document : window).removeEventListener(e, sendPresence))
      wsRef.current?.close()
      wsRef.current = null
    }
  }, [id])

  const loadOlder = useCallback(async (before: number) => {
    const page = await api<Page>(`/api/chats/${id}/entries?before=${before}`)
    dispatch({ type: 'older', before, page })
  }, [id])
  const tools = useMemo(() => toolDetails(`/api/chats/${id}/tools?`), [id])

  // 底部状态栏（App 里常驻）按路由里的对话 ID 取这两项
  useEffect(() => {
    publishChat(id, { context: state.context, activity: state.info?.activity })
  }, [id, state.context, state.info?.activity])

  const send = useCallback((msg: object) => {
    const ws = wsRef.current
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      toast.error('连接还没恢复，稍等一下')
      return false
    }
    ws.send(JSON.stringify(msg))
    return true
  }, [])

  const sendText = useCallback((text: string, attachments: string[] = []) => send({ type: 'send', text, attachments }), [send])
  const answer = useCallback((a: PermissionAnswer) => send({ type: 'permission', ...a }), [send])
  const interrupt = useCallback(() => send({ type: 'interrupt' }), [send])
  const setModel = useCallback((model: string) => send({ type: 'set_model', model }), [send])
  const setMode = useCallback((mode: string) => send({ type: 'set_mode', mode }), [send])
  const setEffort = useCallback((effort: string) => send({ type: 'set_effort', effort }), [send])
  // 告诉服务端有人看过了：「已完成」转「空闲」。不走 send()，没连上就算了，不弹提示
  const seen = useCallback(() => {
    if (wsRef.current?.readyState === WebSocket.OPEN) wsRef.current.send(JSON.stringify({ type: 'seen' }))
  }, [])

  return { ...state, loadOlder, tools, sendText, answer, interrupt, setModel, setMode, setEffort, seen }
}
