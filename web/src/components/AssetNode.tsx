import { Handle, Position } from '@xyflow/react'
import { Badge } from '@/components/ui/badge'

export interface AssetNodeData extends Record<string, unknown> {
  label: string
  type: string
  columns?: Array<{ name: string; type: string }>
  isFocus: boolean
}

interface AssetNodeProps {
  data: AssetNodeData
  selected?: boolean
}

export function AssetNode({ data, selected }: AssetNodeProps) {
  return (
    <div
      className={`px-4 py-2 rounded-lg border-2 min-w-[140px] ${
        selected
          ? 'border-primary shadow-lg'
          : data.isFocus
          ? 'border-primary/50'
          : 'border-border'
      } bg-background`}
    >
      <Handle type="target" position={Position.Top} className="!bg-muted-foreground" />
      <div className="flex items-center gap-2">
        {data.isFocus && <Badge variant="default" className="text-xs">Focus</Badge>}
        <span className="font-medium truncate">{data.label}</span>
      </div>
      <div className="text-xs text-muted-foreground mt-0.5 capitalize">{data.type}</div>
      <Handle type="source" position={Position.Bottom} className="!bg-muted-foreground" />
    </div>
  )
}