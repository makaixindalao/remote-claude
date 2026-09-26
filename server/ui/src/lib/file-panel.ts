import { useSyncExternalStore } from 'react'
import type { ToolDetails } from '@/lib/tool-details'

// 右侧面板里打开的东西：一个文件，或者改动（diff）。消息里的文件名、工具卡片里的路径点一下就调 openFile，
// 每轮末尾的改动卡片点一下调 openDiff。面板在对话页 / 会话页里按那一页的工作目录去解析相对路径

// 一轮对话里改过的一个文件：路径是工具给的（一般是绝对路径），行数是这一轮里几次改动加起来的
export type TurnChange = { path: string; add: number; del: number; status?: string; tools: string[] }

export type DiffTarget = {
  turn: TurnChange[] // 这一轮改了的文件
  focus?: string // 点的是哪个文件：展开它、滚过去
  tools?: ToolDetails // 按工具 id 取带 hunks 的 diff
}

export type Panel = { kind: 'file'; path: string; line?: number } | ({ kind: 'diff'; seq: number } & DiffTarget) | null

let state: Panel = null
let seq = 0
const subs = new Set<() => void>()
const emit = () => subs.forEach((f) => f())

// 「a/b.go:42」「a/b.go:42-50」都认，跳到起始行
export function openFile(ref: string) {
  const m = /^(.*?)(?::(\d+)(?:-\d+)?)?$/.exec(ref.trim())
  state = { kind: 'file', path: m?.[1] || ref, line: m?.[2] ? Number(m[2]) : undefined }
  emit()
}

// seq 每次都变：再点一次同一张卡片也会重新拉工作区的 diff
export function openDiff(target: DiffTarget) {
  state = { kind: 'diff', seq: ++seq, ...target }
  emit()
}

export function closePanel() {
  if (state === null) return
  state = null
  emit()
}

export function usePanel() {
  return useSyncExternalStore(
    (cb) => {
      subs.add(cb)
      return () => subs.delete(cb)
    },
    () => state,
  )
}
