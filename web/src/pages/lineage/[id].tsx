import { useParams } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { LineageDAG } from '@/components/LineageDAG'
import { Button } from '@/components/ui/button'

export function LineagePage() {
  const params = useParams({ from: '/lineage/$id' }) as { id: string }
  const assetId = params.id
  const [depth, setDepth] = useState(2)

  const { data, isLoading, refetch } = useQuery({
    queryKey: ['lineage', 'neighborhood', assetId, depth],
    queryFn: async () => {
      const params = new URLSearchParams({
        asset_id: assetId,
        depth: String(depth),
      })
      const res = await fetch(`/v1/connect/api.v1.LineageService/Neighborhood?${params}`)
      if (!res.ok) throw new Error('Failed to fetch lineage')
      return res.json()
    },
    staleTime: 60 * 1000,
  })

  return (
    <div className="h-screen flex flex-col">
      <div className="flex items-center gap-4 p-4 border-b">
        <h1 className="text-xl font-bold">Lineage: {assetId}</h1>
        <Button variant="outline" onClick={() => void refetch()}>
          Refresh
        </Button>
      </div>

      <div className="flex-1">
        <LineageDAG
          focusAssetId={assetId}
          data={data}
          isLoading={isLoading}
          onDepthChange={setDepth}
          currentDepth={depth}
        />
      </div>
    </div>
  )
}