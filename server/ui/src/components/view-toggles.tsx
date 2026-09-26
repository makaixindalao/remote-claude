import { BrainIcon, FoldVerticalIcon, ListIcon, PanelRightIcon, UnfoldVerticalIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuLabel, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { closePanel } from '@/lib/file-panel'
import { setView, useView, type ToolMode } from '@/lib/view'
import { cn } from '@/lib/utils'

const TOOL_MODES: ToolMode[] = ['merged', 'compact', 'expanded']
const TOOL_LABEL: Record<ToolMode, string> = { merged: '合并', compact: '半开', expanded: '全开' }
const TOOL_HINT: Record<ToolMode, string> = {
  merged: '连续的调用收成一行摘要',
  compact: '摘要展开，一个调用一行',
  expanded: '逐个展开，输出全部显示',
}
const TOOL_ICON = { merged: FoldVerticalIcon, compact: ListIcon, expanded: UnfoldVerticalIcon }

// 开着的染成主题色、带一圈描边，关着的是灰的：以前开了只多一层浅灰底，看不出开没开
const ON = 'bg-primary/10 text-primary ring-1 ring-primary/40 ring-inset hover:bg-primary/15 hover:text-primary dark:bg-primary/20 dark:hover:bg-primary/25'
const OFF = 'text-muted-foreground'

// 页面右上角的三个开关：思考过程、工具调用（合并 / 半开 / 全开，下拉里选）、右侧终端
export function ViewToggles({ terminal = true }: { terminal?: boolean }) {
  const v = useView()
  const ToolIcon = TOOL_ICON[v.tools]
  return (
    <>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            variant="ghost"
            size="icon-sm"
            className={v.thinking ? ON : OFF}
            aria-label="展开思考过程"
            aria-pressed={v.thinking}
            onClick={() => setView({ thinking: !v.thinking })}
          >
            <BrainIcon />
          </Button>
        </TooltipTrigger>
        <TooltipContent>{v.thinking ? '收起思考过程' : '展开思考过程'}</TooltipContent>
      </Tooltip>
      <DropdownMenu>
        <Tooltip>
          <TooltipTrigger asChild>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="sm" className={cn('gap-1 px-2', v.tools === 'merged' ? OFF : ON)} aria-label={`工具调用：${TOOL_LABEL[v.tools]}`}>
                <ToolIcon />
                <span className="hidden text-xs lg:inline">{TOOL_LABEL[v.tools]}</span>
              </Button>
            </DropdownMenuTrigger>
          </TooltipTrigger>
          <TooltipContent>工具调用怎么显示</TooltipContent>
        </Tooltip>
        <DropdownMenuContent align="end" className="w-56">
          <DropdownMenuLabel>工具调用</DropdownMenuLabel>
          <DropdownMenuRadioGroup value={v.tools} onValueChange={(t) => setView({ tools: t as ToolMode })}>
            {TOOL_MODES.map((m) => {
              const Icon = TOOL_ICON[m]
              return (
                <DropdownMenuRadioItem key={m} value={m}>
                  <Icon />
                  <span className="grid">
                    <span>{TOOL_LABEL[m]}</span>
                    <span className="text-xs text-muted-foreground">{TOOL_HINT[m]}</span>
                  </span>
                </DropdownMenuRadioItem>
              )
            })}
          </DropdownMenuRadioGroup>
        </DropdownMenuContent>
      </DropdownMenu>
      {terminal && (
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              variant="ghost"
              size="icon-sm"
              className={v.terminal ? ON : OFF}
              aria-label="右侧终端"
              aria-pressed={v.terminal}
              onClick={() => {
                closePanel() // 右半屏只有一块：切终端时把打开的文件 / 改动收起来
                setView({ terminal: !v.terminal })
              }}
            >
              {/* 用面板图标：终端图标留给「终端里继续」这种跳到终端页的操作 */}
              <PanelRightIcon />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{v.terminal ? '关闭终端' : '在右侧打开终端'}</TooltipContent>
        </Tooltip>
      )}
    </>
  )
}
