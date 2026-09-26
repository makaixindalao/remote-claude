// 终端的连接状态。单独放一个文件：对话页要显示它，但不该因此把 xterm 拉进首屏的包（见 side-terminal.tsx）

export type TermStatus = 'connecting' | 'open' | 'ended' | 'failed' | 'retrying'

export const termStatusLabel: Record<TermStatus, string> = {
  connecting: '连接中',
  open: '已连接',
  retrying: '重连中',
  ended: '已结束',
  failed: '连不上',
}
