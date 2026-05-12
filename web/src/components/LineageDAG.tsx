import { useCallback, useMemo, useState } from 'react'
import {
  ReactFlow,
  Controls,
  MiniMap,
  Background,
  applyNodeChanges,
  applyEdgeChanges,
  type NodeChange,
  type EdgeChange,
  type Node,
  type Edge,
  MarkerType,
} from '@xyflow/react'
import dagre from 'dagre'
import '@xyflow/react/dist/style.css'
import { AssetNode, type AssetNodeData } from './AssetNode'
import { ColumnPanel } from './ColumnPanel'

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const nodeTypes: Record<string, any> = {
  asset: AssetNode,
}

interface NeighborhoodData {
  nodes: Array<{
    id: string
    name: string
    type: 'asset' | 'column'
    columns?: Array<{ name: string; type: string }>
  }>
  edges: Array<{
    from_id: string
    to_id: string
    type: 'asset_lineage' | 'column_lineage'
  }>
}

interface LineageDAGProps {
  focusAssetId: string
  data: NeighborhoodData | null
  isLoading: boolean
  onDepthChange: (depth: number) => void
  currentDepth: number
}

export function LineageDAG({
  focusAssetId,
  data,
  isLoading,
  onDepthChange,
  currentDepth,
}: LineageDAGProps) {
  const [nodes, setNodes] = useState<Node<AssetNodeData>[]>([])
  const [edges, setEdges] = useState<Edge[]>([])
  const [selectedNode, setSelectedNode] = useState<string | null>(null)
  const [showColumnView, setShowColumnView] = useState(false)

  // Apply dagre layout to nodes when data changes
  useMemo(() => {
    if (!data) {
      setNodes([])
      setEdges([])
      return
    }

    const cfgnodes: Node<AssetNodeData>[] = data.nodes.map(node => ({
      id: node.id,
      type: 'asset',
      position: { x: 0, y: 0 },
      data: {
        label: node.name,
        type: node.type,
        columns: node.columns,
        isFocus: node.id === focusAssetId,
      },
    }))

    const cfgedges: Edge[] = data.edges
      .filter(e => e.type === 'asset_lineage' || showColumnView)
      .map(edge => ({
        id: `${edge.from_id}-${edge.to_id}`,
        source: edge.from_id,
        target: edge.to_id,
        type: 'smoothstep',
        animated: edge.type === 'asset_lineage',
        markerEnd: { type: MarkerType.ArrowClosed },
        style: { stroke: '#94a3b8' },
      }))

    // Apply dagre layout
    const g = new dagre.graphlib.Graph()
    g.setDefaultEdgeLabel(() => ({}))
    g.setGraph({ rankdir: 'TB', nodesep: 60, ranksep: 80 })

    cfgnodes.forEach(node => g.setNode(node.id, { width: 180, height: 60 }))
    cfgedges.forEach(edge => g.setEdge(edge.source, edge.target))

    dagre.layout(g)

    const layoutedNodes = cfgnodes.map(node => {
      const dagreNode = g.node(node.id)
      return {
        ...node,
        position: {
          x: dagreNode.x - 90,
          y: dagreNode.y - 30,
        },
      }
    })

    setNodes(layoutedNodes)
    setEdges(cfgedges)
  }, [data, focusAssetId, showColumnView])

  const onNodesChange = useCallback(
    (changes: NodeChange<Node<AssetNodeData>>[]) => {
      setNodes(nds => applyNodeChanges(changes, nds))
    },
    [],
  )

  const onEdgesChange = useCallback(
    (changes: EdgeChange<Edge>[]) => {
      setEdges(eds => applyEdgeChanges(changes, eds))
    },
    [],
  )

  const onNodeClick = useCallback((_event: React.MouseEvent, node: Node<AssetNodeData>) => {
    void _event
    setSelectedNode(node.id)
  }, [])

  const onPaneClick = useCallback(() => {
    setSelectedNode(null)
    setShowColumnView(false)
  }, [])

  const selectedNodeData = selectedNode
    ? data?.nodes.find(n => n.id === selectedNode)
    : null

  return (
    <div className="relative h-[calc(100vh-200px)]">
      {isLoading && (
        <div className="absolute inset-0 bg-background/50 z-10 flex items-center justify-center">
          <div className="animate-spin h-8 w-8 border-4 border-primary border-t-transparent rounded-full" />
        </div>
      )}

      <ReactFlow
        nodes={nodes}
        edges={edges}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onNodeClick={onNodeClick}
        onPaneClick={onPaneClick}
        nodeTypes={nodeTypes}
        fitView
        fitViewOptions={{ padding: 0.2 }}
      >
        <Background />
        <Controls />
        <MiniMap />
      </ReactFlow>

      {/* Depth selector */}
      <div className="absolute top-4 right-4 flex items-center gap-2 bg-background border rounded-lg p-2 shadow-md">
        <span className="text-sm">Depth:</span>
        {[1, 2, 3].map(d => (
          <button
            key={d}
            onClick={() => onDepthChange(d)}
            className={`px-2 py-1 rounded text-sm ${
              currentDepth === d
                ? 'bg-primary text-primary-foreground'
                : 'hover:bg-muted'
            }`}
          >
            {d}
          </button>
        ))}
      </div>

      {/* Column toggle */}
      {selectedNode && (
        <button
          onClick={() => setShowColumnView(v => !v)}
          className="absolute top-4 left-4 px-3 py-1.5 bg-background border rounded-lg text-sm shadow-md hover:bg-muted"
        >
          {showColumnView ? 'Hide columns' : 'Show columns'}
        </button>
      )}

      {/* Column panel (D-13: side panel on node click) */}
      {selectedNodeData && (
        <ColumnPanel
          node={selectedNodeData}
          onClose={() => setSelectedNode(null)}
        />
      )}
    </div>
  )
}