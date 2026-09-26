// 够用的 Markdown：代码块、标题、列表、引用、表格、行内代码 / 粗体 / 斜体 / 链接。
// 先整体转义再加标签，任何输入都不会变成可执行的 HTML，所以可以放心 dangerouslySetInnerHTML。
//
// 额外做三件事（交互在 components/markdown.tsx 里接）：
//   - 代码块包成卡片，带语言名和复制按钮；```mermaid 留一个占位，渲染成流程图
//   - 看着像文件路径的（`a/b.go`、b.go:42、表格里的 service/x.go）变成可点的链接，点了在右侧打开
//   - 裸 URL 自动成链接

const escMap: Record<string, string> = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }
const esc = (s: string) => s.replace(/[&<>"']/g, (c) => escMap[c])

// 认得这些扩展名才当文件：宁可漏，别把「e.g.」「v1.5」这种当成文件
const EXT =
  'go|mod|sum|ts|tsx|js|jsx|mjs|cjs|vue|svelte|py|rb|rs|java|kt|kts|swift|c|h|cc|cpp|hpp|m|cs|php|lua|sh|bash|zsh|fish|ps1|' +
  'md|mdx|txt|json|jsonl|yaml|yml|toml|ini|conf|cfg|env|example|lock|xml|html|htm|css|scss|less|sql|proto|graphql|gradle|' +
  'dockerfile|makefile|plist|service|ignore|tf|nix|dart|ex|exs|erl|hs|scala|clj|r|jl|zig|ipynb'
const pathRe = new RegExp(`^(?:~|\\.{1,2})?/?(?:[\\w@.+-]+/)*[\\w@+-][\\w@.+-]*\\.(?:${EXT})(?::\\d+(?:-\\d+)?)?$`, 'i')
const barePathRe = new RegExp(
  `(^|[\\s(（「“'"：:，,])((?:~|\\.{1,2})?/?(?:[\\w@.+-]+/)+[\\w@+-][\\w@.+-]*\\.(?:${EXT})(?::\\d+(?:-\\d+)?)?)(?=$|[\\s)）」”'"，,。；;:!?！？])`,
  'gi',
)

export const isFilePath = (s: string) => pathRe.test(s)

const fileLink = (path: string, inner: string) => `<a class="file-link" data-path="${esc(path)}" role="button" tabindex="0">${inner}</a>`

function inline(src: string) {
  const held: string[] = []
  const hold = (html: string) => `\u0000${held.push(html) - 1}\u0000`

  let s = src.replace(/`([^`\n]+)`/g, (_, c: string) => hold(isFilePath(c) ? `<code>${fileLink(c, esc(c))}</code>` : `<code>${esc(c)}</code>`))
  s = s.replace(/\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\)/g, (_, t: string, u: string) =>
    hold(`<a href="${esc(u)}" target="_blank" rel="noopener noreferrer">${esc(t)}</a>`),
  )
  s = s.replace(/https?:\/\/[^\s<>"'`，。）)]+/g, (u) => hold(`<a href="${esc(u)}" target="_blank" rel="noopener noreferrer">${esc(u)}</a>`))
  // 裸路径至少要带一个 /，免得把句子里的「README.md 很长」之外的词也当成文件
  s = s.replace(barePathRe, (_, pre: string, p: string) => pre + hold(fileLink(p, esc(p))))

  s = esc(s)
  s = s.replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>')
  s = s.replace(/(^|[^*\w])\*([^*\n]+)\*(?!\w)/g, '$1<em>$2</em>')
  s = s.replace(/~~([^~\n]+)~~/g, '<del>$1</del>')
  return s.replace(/\u0000(\d+)\u0000/g, (_, i: string) => held[+i])
}

function codeBlock(lang: string, body: string) {
  if (lang.toLowerCase() === 'mermaid') {
    // 先把源码显示出来，markdown.tsx 按需加载 mermaid 后换成图
    return `<div class="mermaid-block" data-src="${esc(body)}"><pre><code>${esc(body)}</code></pre></div>`
  }
  const langAttr = lang ? ` class="language-${esc(lang.toLowerCase())}"` : ''
  return (
    `<div class="code-block"><div class="code-head"><span>${esc(lang || 'text')}</span>` +
    `<button type="button" class="code-copy" aria-label="复制代码">复制</button></div>` +
    `<pre><code${langAttr}>${esc(body)}</code></pre></div>`
  )
}

const isTableSep = (l: string) => /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/.test(l)
const cells = (l: string) =>
  l
    .trim()
    .replace(/^\||\|$/g, '')
    .split('|')
    .map((c) => c.trim())
const listRe = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/
const hrRe = /^\s*([-*_])(\s*\1){2,}\s*$/
const blockStart = (l: string) => /^\s*(```|~~~|#{1,6}\s|>|[-*+]\s|\d+[.)]\s|\|)/.test(l) || hrRe.test(l)

export function md(src: string): string {
  const lines = String(src ?? '')
    .replace(/\r\n?/g, '\n')
    .split('\n')
  const out: string[] = []
  let i = 0
  while (i < lines.length) {
    const line = lines[i]
    let m: RegExpMatchArray | null
    if ((m = line.match(/^\s*(```|~~~)\s*([\w+#.-]*)/))) {
      const fence = m[1]
      const body: string[] = []
      i++
      while (i < lines.length && !lines[i].trim().startsWith(fence)) body.push(lines[i++])
      i++
      out.push(codeBlock(m[2], body.join('\n')))
      continue
    }
    if ((m = line.match(/^(#{1,6})\s+(.*)$/))) {
      const n = Math.min(m[1].length + 1, 6) // 消息里的标题降一级，别比页面标题还大
      out.push(`<h${n}>${inline(m[2])}</h${n}>`)
      i++
      continue
    }
    if (hrRe.test(line)) {
      out.push('<hr>')
      i++
      continue
    }
    if (line.trim().startsWith('|') && i + 1 < lines.length && isTableSep(lines[i + 1])) {
      const head = cells(line)
      i += 2
      const rows: string[][] = []
      while (i < lines.length && lines[i].trim().startsWith('|')) rows.push(cells(lines[i++]))
      out.push(
        '<div class="table-wrap"><table><thead><tr>' +
          head.map((c) => `<th>${inline(c)}</th>`).join('') +
          '</tr></thead><tbody>' +
          rows.map((r) => '<tr>' + r.map((c) => `<td>${inline(c)}</td>`).join('') + '</tr>').join('') +
          '</tbody></table></div>',
      )
      continue
    }
    if (/^\s*>/.test(line)) {
      const body: string[] = []
      while (i < lines.length && /^\s*>/.test(lines[i])) body.push(lines[i++].replace(/^\s*>\s?/, ''))
      out.push(`<blockquote>${md(body.join('\n'))}</blockquote>`)
      continue
    }
    if ((m = line.match(listRe))) {
      const ordered = /\d/.test(m[2])
      const items: { depth: number; text: string }[] = []
      while (i < lines.length) {
        const lm = lines[i].match(listRe)
        if (lm) {
          items.push({ depth: Math.floor(lm[1].replace(/\t/g, '  ').length / 2), text: lm[3] })
        } else if (lines[i].trim() && /^\s{2,}/.test(lines[i]) && items.length) {
          items[items.length - 1].text += ' ' + lines[i].trim() // 列表项的续行
        } else break
        i++
      }
      const tag = ordered ? 'ol' : 'ul'
      out.push(
        `<${tag}>` +
          items.map((it) => `<li${it.depth ? ` style="margin-left:${it.depth * 1.2}em"` : ''}>${inline(it.text)}</li>`).join('') +
          `</${tag}>`,
      )
      continue
    }
    if (!line.trim()) {
      i++
      continue
    }
    const para: string[] = []
    while (i < lines.length && lines[i].trim() && !(para.length && blockStart(lines[i]))) para.push(lines[i++])
    out.push(`<p>${para.map(inline).join('<br>')}</p>`)
  }
  return out.join('')
}
