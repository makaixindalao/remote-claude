import { useState, type ReactNode } from 'react'
import {
  BookOpenIcon,
  CheckCircle2Icon,
  CircleDashedIcon,
  CircleIcon,
  FilePenIcon,
  FilePlusIcon,
  FileTextIcon,
  FolderOpenIcon,
  GlobeIcon,
  ListChecksIcon,
  MessageCircleQuestionIcon,
  SearchIcon,
  SparklesIcon,
  TerminalIcon,
  UsersIcon,
  WrenchIcon,
} from 'lucide-react'
import { DiffView } from '@/components/diff-view'
import { Markdown } from '@/components/markdown'
import type { FileChange } from '@/lib/api'
import { openFile } from '@/lib/file-panel'
import { shortPath } from '@/lib/format'
import { cn } from '@/lib/utils'

// 工具调用的展示：一行摘要（卡片标题）+ 展开后的参数视图。权限确认卡片也复用这里。

type Input = Record<string, unknown>
const str = (v: unknown) => (typeof v === 'string' ? v : v == null ? '' : JSON.stringify(v))

export function toolIcon(name: string) {
  const cls = 'size-3.5 shrink-0'
  switch (name) {
    case 'Bash':
    case 'BashOutput':
    case 'KillShell':
      return <TerminalIcon className={cls} />
    case 'Read':
      return <FileTextIcon className={cls} />
    case 'Write':
      return <FilePlusIcon className={cls} />
    case 'Edit':
    case 'MultiEdit':
    case 'NotebookEdit':
      return <FilePenIcon className={cls} />
    case 'Grep':
    case 'Glob':
      return <SearchIcon className={cls} />
    case 'WebFetch':
    case 'WebSearch':
      return <GlobeIcon className={cls} />
    case 'TodoWrite':
      return <ListChecksIcon className={cls} />
    case 'Task':
    case 'Agent':
      return <UsersIcon className={cls} />
    case 'AskUserQuestion':
      return <MessageCircleQuestionIcon className={cls} />
    case 'ExitPlanMode':
      return <BookOpenIcon className={cls} />
    case 'Skill':
      return <SparklesIcon className={cls} />
    case 'Patch':
      return <FilePenIcon className={cls} />
    case 'LS':
      return <FolderOpenIcon className={cls} />
  }
  return <WrenchIcon className={cls} />
}

export function toolSummary(name: string, input: Input = {}, cwd?: string): string {
  switch (name) {
    case 'Bash':
      return str(input.command)
    case 'Read':
    case 'Write':
    case 'Edit':
    case 'MultiEdit':
      return shortPath(str(input.file_path), cwd)
    case 'NotebookEdit':
      return shortPath(str(input.notebook_path), cwd)
    case 'Grep':
      return [str(input.pattern), input.path ? shortPath(str(input.path), cwd) : ''].filter(Boolean).join('  ')
    case 'Glob':
      return str(input.pattern)
    case 'WebFetch':
      return str(input.url)
    case 'WebSearch':
      return str(input.query)
    case 'Task':
    case 'Agent':
      return str(input.description || input.subagent_type)
    case 'TodoWrite':
      return `${Array.isArray(input.todos) ? input.todos.length : 0} 项`
    case 'Skill':
      return str(input.skill || input.command)
    case 'AskUserQuestion':
      return Array.isArray(input.questions) ? str((input.questions[0] as Input)?.question) : ''
    case 'ExitPlanMode':
      return '计划'
    case 'Patch':
      return patchChanges(input)
        .map((c) => shortPath(c.path, cwd))
        .join('  ')
    case 'LS':
      return shortPath(str(input.path), cwd)
  }
  const first = Object.values(input).find((v) => typeof v === 'string')
  return str(first)
}

// full：全展开，长内容不截断
const FILE_TOOLS = ['Read', 'Write', 'Edit', 'MultiEdit', 'NotebookEdit']

// 读写文件的工具，参数上方多一行可点的路径：点了在右侧打开这个文件（Read 带 offset 就跳到那一行）。
// files 是服务端给的 diff（带行号、改动上下各 3 行），有就用它显示改动，没有就按参数前后对比
export function ToolInput({ name, input = {}, full, files }: { name: string; input?: Input; full?: boolean; files?: FileChange[] }) {
  const path = FILE_TOOLS.includes(name) ? str(input.file_path || input.notebook_path) : ''
  const diff = hasHunks(files) && ['Edit', 'MultiEdit', 'Write', 'Patch'].includes(name)
  if (diff && name === 'Patch') return <FileDiffs files={files!} full={full} />
  if (!path) return <ToolInputBody name={name} input={input} full={full} />
  const line = typeof input.offset === 'number' && input.offset > 0 ? input.offset : undefined
  return (
    <div className="space-y-1.5">
      <button
        type="button"
        className="file-link-btn max-w-full truncate text-left font-mono text-xs"
        onClick={() => openFile(line ? `${path}:${line}` : path)}
      >
        {path}
        {line ? `:${line}` : ''}
      </button>
      {diff ? <FileDiffs files={files!} full={full} /> : name !== 'Read' && <ToolInputBody name={name} input={input} full={full} />}
    </div>
  )
}

export const hasHunks = (files?: FileChange[]) => !!files?.some((f) => f.hunks?.length)

function FileDiffs({ files, full }: { files: FileChange[]; full?: boolean }) {
  return (
    <div className={cn('overflow-auto rounded-md border', full ? 'max-h-none' : 'max-h-[28rem]')}>
      {files.map((f) => (
        <DiffView key={f.path} hunks={f.hunks ?? []} path={f.path} />
      ))}
      {files.some((f) => f.truncated) && <p className="px-3 py-1.5 text-xs text-muted-foreground">改动太长，只显示了前面一部分。</p>}
    </div>
  )
}

function ToolInputBody({ name, input = {}, full }: { name: string; input?: Input; full?: boolean }) {
  const long = full ? 'max-h-none' : undefined
  switch (name) {
    case 'Bash':
      return (
        <div className="space-y-1">
          {!!input.description && <p className="text-xs text-muted-foreground">{str(input.description)}</p>}
          <Pre className={cn('text-foreground', long)}>
            <span className="text-muted-foreground select-none">$ </span>
            {str(input.command)}
          </Pre>
        </div>
      )
    case 'Edit':
      return <Diff before={str(input.old_string)} after={str(input.new_string)} full={full} />
    case 'MultiEdit':
      return (
        <div className="space-y-2">
          {(Array.isArray(input.edits) ? input.edits : []).map((e, i) => (
            <Diff key={i} before={str((e as Input).old_string)} after={str((e as Input).new_string)} full={full} />
          ))}
        </div>
      )
    case 'Write':
      return (
        <Pre clamp={full ? 2000 : 40} className={long}>
          {str(input.content)}
        </Pre>
      )
    case 'Patch':
      return (
        <div className="space-y-2">
          {patchChanges(input).map((c, i) => (
            <div key={i} className="space-y-1">
              <div className="flex items-center gap-2 text-xs">
                <span className="text-muted-foreground">{{ add: '新建', delete: '删除' }[c.kind] ?? '修改'}</span>
                <button type="button" className="file-link-btn min-w-0 truncate text-left font-mono" onClick={() => openFile(c.path)}>
                  {c.path}
                </button>
              </div>
              {c.diff && <UnifiedDiff diff={c.diff} kind={c.kind} full={full} />}
            </div>
          ))}
        </div>
      )
    case 'TodoWrite':
      return <Todos todos={Array.isArray(input.todos) ? (input.todos as Input[]) : []} />
    case 'ExitPlanMode':
      return <Markdown text={str(input.plan)} className="text-sm" />
    case 'AskUserQuestion':
      return (
        <div className="space-y-2 text-sm">
          {(Array.isArray(input.questions) ? (input.questions as Input[]) : []).map((q, i) => (
            <div key={i}>
              <div className="font-medium">{str(q.question)}</div>
              <ul className="mt-1 list-disc pl-5 text-muted-foreground">
                {(Array.isArray(q.options) ? (q.options as Input[]) : []).map((o, j) => (
                  <li key={j}>{str(o.label)}</li>
                ))}
              </ul>
            </div>
          ))}
        </div>
      )
  }
  return (
    <Pre clamp={full ? 2000 : 30} className={long}>
      {JSON.stringify(input, null, 2)}
    </Pre>
  )
}

// 结果文本：太长先收起，点一下看全部
export function ToolResult({ text, isError, full }: { text: string; isError?: boolean; full?: boolean }) {
  if (!text.trim()) return <p className="text-xs text-muted-foreground">（无输出）</p>
  return (
    <Pre clamp={full ? 2000 : 12} className={cn(isError && 'text-destructive', full && 'max-h-none')}>
      {text}
    </Pre>
  )
}

export function Pre({ children, clamp, className }: { children: ReactNode; clamp?: number; className?: string }) {
  const [all, setAll] = useState(false)
  const lines = typeof children === 'string' ? children.split('\n') : null
  const cut = !!clamp && !!lines && lines.length > clamp && !all
  return (
    <div>
      <pre
        className={cn(
          'max-h-[28rem] overflow-auto rounded-md bg-muted/60 px-3 py-2 font-mono text-xs leading-relaxed whitespace-pre-wrap break-words text-muted-foreground',
          className,
        )}
      >
        {cut ? lines!.slice(0, clamp).join('\n') : children}
      </pre>
      {cut && (
        <button className="mt-1 text-xs text-muted-foreground underline-offset-2 hover:underline" onClick={() => setAll(true)}>
          还有 {lines!.length - clamp!} 行，展开
        </button>
      )}
    </div>
  )
}

// 只做首尾公共行的裁剪：中间改动的部分整段标红标绿，够看清改了什么
export function Diff({ before, after, full }: { before: string; after: string; full?: boolean }) {
  const a = before.split('\n')
  const b = after.split('\n')
  let head = 0
  while (head < a.length && head < b.length && a[head] === b[head]) head++
  let tail = 0
  while (tail < a.length - head && tail < b.length - head && a[a.length - 1 - tail] === b[b.length - 1 - tail]) tail++
  const ctx = (lines: string[]) => lines.slice(-3)
  const rows: { k: ' ' | '-' | '+'; t: string }[] = [
    ...ctx(a.slice(0, head)).map((t) => ({ k: ' ' as const, t })),
    ...a.slice(head, a.length - tail).map((t) => ({ k: '-' as const, t })),
    ...b.slice(head, b.length - tail).map((t) => ({ k: '+' as const, t })),
    ...a
      .slice(a.length - tail)
      .slice(0, 3)
      .map((t) => ({ k: ' ' as const, t })),
  ]
  return (
    <div className={cn('overflow-auto rounded-md border font-mono text-xs leading-relaxed', full ? 'max-h-none' : 'max-h-[28rem]')}>
      {rows.map((r, i) => (
        <div
          key={i}
          className={cn(
            'px-2 whitespace-pre-wrap break-words',
            r.k === '-' && 'bg-red-500/10 text-red-700 dark:text-red-300',
            r.k === '+' && 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-300',
            r.k === ' ' && 'text-muted-foreground',
          )}
        >
          <span className="mr-2 select-none opacity-60">{r.k}</span>
          {r.t || ' '}
        </div>
      ))}
    </div>
  )
}

// codex 改文件（Patch）的一项：新建 / 删除给的是整个文件内容，修改给的是统一 diff
type PatchChange = { path: string; kind: string; diff: string }

function patchChanges(input: Input): PatchChange[] {
  return Array.isArray(input.changes) ? (input.changes as PatchChange[]) : []
}

export function UnifiedDiff({ diff, kind, full }: { diff: string; kind?: string; full?: boolean }) {
  const isDiff = /^(@@|--- |\+\+\+ )/m.test(diff)
  const lines = diff.replace(/\n$/, '').split('\n')
  return (
    <div className={cn('overflow-auto rounded-md border font-mono text-xs leading-relaxed', full ? 'max-h-none' : 'max-h-[28rem]')}>
      {lines.map((line, i) => {
        // 不是 diff 格式的就是整个文件：新建全算加的，删除全算删的
        const k = isDiff ? (line.startsWith('@@') ? '@' : line[0]) : kind === 'delete' ? '-' : '+'
        const text = isDiff && k !== '@' ? line.slice(1) : line
        return (
          <div
            key={i}
            className={cn(
              'px-2 whitespace-pre-wrap break-words',
              k === '-' && 'bg-red-500/10 text-red-700 dark:text-red-300',
              k === '+' && 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-300',
              k === '@' && 'bg-muted/60 text-muted-foreground',
              k !== '-' && k !== '+' && k !== '@' && 'text-muted-foreground',
            )}
          >
            {k !== '@' && <span className="mr-2 select-none opacity-60">{k === '+' || k === '-' ? k : ' '}</span>}
            {text || ' '}
          </div>
        )
      })}
    </div>
  )
}

function Todos({ todos }: { todos: Input[] }) {
  return (
    <ul className="space-y-1 text-sm">
      {todos.map((t, i) => (
        <li key={i} className="flex items-start gap-2">
          {t.status === 'completed' ? (
            <CheckCircle2Icon className="mt-0.5 size-4 shrink-0 text-emerald-500" />
          ) : t.status === 'in_progress' ? (
            <CircleDashedIcon className="mt-0.5 size-4 shrink-0 animate-spin text-sky-500 [animation-duration:3s]" />
          ) : (
            <CircleIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
          )}
          <span className={cn(t.status === 'completed' && 'text-muted-foreground line-through')}>
            {str(t.status === 'in_progress' && t.activeForm ? t.activeForm : t.content)}
          </span>
        </li>
      ))}
    </ul>
  )
}
