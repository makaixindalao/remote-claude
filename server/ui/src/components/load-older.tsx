import { Loader2Icon } from 'lucide-react'

// 会话记录顶上那一行：还有更早的（滚上来会自动加载，也可以点）/ 到头了但太老的没留
export function LoadOlder({
  more,
  loading,
  truncated,
  onLoad,
}: {
  more: boolean
  loading: boolean
  truncated?: boolean
  onLoad: () => void
}) {
  if (more) {
    return (
      <button
        type="button"
        onClick={onLoad}
        disabled={loading}
        className="flex items-center justify-center gap-1.5 py-1 text-xs text-muted-foreground hover:text-foreground"
      >
        {loading && <Loader2Icon className="size-3 animate-spin" />}
        {loading ? '加载更早的记录…' : '加载更早的记录'}
      </button>
    )
  }
  if (truncated) return <p className="text-center text-xs text-muted-foreground">会话太长，更早的记录没有保留（只留最后 3000 条）</p>
  return null
}
