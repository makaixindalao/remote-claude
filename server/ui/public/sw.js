// rcweb 的 service worker：只管通知，不缓存页面（页面照常走网络）。
// - push：服务端发来的 Web Push（../../notify.go），弹系统通知。正开着这个对话、页面在前台就不弹
// - notificationclick：点通知回到那个对话，已经开着的标签页就切过去，没有就新开
// - message：页面自己要弹的通知（没订阅推送、只在页面开着时提醒的那种）。手机上的 Chrome 不让页面直接 new Notification

self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()))

const show = (d) =>
  self.registration.showNotification(d.title || 'remote-claude', {
    body: d.body || '',
    tag: d.tag || 'rcweb',
    renotify: true,
    icon: '/icon-192.png',
    badge: '/badge-96.png',
    data: { url: d.url || '/' },
  })

self.addEventListener('push', (e) => {
  let d = {}
  try {
    d = e.data ? e.data.json() : {}
  } catch {
    d = { body: e.data ? e.data.text() : '' }
  }
  e.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((wins) => {
      const target = new URL(d.url || '/', self.location.origin)
      const watching = wins.some((w) => w.focused && w.visibilityState === 'visible' && new URL(w.url).hash === target.hash)
      return watching ? undefined : show(d)
    }),
  )
})

self.addEventListener('message', (e) => {
  if (e.data && e.data.type === 'notify') e.waitUntil(show(e.data))
})

self.addEventListener('notificationclick', (e) => {
  e.notification.close()
  const url = new URL((e.notification.data && e.notification.data.url) || '/', self.location.origin).href
  e.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then(async (wins) => {
      const w = wins.find((w) => new URL(w.url).origin === self.location.origin)
      if (!w) return self.clients.openWindow(url)
      // 页面用 hash 路由：让它自己改 hash，不整页重新加载（见 src/lib/notify.ts 的 registerServiceWorker）
      w.postMessage({ type: 'open', url })
      await w.focus()
    }),
  )
})
