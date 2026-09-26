import { memo, useEffect, useMemo, useRef, type KeyboardEvent, type MouseEvent } from 'react'
import { copyText } from '@/lib/clipboard'
import { openFile } from '@/lib/file-panel'
import { loadHljs, loadMermaid } from '@/lib/highlight'
import { md } from '@/lib/md'
import { cn } from '@/lib/utils'

// md() 先整体转义再加标签，输出不含任何来自输入的原始 HTML。
// 这里补上交互：代码块复制、文件名点开、代码高亮和 mermaid（都按需加载）。
export const Markdown = memo(function Markdown({ text, className }: { text: string; className?: string }) {
  const html = useMemo(() => md(text), [text])
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const root = ref.current
    if (root) enhance(root)
  }, [html])

  return <div ref={ref} className={cn('markdown', className)} onClick={onClick} onKeyDown={onKeyDown} dangerouslySetInnerHTML={{ __html: html }} />
})

function onClick(e: MouseEvent<HTMLDivElement>) {
  const t = e.target as HTMLElement
  const copy = t.closest<HTMLButtonElement>('.code-copy')
  if (copy) {
    const code = copy.closest('.code-block')?.querySelector('code')?.textContent ?? ''
    copyText(code).then(() => {
      copy.textContent = '已复制'
      setTimeout(() => (copy.textContent = '复制'), 1500)
    })
    return
  }
  const link = t.closest<HTMLElement>('.file-link')
  if (link?.dataset.path) {
    e.preventDefault()
    openFile(link.dataset.path)
  }
}

function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
  const link = (e.target as HTMLElement).closest<HTMLElement>('.file-link')
  if (link?.dataset.path && (e.key === 'Enter' || e.key === ' ')) {
    e.preventDefault()
    openFile(link.dataset.path)
  }
}

let mermaidSeq = 0

async function enhance(root: HTMLElement) {
  const codes = root.querySelectorAll<HTMLElement>('.code-block code[class*="language-"]:not([data-hl])')
  if (codes.length) {
    const hljs = await loadHljs()
    codes.forEach((el) => {
      const lang = /language-([\w+#.-]+)/.exec(el.className)?.[1]
      el.dataset.hl = '1'
      if (!lang || !hljs.getLanguage(lang) || (el.textContent?.length ?? 0) > 200_000) return
      el.innerHTML = hljs.highlight(el.textContent ?? '', { language: lang, ignoreIllegals: true }).value
    })
  }

  const diagrams = root.querySelectorAll<HTMLElement>('.mermaid-block:not([data-done])')
  if (diagrams.length) {
    const mermaid = await loadMermaid()
    mermaid.initialize({
      startOnLoad: false,
      securityLevel: 'strict',
      theme: document.documentElement.classList.contains('dark') ? 'dark' : 'neutral',
      fontFamily: 'inherit',
    })
    for (const el of diagrams) {
      el.dataset.done = '1'
      try {
        const { svg } = await mermaid.render(`rc-mermaid-${++mermaidSeq}`, el.dataset.src ?? '')
        el.innerHTML = svg
        el.classList.add('rendered')
      } catch {
        // 语法不对就留着源码，底下提示一句
        el.insertAdjacentHTML('beforeend', '<p class="mermaid-error">流程图语法有误，显示原文</p>')
      }
    }
  }
}
