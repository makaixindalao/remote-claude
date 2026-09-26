// 附件（见 ../../uploads.go）：输入框里粘贴、拖进来、点回形针选的文件和图片。
// 选中就开始传，发消息时只带 id。服务端把路径列在正文末尾的 <attachments> 里交给 CLI，
// 页面上（实时对话、会话记录）按同一段文字把附件显示回来。

export type Uploaded = { id: string; name: string; size: number; path: string; image: boolean }

// 和 uploads.go 一致
export const MAX_FILE = 100 << 20
export const MAX_ATTACHMENTS = 20
const VISION_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp']
const MAX_EDGE = 2000
const MAX_BYTES = ((5 << 20) * 3) / 4 // base64 之后 5MB

const toBlob = (c: HTMLCanvasElement, type: string, quality?: number) => new Promise<Blob | null>((r) => c.toBlob(r, type, quality))

// visionCopy：给模型看的那份图。undefined = 原图就能用；null = 不是图、或者浏览器也解不开（当普通文件，只给路径）。
// 原图太大、太长，或者是模型不认的格式（HEIC、BMP、AVIF…）时缩到长边 2000、3.75MB 以内
export async function visionCopy(file: File): Promise<Blob | null | undefined> {
  if (!file.type.startsWith('image/') || file.type === 'image/svg+xml') return null
  const url = URL.createObjectURL(file)
  try {
    const img = new Image()
    img.src = url
    await img.decode()
    const w = img.naturalWidth
    const h = img.naturalHeight
    if (!w || !h) return null
    if (VISION_TYPES.includes(file.type) && file.size <= MAX_BYTES && Math.max(w, h) <= MAX_EDGE) return undefined
    let scale = Math.min(1, MAX_EDGE / Math.max(w, h))
    // 截图多是 PNG：先试 PNG 保住文字的边缘，太大再换 JPEG，还大就接着缩
    for (let i = 0; i < 6; i++, scale *= 0.75) {
      const canvas = document.createElement('canvas')
      canvas.width = Math.max(1, Math.round(w * scale))
      canvas.height = Math.max(1, Math.round(h * scale))
      const ctx = canvas.getContext('2d')
      if (!ctx) return null
      if (i === 0 && file.type === 'image/png') {
        ctx.drawImage(img, 0, 0, canvas.width, canvas.height)
        const png = await toBlob(canvas, 'image/png')
        if (png && png.size <= MAX_BYTES) return png
      }
      ctx.fillStyle = '#fff' // JPEG 没有透明，透明的地方别变成黑的
      ctx.fillRect(0, 0, canvas.width, canvas.height)
      ctx.drawImage(img, 0, 0, canvas.width, canvas.height)
      const jpg = await toBlob(canvas, 'image/jpeg', 0.88)
      if (jpg && jpg.size <= MAX_BYTES) return jpg
    }
    return null
  } catch {
    return null
  } finally {
    URL.revokeObjectURL(url)
  }
}

// uploadFile 传一个文件（XHR：fetch 拿不到上传进度）
export function uploadFile(file: File, vision: Blob | undefined, onProgress: (f: number) => void, signal: AbortSignal) {
  return new Promise<Uploaded>((resolve, reject) => {
    const form = new FormData()
    form.append('file', file, file.name)
    if (vision) form.append('vision', vision, 'vision')
    const xhr = new XMLHttpRequest()
    xhr.open('POST', '/api/uploads')
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress(e.loaded / e.total)
    xhr.onload = () => {
      let data: { error?: string } & Partial<Uploaded> = {}
      try {
        data = JSON.parse(xhr.responseText)
      } catch {
        // 不是 JSON（被代理挡了之类），下面按状态码报
      }
      if (xhr.status === 401) {
        window.dispatchEvent(new Event('rcweb:unauthorized'))
        reject(new Error('登录已失效'))
      } else if (xhr.status >= 200 && xhr.status < 300 && data.id) resolve(data as Uploaded)
      else reject(new Error(data.error || `上传失败（${xhr.status}）`))
    }
    xhr.onerror = () => reject(new Error('上传失败：连不上服务器'))
    xhr.onabort = () => reject(new DOMException('已取消', 'AbortError'))
    signal.addEventListener('abort', () => xhr.abort())
    xhr.send(form)
  })
}

export const deleteUpload = (id: string) => fetch(`/api/uploads/${id}`, { method: 'DELETE' }).catch(() => {})

// 粘贴的截图浏览器一律叫 image.png，几张放在一起分不清；按时间起个名
export function pastedName(file: File) {
  if (!/^image\.\w+$/.test(file.name)) return file
  const d = new Date()
  const p = (n: number) => String(n).padStart(2, '0')
  const stamp = `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`
  return new File([file], `paste-${stamp}.${file.name.split('.').pop()}`, { type: file.type, lastModified: file.lastModified })
}

// ---- 显示：正文里的 <attachments> ----

// image：标了 (image)，模型直接看过（或能看）的图。id：还在上传目录里的，能取回原文件
export type AttachmentRef = { path: string; name: string; image: boolean; id?: string }

const TAG = /\s*<attachments>\n([\s\S]*?)\n<\/attachments>\s*/

// splitAttachments 把一条用户消息拆成打的字 + 附件；没有附件返回 null
export function splitAttachments(text: string): { text: string; files: AttachmentRef[] } | null {
  const m = TAG.exec(text)
  if (!m) return null
  const files: AttachmentRef[] = []
  for (const line of m[1].split('\n')) {
    const f = /^- (.+?)( \(image\))?$/.exec(line)
    if (!f) continue
    const path = f[1]
    files.push({ path, name: path.split('/').pop() || path, image: !!f[2], id: /\/uploads\/([0-9a-f]{16})\/[^/]+$/.exec(path)?.[1] })
  }
  const rest = [text.slice(0, m.index), text.slice(m.index + m[0].length)].filter((s) => s.trim()).join('\n')
  return { text: rest, files }
}

export const uploadURL = (id: string) => `/api/uploads/${id}`
export const previewURL = (id: string) => `/api/uploads/${id}/preview`
