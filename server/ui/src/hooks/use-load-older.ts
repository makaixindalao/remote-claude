import { useCallback, useEffect, useLayoutEffect, useRef, useState, type RefObject } from 'react'

// 打开会话只拿了最后一页。往上翻到离顶部不远就要更早的一页，插到上面以后把滚动位置挪回原处，
// 正在看的内容不跳。内容不满一屏时也接着要，直到填满或者到头。
//
// start：当前最早那条的下标；more：前面还有没有；load(before)：要 before 之前的一页并插进去
export function useLoadOlder(
  scrollRef: RefObject<HTMLElement | null>,
  start: number | undefined,
  more: boolean,
  load: (before: number) => Promise<void>,
) {
  const [loading, setLoading] = useState(false)
  const busy = useRef(false)
  const fromBottom = useRef<number | null>(null) // 加载前视口离内容底部多远
  const loadRef = useRef(load)
  loadRef.current = load

  const trigger = useCallback(() => {
    const el = scrollRef.current
    if (!el || busy.current || !more || start === undefined) return
    busy.current = true
    setLoading(true)
    fromBottom.current = el.scrollHeight - el.scrollTop
    loadRef.current(start).finally(() => {
      busy.current = false
      setLoading(false)
    })
  }, [scrollRef, start, more])

  // 按离底部的距离恢复：浏览器自己的滚动锚定有没有生效，结果都一样
  useLayoutEffect(() => {
    const el = scrollRef.current
    if (el && fromBottom.current !== null) {
      el.scrollTop = el.scrollHeight - fromBottom.current
      fromBottom.current = null
    }
  }, [scrollRef, start])

  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    const check = () => el.scrollTop < 600 && trigger()
    check()
    el.addEventListener('scroll', check, { passive: true })
    return () => el.removeEventListener('scroll', check)
  }, [scrollRef, trigger])

  return { loading, trigger }
}
