import { useEffect, useRef } from 'react'
import { useTheme } from 'next-themes'
import { agentLabel, agentOf } from '@/lib/agents'
import { api, type ChatInfo, type ChatStatus } from '@/lib/api'
import { showLocal, useNotifyMode, type NoticeKind, type NotifyInfo } from '@/lib/notify'
import { href } from '@/lib/route'

// 人不在这个页面上时也得知道「有对话等你回答」：标签页标题、favicon 右上角的点。
// 系统通知一般是服务端发的（Web Push / ntfy / Bark，见 ../../../notify.go）；这个浏览器订阅不了推送、
// 退到「页面自己弹」时，才由这里按对话状态的变化弹。数据来自 DataProvider 的轮询，所以最多晚一个轮询周期

const TITLE = 'remote-claude'
// 和 index.css 里的 --background 同色：手机浏览器顶栏跟着页面走
const BAR = { light: '#faf9f5', dark: '#262624' }

export function AttentionSignals({ chats, loaded }: { chats: ChatInfo[]; loaded: boolean }) {
  const waiting = chats.filter((c) => c.status === 'waiting')
  const first = waiting[0]

  useEffect(() => {
    document.title =
      waiting.length === 0
        ? TITLE
        : waiting.length === 1
          ? `● 等你回答 · ${first.title || '新对话'}`
          : `● ${waiting.length} 个对话等你回答`
    setFavicon(waiting.length > 0)
  }, [waiting.length, first?.title])

  // 页面自己弹：状态变了才弹（变成等你回答 / 运行完了 / 出错），第一次拿到列表时已经是这样的不算（打开页面就能看见）
  const mode = useNotifyMode()
  const off = useRef<NoticeKind[]>([])
  useEffect(() => {
    if (mode === 'local')
      api<NotifyInfo>('/api/notify')
        .then((i) => (off.current = i.config.off ?? []))
        .catch(() => {})
  }, [mode])
  const last = useRef<Map<string, ChatStatus> | null>(null)
  useEffect(() => {
    if (!loaded) return
    const prev = last.current
    const next = new Map(chats.map((c) => [c.id, c.status]))
    last.current = next
    if (!prev || mode !== 'local' || (document.visibilityState === 'visible' && document.hasFocus())) return
    for (const c of chats) {
      const was = prev.get(c.id)
      const kind = localNotice(was, c.status)
      if (!kind || off.current.includes(kind)) continue
      const who = agentLabel[agentOf(c.agent)]
      const title = { done: `${who} 完成了`, waiting: `${who} 等你回答`, error: `${who} 出错了` }[kind]
      void showLocal({ title, body: `${c.title || '新对话'} · ${c.project}`, tag: `rcweb-${c.id}`, url: href.chat(c.id) })
    }
  }, [chats, loaded, mode])

  const { resolvedTheme } = useTheme()
  useEffect(() => {
    const color = resolvedTheme === 'dark' ? BAR.dark : BAR.light
    document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]').forEach((m) => (m.content = color))
  }, [resolvedTheme])

  return null
}

function localNotice(was: ChatStatus | undefined, now: ChatStatus): NoticeKind | undefined {
  if (was === undefined || was === now) return
  if (now === 'waiting') return 'waiting'
  if (now === 'error') return 'error'
  // 后台任务还在跑不算完成，等它跑完、CLI 接着的那一轮结束
  if (now === 'done' && (was === 'running' || was === 'background')) return 'done'
}

// 侧栏标志那颗 ✻ 画成 SVG（favicon 里的字形各平台不一样）；等你回答时右上角加一个琥珀点
export function faviconSvg(dot: boolean) {
  const spokes = [0, 45, 90, 135].map((a) => `<line x1="16" y1="8" x2="16" y2="24" transform="rotate(${a} 16 16)"/>`).join('')
  const badge = dot ? '<circle cx="25" cy="7" r="6.5" fill="#d97706" stroke="#fff" stroke-width="2"/>' : ''
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32" rx="7" fill="#b7522f"/><g stroke="#fff" stroke-width="3" stroke-linecap="round">${spokes}</g>${badge}</svg>`
}

function setFavicon(dot: boolean) {
  let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
  if (!link) {
    link = document.createElement('link')
    link.rel = 'icon'
    document.head.appendChild(link)
  }
  link.type = 'image/svg+xml'
  link.href = `data:image/svg+xml,${encodeURIComponent(faviconSvg(dot))}`
}
