import { useEffect, useReducer, useRef, useState } from 'react'
import { toast } from 'sonner'
import {
  FileArchiveIcon,
  FileCodeIcon,
  FileIcon,
  FileImageIcon,
  FileMusicIcon,
  FileSpreadsheetIcon,
  FileTextIcon,
  FileVideoCameraIcon,
  PaperclipIcon,
  RotateCcwIcon,
  XIcon,
} from 'lucide-react'
import { agentLabel, type Agent } from '@/lib/agents'
import {
  deleteUpload,
  MAX_ATTACHMENTS,
  MAX_FILE,
  pastedName,
  previewURL,
  uploadFile,
  uploadURL,
  visionCopy,
  type AttachmentRef,
  type Uploaded,
} from '@/lib/attachments'
import { fileSize } from '@/lib/format'
import { cn } from '@/lib/utils'

// 输入框的附件：选中就开始传（缩图、上传进度都在这里），发消息前等它们传完。
// 显示在输入框里面、文字上方：图片是缩略图方块，别的文件是一张带图标、名字、大小的小卡片。

export type Att = {
  key: string
  file: File
  thumb?: string // 能显示的图才有（object URL）
  progress: number
  status: 'uploading' | 'done' | 'error'
  error?: string
  uploaded?: Uploaded
  abort: AbortController
}

let seq = 0

export function useAttachments() {
  // 数据放 ref：发消息时要在等完上传之后立刻读到最新的，不能等下一次渲染
  const store = useRef<Att[]>([])
  const tasks = useRef(new Map<string, Promise<void>>())
  const [, render] = useReducer((n: number) => n + 1, 0)
  const set = (next: Att[]) => {
    store.current = next
    render()
  }
  const update = (key: string, patch: Partial<Att>) => set(store.current.map((a) => (a.key === key ? { ...a, ...patch } : a)))

  async function run(a: Att) {
    try {
      let vision: Blob | null | undefined = null
      if (a.file.type.startsWith('image/')) {
        vision = await visionCopy(a.file)
        if (a.abort.signal.aborted) return
        // 原图太大就拿缩图当缩略图；浏览器解不开的（Chrome 里的 HEIC）当普通文件显示
        if (vision !== undefined) {
          if (a.thumb) URL.revokeObjectURL(a.thumb)
          update(a.key, { thumb: vision ? URL.createObjectURL(vision) : undefined })
        }
      }
      const up = await uploadFile(a.file, vision ?? undefined, (progress) => update(a.key, { progress }), a.abort.signal)
      update(a.key, { status: 'done', progress: 1, uploaded: up })
    } catch (e) {
      if (a.abort.signal.aborted) return
      update(a.key, { status: 'error', error: (e as Error).message })
    }
  }

  function start(a: Att) {
    const t = run(a)
    tasks.current.set(a.key, t)
    t.finally(() => tasks.current.get(a.key) === t && tasks.current.delete(a.key))
  }

  function add(files: File[], pasted = false) {
    const room = MAX_ATTACHMENTS - store.current.length
    if (files.length > room) toast.error(`一条消息最多带 ${MAX_ATTACHMENTS} 个附件`)
    const added: Att[] = []
    for (let file of files.slice(0, Math.max(0, room))) {
      if (file.size > MAX_FILE) {
        toast.error(`${file.name} 太大了（${fileSize(file.size)}），上限 ${fileSize(MAX_FILE)}`)
        continue
      }
      if (pasted) file = pastedName(file)
      added.push({
        key: `att-${++seq}`,
        file,
        thumb: file.type.startsWith('image/') ? URL.createObjectURL(file) : undefined,
        progress: 0,
        status: 'uploading',
        abort: new AbortController(),
      })
    }
    if (!added.length) return
    set([...store.current, ...added])
    added.forEach(start)
  }

  function drop(a: Att) {
    a.abort.abort()
    if (a.thumb) URL.revokeObjectURL(a.thumb)
  }

  // 去掉：服务器上的也删掉，没发出去的别留着
  function remove(key: string) {
    const a = store.current.find((x) => x.key === key)
    if (!a) return
    drop(a)
    if (a.uploaded) deleteUpload(a.uploaded.id)
    set(store.current.filter((x) => x.key !== key))
  }

  function retry(key: string) {
    const a = store.current.find((x) => x.key === key)
    if (!a || a.status !== 'error') return
    const next = { ...a, status: 'uploading' as const, progress: 0, error: undefined, abort: new AbortController() }
    set(store.current.map((x) => (x.key === key ? next : x)))
    start(next)
  }

  // settle：等还在传的传完。有没传上去的就提示、返回 null（这条先不发）
  async function settle(): Promise<Uploaded[] | null> {
    await Promise.all(tasks.current.values())
    const failed = store.current.filter((a) => a.status !== 'done')
    if (failed.length) {
      toast.error(`${failed[0].file.name} 没传上去：${failed[0].error ?? '重试一下'}`, { description: '点附件上的重试，或者先去掉它' })
      return null
    }
    return store.current.map((a) => a.uploaded!)
  }

  // 发出去了：清掉输入框里的，服务器上的留着（消息里引用着）
  function clear() {
    store.current.forEach((a) => a.thumb && URL.revokeObjectURL(a.thumb))
    set([])
  }

  // 离开页面：停掉还在传的。传完了的不删 —— 新对话发出去就跳走，这时可能还没来得及 clear，
  // 删了就把刚发的附件删了；真没发出去的留在服务器上，30 天后清掉
  useEffect(
    () => () => {
      store.current.forEach(drop)
      store.current = []
    },
    [],
  )

  return { items: store.current, add, remove, retry, settle, clear, uploading: store.current.some((a) => a.status === 'uploading') }
}

export type Attachments = ReturnType<typeof useAttachments>

const EXT_ICONS: [RegExp, typeof FileIcon][] = [
  [/\.(png|jpe?g|gif|webp|heic|heif|avif|bmp|tiff?|svg|ico)$/i, FileImageIcon],
  [/\.(zip|tar|gz|tgz|bz2|xz|7z|rar|zst)$/i, FileArchiveIcon],
  [/\.(csv|tsv|xlsx?|numbers|ods)$/i, FileSpreadsheetIcon],
  [/\.(mp3|wav|m4a|flac|ogg|aac)$/i, FileMusicIcon],
  [/\.(mp4|mov|webm|mkv|avi)$/i, FileVideoCameraIcon],
  [/\.(pdf|docx?|pages|rtf|txt|md|log)$/i, FileTextIcon],
  [/\.(jsx?|tsx?|go|py|rs|rb|java|kt|swift|c|cc|cpp|h|hpp|cs|php|sh|zsh|sql|json|ya?ml|toml|xml|html?|css|scss|vue|svelte|lua|dart)$/i, FileCodeIcon],
]

export const fileIcon = (name: string) => EXT_ICONS.find(([re]) => re.test(name))?.[1] ?? FileIcon

const extLabel = (name: string) => (/\.([a-z0-9]{1,6})$/i.exec(name)?.[1] ?? '').toUpperCase()

// 进度环：一整圈是传完。缩图、刚开始传时是 0
function Ring({ value }: { value: number }) {
  const r = 9
  const c = 2 * Math.PI * r
  return (
    <svg viewBox="0 0 24 24" className="size-6 -rotate-90" aria-hidden>
      <circle cx="12" cy="12" r={r} fill="none" stroke="currentColor" strokeOpacity={0.25} strokeWidth={2.5} />
      <circle
        cx="12"
        cy="12"
        r={r}
        fill="none"
        stroke="currentColor"
        strokeWidth={2.5}
        strokeLinecap="round"
        strokeDasharray={c}
        strokeDashoffset={c * (1 - Math.max(0.04, value))}
        className="transition-[stroke-dashoffset] duration-200"
      />
    </svg>
  )
}

const removeBtn =
  'absolute top-1 right-1 grid size-5 place-items-center rounded-full border bg-background/90 text-muted-foreground shadow-xs transition-opacity hover:text-foreground focus-visible:opacity-100 sm:opacity-0 sm:group-hover:opacity-100 [@media(pointer:coarse)]:opacity-100'

// 输入框里的附件一排
export function AttachmentTray({ att }: { att: Attachments }) {
  if (!att.items.length) return null
  return (
    <div className="flex flex-wrap gap-2 px-2.5 pt-2.5">
      {att.items.map((a) => {
        const pct = Math.round(a.progress * 100)
        const state = a.status === 'uploading' ? `上传中 ${pct}%` : a.status === 'error' ? `上传失败：${a.error}` : fileSize(a.file.size)
        const remove = (
          <button type="button" onClick={() => att.remove(a.key)} className={removeBtn} aria-label={`去掉 ${a.file.name}`}>
            <XIcon className="size-3" />
          </button>
        )
        const retry = a.status === 'error' && (
          <button
            type="button"
            onClick={() => att.retry(a.key)}
            className="flex items-center gap-1 font-medium text-destructive hover:underline"
            aria-label={`重新上传 ${a.file.name}`}
          >
            <RotateCcwIcon className="size-3" /> 重试
          </button>
        )
        if (a.thumb)
          return (
            <div
              key={a.key}
              title={`${a.file.name} · ${state}`}
              className={cn('group relative size-16 shrink-0 overflow-hidden rounded-lg border bg-muted animate-in fade-in-0 zoom-in-95', a.status === 'error' && 'border-destructive/60')}
            >
              <img src={a.thumb} alt={a.file.name} className="size-full object-cover" />
              {a.status === 'uploading' && (
                <div className="absolute inset-0 grid place-items-center bg-background/55 text-primary">
                  <Ring value={a.progress} />
                </div>
              )}
              {a.status === 'error' && (
                <div className="absolute inset-0 grid place-items-center bg-background/75 text-xs">{retry}</div>
              )}
              {remove}
            </div>
          )
        const Icon = fileIcon(a.file.name)
        return (
          <div
            key={a.key}
            title={`${a.file.name} · ${state}`}
            className={cn(
              'group relative flex h-16 w-56 max-w-full shrink-0 items-center gap-2.5 overflow-hidden rounded-lg border bg-muted/40 pr-7 pl-2.5 animate-in fade-in-0 zoom-in-95',
              a.status === 'error' && 'border-destructive/60',
            )}
          >
            <div className="grid size-9 shrink-0 place-items-center rounded-md border bg-background text-muted-foreground">
              <Icon className="size-4.5" />
            </div>
            <div className="min-w-0 flex-1">
              <div className="truncate text-xs font-medium">{a.file.name}</div>
              <div className="mt-0.5 truncate text-2xs text-muted-foreground">
                {a.status === 'error' ? (
                  <span className="flex items-center gap-1.5 text-destructive">
                    上传失败 · {retry}
                  </span>
                ) : (
                  [extLabel(a.file.name), a.status === 'uploading' ? `${pct}%` : fileSize(a.file.size)].filter(Boolean).join(' · ')
                )}
              </div>
            </div>
            {a.status === 'uploading' && (
              <div className="absolute inset-x-0 bottom-0 h-0.5 bg-primary/15">
                <div className="h-full bg-primary transition-[width] duration-200" style={{ width: `${Math.max(4, pct)}%` }} />
              </div>
            )}
            {remove}
          </div>
        )
      })}
    </div>
  )
}

// 拖文件进窗口时盖一层，松手就加到输入框里。整个窗口都能放
export function useFileDrop(onFiles: (files: File[]) => void) {
  const [dragging, setDragging] = useState(false)
  const cb = useRef(onFiles)
  cb.current = onFiles
  useEffect(() => {
    let depth = 0
    const hasFiles = (e: DragEvent) => !!e.dataTransfer?.types.includes('Files')
    const enter = (e: DragEvent) => {
      if (!hasFiles(e)) return
      e.preventDefault()
      depth++
      setDragging(true)
    }
    const over = (e: DragEvent) => {
      if (!hasFiles(e)) return
      e.preventDefault() // 不拦的话浏览器会直接打开这个文件，页面就没了
      e.dataTransfer!.dropEffect = 'copy'
    }
    const leave = (e: DragEvent) => {
      if (!hasFiles(e)) return
      depth = Math.max(0, depth - 1)
      if (!depth) setDragging(false)
    }
    const drop = (e: DragEvent) => {
      if (!hasFiles(e)) return
      e.preventDefault()
      depth = 0
      setDragging(false)
      const dt = e.dataTransfer!
      // 文件夹也会出现在 files 里（大小 0、读不出来），挑出来提示
      const dirs = new Set<number>()
      Array.from(dt.items).forEach((it, i) => it.webkitGetAsEntry?.()?.isDirectory && dirs.add(i))
      if (dirs.size) toast.error('文件夹传不了，先打个压缩包再拖进来')
      const files = Array.from(dt.files).filter((_, i) => !dirs.has(i))
      if (files.length) cb.current(files)
    }
    // 拖到一半按 Esc、拖出浏览器：有的浏览器不给最后那个 dragleave
    const reset = () => {
      depth = 0
      setDragging(false)
    }
    window.addEventListener('dragenter', enter)
    window.addEventListener('dragover', over)
    window.addEventListener('dragleave', leave)
    window.addEventListener('drop', drop)
    window.addEventListener('dragend', reset)
    return () => {
      window.removeEventListener('dragenter', enter)
      window.removeEventListener('dragover', over)
      window.removeEventListener('dragleave', leave)
      window.removeEventListener('drop', drop)
      window.removeEventListener('dragend', reset)
    }
  }, [])
  return dragging
}

export function DropOverlay({ agent }: { agent: Agent }) {
  const who = agentLabel[agent]
  return (
    <div className="pointer-events-none fixed inset-0 z-50 bg-background/70 p-3 backdrop-blur-[2px] animate-in fade-in-0 duration-150">
      <div className="grid size-full place-items-center rounded-2xl border-2 border-dashed border-primary/60 bg-primary/[0.03]">
        <div className="grid justify-items-center gap-3 text-center">
          <div className="grid size-12 place-items-center rounded-full bg-primary/10 text-primary">
            <PaperclipIcon className="size-5.5" />
          </div>
          <div className="text-base font-medium">松开，加到消息里</div>
          <div className="max-w-sm text-sm text-balance text-muted-foreground">
            {agent === 'grok' ? `${who} 看不了图片，所有文件都给它服务器上的路径` : `图片直接给 ${who} 看，其它文件给它服务器上的路径`}
          </div>
        </div>
      </div>
    </div>
  )
}

// ---- 对话里显示一条消息带的附件 ----

// images：会话记录里这条消息自带的图（base64），和标了 (image) 的附件一一对应；没有就按 id 去取
export function AttachmentList({ files, images = [] }: { files: AttachmentRef[]; images?: string[] }) {
  let n = 0
  return (
    <div className="flex max-w-[85%] flex-wrap items-end justify-end gap-2">
      {files.map((f, i) => {
        const src = f.image ? images[n++] || (f.id && previewURL(f.id)) : undefined
        return src ? <Thumb key={i} f={f} src={src} /> : <FileChip key={i} f={f} />
      })}
    </div>
  )
}

function Thumb({ f, src }: { f: AttachmentRef; src: string }) {
  const [broken, setBroken] = useState(false)
  if (broken) return <FileChip f={f} />
  const img = <img src={src} alt={f.name} loading="lazy" onError={() => setBroken(true)} className="max-h-40 max-w-64 rounded-xl border bg-muted object-contain" />
  return f.id ? (
    <a href={uploadURL(f.id)} target="_blank" rel="noopener" title={f.path} className="rounded-xl focus-visible:ring-2 focus-visible:ring-ring">
      {img}
    </a>
  ) : (
    <span title={f.path}>{img}</span>
  )
}

function FileChip({ f }: { f: AttachmentRef }) {
  const Icon = fileIcon(f.name)
  const body = (
    <>
      <Icon className="size-4 shrink-0 text-muted-foreground" />
      <span className="truncate">{f.name}</span>
    </>
  )
  const cls = 'flex max-w-64 items-center gap-2 rounded-lg border bg-card px-2.5 py-1.5 text-xs'
  // 还在上传目录里的能取回原文件（图片、纯文本在新标签页里看，别的下载）；清掉了就只显示名字
  return f.id ? (
    <a href={uploadURL(f.id)} target="_blank" rel="noopener" title={f.path} className={cn(cls, 'hover:bg-muted')}>
      {body}
    </a>
  ) : (
    <span title={f.path} className={cls}>
      {body}
    </span>
  )
}
