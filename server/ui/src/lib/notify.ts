import { useSyncExternalStore } from 'react'
import { api } from '@/lib/api'

// 通知：对话完成了、等你回答、出错了。两层：
// - 服务端发的（../../notify.go）：Web Push 发给订阅了的浏览器，ntfy / Bark 发到手机 App。页面关了、手机锁屏也收得到
// - 页面自己弹的：这个浏览器订阅不了推送（没有 PushManager 的老浏览器）时退而求其次，得开着页面才行
// 这个浏览器用哪种记在 localStorage：push 订阅了 / local 页面自己弹 / off
//
// 浏览器只在 https 或 localhost 下给通知；iPhone 上还得先「添加到主屏幕」、从主屏幕打开（iOS 16.4 起）。

export type NoticeKind = 'done' | 'waiting' | 'error'
export type NotifyConfig = { off?: NoticeKind[]; ntfy?: string; bark?: string; publicUrl?: string }
export type NotifyInfo = { config: NotifyConfig; publicKey: string; subscriptions: number }
type Mode = 'push' | 'local' | 'off'

const KEY = 'rcweb.notify'
const subs = new Set<() => void>()

let mode: Mode = (() => {
  try {
    const v = localStorage.getItem(KEY)
    return v === 'push' || v === 'local' ? v : v === '1' ? 'local' : 'off' // '1' 是以前只有页面自己弹时存的
  } catch {
    return 'off'
  }
})()

function setMode(m: Mode) {
  mode = m
  try {
    localStorage.setItem(KEY, m)
  } catch {
    /* 记不住而已 */
  }
  subs.forEach((f) => f())
}

function subscribe(cb: () => void) {
  subs.add(cb)
  return () => {
    subs.delete(cb)
  }
}

export const useNotifyMode = () => useSyncExternalStore(subscribe, () => mode)
export const notifyMode = () => mode

const isIOS = () => /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
const standalone = () => matchMedia('(display-mode: standalone)').matches || (navigator as { standalone?: boolean }).standalone === true

// 为什么这个浏览器开不了：null = 能开
export function notifyUnsupported(): string | null {
  if (!window.isSecureContext)
    return '现在是 http 连接，浏览器不让网页发通知（手机、电脑都一样）。改用 https 打开（比如 Tailscale），或者用下面的 ntfy / Bark 推到手机 App。'
  if (typeof Notification === 'undefined') {
    if (isIOS() && !standalone()) return 'iPhone / iPad 上要先用 Safari 的「分享 → 添加到主屏幕」，再从主屏幕打开，才能开通知（iOS 16.4 起）。'
    return '这个浏览器不支持网页通知。'
  }
  return null
}

const canPush = () => 'serviceWorker' in navigator && 'PushManager' in window

let registration: Promise<ServiceWorkerRegistration | undefined> = Promise.resolve(undefined)

// 页面一打开就注册（main.tsx）：通知的点击、手机上弹通知都靠它
export function registerServiceWorker() {
  if (!('serviceWorker' in navigator) || !window.isSecureContext) return
  registration = navigator.serviceWorker.register('/sw.js').catch(() => undefined)
  // 点了通知：service worker 让已经开着的页面自己跳到那个对话（hash 路由，不整页重新加载）
  navigator.serviceWorker.addEventListener('message', (e) => {
    if (e.data?.type === 'open') location.hash = new URL(e.data.url, location.origin).hash || '#/'
  })
  // 服务器上的订阅可能没了（推送服务说过期、state 目录被清）：推送模式下每次打开再登记一次，同一个 endpoint 只是覆盖
  if (mode === 'push') void subscribePush().catch(() => {})
}

const b64 = (buf: ArrayBuffer | null) =>
  buf ? btoa(String.fromCharCode(...new Uint8Array(buf))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '') : ''

function fromB64(s: string) {
  const bin = atob(s.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (s.length % 4)) % 4))
  return Uint8Array.from(bin, (c) => c.charCodeAt(0))
}

async function subscribePush() {
  const r = await registration
  if (!r) throw new Error('service worker 没注册上，刷新页面再试')
  const info = await api<NotifyInfo>('/api/notify')
  let sub = await r.pushManager.getSubscription()
  // 服务器换过密钥时旧订阅收不到，退掉重来
  if (sub && b64(sub.options.applicationServerKey) !== info.publicKey) {
    await sub.unsubscribe()
    sub = null
  }
  sub ??= await r.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: fromB64(info.publicKey) })
  await api('/api/push/subscribe', { method: 'POST', body: { ...sub.toJSON(), origin: location.origin } })
  return sub
}

// enableNotify：要权限，能订阅推送就订阅（页面关了也收得到），不能就退到页面自己弹。要在点击里调（iOS 的要求）
export async function enableNotify() {
  const why = notifyUnsupported()
  if (why) throw new Error(why)
  const p = Notification.permission === 'default' ? await Notification.requestPermission() : Notification.permission
  if (p !== 'granted') throw new Error(p === 'denied' ? '浏览器拒绝了这个网站的通知，要到浏览器的网站设置里改成允许。' : '没有拿到通知权限。')
  if (canPush()) {
    await subscribePush()
    setMode('push')
  } else setMode('local')
}

export async function disableNotify() {
  if (mode === 'push') {
    const sub = await (await registration)?.pushManager.getSubscription()
    if (sub) {
      await api('/api/push/unsubscribe', { method: 'POST', body: { endpoint: sub.endpoint } }).catch(() => {})
      await sub.unsubscribe().catch(() => {})
    }
  }
  setMode('off')
}

// testNotify：推送模式让服务器真发一条（顺便验证 VPS 连得上推送服务），页面模式就地弹一条
export async function testNotify() {
  if (mode === 'push') {
    const sub = await subscribePush()
    await api('/api/notify/test', { method: 'POST', body: { channel: sub.endpoint } })
  } else await showLocal({ title: 'rcweb 通知试一下', body: '对话完成、等你回答、出错时会这样提醒你', tag: 'rcweb-test', url: location.href })
}

// showLocal：页面自己弹。交给 service worker 弹（手机上的 Chrome 不让页面 new Notification），没有才直接弹
export async function showLocal(n: { title: string; body: string; tag: string; url: string }) {
  if (mode !== 'local' || notifyUnsupported() || Notification.permission !== 'granted') return
  const sw = (await registration)?.active
  if (sw) return sw.postMessage({ type: 'notify', ...n })
  try {
    const x = new Notification(n.title, { body: n.body, tag: n.tag })
    x.onclick = () => {
      window.focus()
      location.hash = new URL(n.url, location.origin).hash
      x.close()
    }
  } catch {
    /* 发不出来就算了，标题和图标上的点还在 */
  }
}
