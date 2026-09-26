import { useState } from 'react'
import { FolderIcon } from 'lucide-react'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { PageHeader } from '@/components/page-header'
import { StartComposer } from '@/components/start-composer'
import { useData } from '@/hooks/use-data'
import { agentLabel } from '@/lib/agents'
import { loadProject } from '@/lib/prefs'

export function NewChatView({ project: initial }: { project?: string }) {
  const { projects, agents } = useData()
  const usable = projects.filter((p) => p.exists)
  const [picked, setPicked] = useState(initial ?? '')
  // 没指定项目就用上次的，再不行用第一个
  const project = picked || [loadProject(), usable[0]?.name].find((p) => p && usable.some((u) => u.name === p)) || ''
  const path = usable.find((p) => p.name === project)?.path

  return (
    <>
      <PageHeader title="新对话" subtitle={path} />
      <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-4 p-6 text-center">
        <p className="text-lg font-medium">在哪个项目里干活？</p>
        <Select value={project} onValueChange={setPicked}>
          <SelectTrigger className="w-72 max-w-full">
            <FolderIcon className="size-4 text-muted-foreground" />
            <SelectValue placeholder="选择项目" />
          </SelectTrigger>
          <SelectContent position="popper">
            {usable.map((p) => (
              <SelectItem key={p.name} value={p.name}>
                {p.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className="max-w-sm text-sm text-muted-foreground">
          发出第一条消息就会在 VPS 上起一个 {agents.map((a) => agentLabel[a]).join(' / ')}
          {agents.length > 1 && '（输入框下面选）'}，页面关掉它也会继续跑。
        </p>
      </div>
      <StartComposer key={project} project={project || undefined} />
    </>
  )
}
