import { useQuery } from '@tanstack/react-query'
import { QualityTrendChart } from '@/components/QualityTrendChart'
import { AlertList } from '@/components/AlertList'

interface QualityPageProps {
  assetName: string
}

// Quality tab page for asset detail - shows trend chart and active alerts
export function QualityPage({ assetName }: QualityPageProps) {
  // Fetch quality trend data for this asset
  const { data: trendData, isLoading: trendLoading } = useQuery({
    queryKey: ['quality', 'trend', assetName],
    queryFn: async () => {
      const res = await fetch(`/v1/quality/trend?asset=${encodeURIComponent(assetName)}&runs=30`)
      if (!res.ok) throw new Error('Failed to fetch quality trend')
      return res.json()
    },
    staleTime: 30 * 1000,
    refetchInterval: 20 * 1000, // D-17 hot screen: 20s polling
  })

  return (
    <div className="space-y-6">
      <div>
        <h3 className="text-lg font-semibold mb-4">Quality Trend</h3>
        <QualityTrendChart
          assetName={assetName}
          data={trendData?.points || null}
          isLoading={trendLoading}
        />
      </div>
      <div>
        <h3 className="text-lg font-semibold mb-4">Quality Alerts</h3>
        <AlertList />
      </div>
    </div>
  )
}
