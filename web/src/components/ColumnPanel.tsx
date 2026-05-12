import { X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'

interface ColumnPanelProps {
  node: {
    id: string
    name: string
    type: 'asset' | 'column'
    columns?: Array<{ name: string; type: string }>
  }
  onClose: () => void
}

export function ColumnPanel({ node, onClose }: ColumnPanelProps) {
  return (
    <div className="absolute right-0 top-0 h-full w-80 bg-background border-l shadow-lg z-20 overflow-y-auto">
      <div className="flex items-center justify-between p-4 border-b">
        <h3 className="font-semibold truncate">{node.name}</h3>
        <Button variant="ghost" onClick={onClose}>
          <X size={16} />
        </Button>
      </div>

      <div className="p-4 space-y-4">
        <div>
          <h4 className="text-sm font-medium mb-2">Type</h4>
          <Badge variant="secondary">{node.type}</Badge>
        </div>

        {node.columns && node.columns.length > 0 && (
          <div>
            <h4 className="text-sm font-medium mb-2">Columns ({node.columns.length})</h4>
            <div className="space-y-1">
              {node.columns.map(col => (
                <div key={col.name} className="flex items-center justify-between text-sm py-1 border-b">
                  <span className="font-mono">{col.name}</span>
                  <span className="text-muted-foreground text-xs">{col.type}</span>
                </div>
              ))}
            </div>
          </div>
        )}

        <div>
          <h4 className="text-sm font-medium mb-2">Metadata</h4>
          <div className="text-sm space-y-1">
            <div className="flex justify-between">
              <span className="text-muted-foreground">ID</span>
              <span className="font-mono text-xs">{node.id.slice(0, 12)}...</span>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}