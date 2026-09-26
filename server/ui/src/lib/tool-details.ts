import { api, type ToolDetail } from '@/lib/api'

// 精简过的工具卡片（block.lazy）展开时按 id 取参数和输出的全文。
// 同一轮渲染里要的合成一个请求（「逐个展开」模式下一页的卡片会一起要），取过的记住。
export type ToolDetails = { load: (id: string) => Promise<ToolDetail | undefined> }

// base：带好了问号和前面参数的地址，比如 /api/chats/<id>/tools? 或 /api/session/tools?project=…&id=…&
export function toolDetails(base: string): ToolDetails {
  const cache = new Map<string, Promise<ToolDetail | undefined>>()
  let queue = new Map<string, (d: ToolDetail | undefined) => void>()
  let timer: ReturnType<typeof setTimeout> | undefined

  function flush() {
    const batch = queue
    queue = new Map()
    timer = undefined
    const ids = [...batch.keys()]
    for (let i = 0; i < ids.length; i += 100) {
      const chunk = ids.slice(i, i + 100)
      api<{ tools: Record<string, ToolDetail> }>(`${base}ids=${chunk.map(encodeURIComponent).join(',')}`)
        .then((r) => chunk.forEach((id) => batch.get(id)!(r.tools[id])))
        .catch(() =>
          chunk.forEach((id) => {
            cache.delete(id) // 下次展开再试
            batch.get(id)!(undefined)
          }),
        )
    }
  }

  return {
    load(id) {
      let p = cache.get(id)
      if (!p) {
        p = new Promise((resolve) => queue.set(id, resolve))
        cache.set(id, p)
        timer ??= setTimeout(flush)
      }
      return p
    },
  }
}
