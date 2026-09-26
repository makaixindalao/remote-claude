import { DiffPanel } from '@/components/diff-panel'
import { FileViewer } from '@/components/file-viewer'
import { SideTerminal } from '@/components/side-terminal'
import { usePanel } from '@/lib/file-panel'
import { useView } from '@/lib/view'

// 对话页 / 会话页的右半屏：点了文件名就看文件、点了改动卡片就看 diff，否则开着终端开关就是终端。手机上占满整屏
export function SidePanel({ cwd, project }: { cwd?: string; project: string }) {
  const panel = usePanel()
  const view = useView()
  if (panel) {
    return (
      <div className="flex min-h-0 w-full flex-col border-l md:w-1/2">
        {panel.kind === 'file' ? (
          <FileViewer key={panel.path} path={panel.path} line={panel.line} cwd={cwd} project={project} />
        ) : (
          <DiffPanel key={panel.seq} target={panel} cwd={cwd} project={project} />
        )}
      </div>
    )
  }
  if (view.terminal && cwd) return <SideTerminal cwd={cwd} project={project} />
  return null
}

// 页面据此决定手机上要不要把对话先藏起来
export function useSidePanelOpen(hasCwd: boolean) {
  const panel = usePanel()
  const view = useView()
  return !!panel || (view.terminal && hasCwd)
}
