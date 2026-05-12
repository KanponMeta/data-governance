import { useQuery } from '@tanstack/react-query'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

interface QualityAlert {
  id: string
  asset_name: string
  rule_name: string
  severity: 'critical' | 'warning' | 'info'
  message: string
  created_at: string
  acknowledged: boolean
}

export function AlertList() {
  // D-17: 15-30s polling for quality alerts (hot screen)
  const { data: alerts, isLoading } = useQuery<QualityAlert[]>({
    queryKey: ['quality', 'alerts'],
    queryFn: async () => {
      const res = await fetch('/v1/quality/alerts')
      if (!res.ok) throw new Error('Failed to fetch alerts')
      return (await res.json()).alerts
    },
    staleTime: 15 * 1000,
    refetchInterval: 20 * 1000, // 20s polling (D-17 hot screen)
  })

  const acknowledgeAlert = async (id: string) => {
    await fetch(`/v1/quality/alerts/${id}/acknowledge`, { method: 'POST' })
    // Query will auto-refresh
  }

  const severityVariant = {
    critical: 'destructive' as const,
    warning: 'secondary' as const,
    info: 'outline' as const,
  }

  const unacknowledged = alerts?.filter(a => !a.acknowledged) || []
  const acknowledged = alerts?.filter(a => a.acknowledged) || []

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardTitle className="flex items-center gap-2">
            <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.94 1.94 0 0 0 3.3 0"/></svg>
            Quality Alerts
          </CardTitle>
          <Badge variant="outline">{unacknowledged.length} active</Badge>
        </div>
      </CardHeader>
      <CardContent>
        {isLoading && <div className="text-center py-8 text-muted-foreground">Loading...</div>}

        {unacknowledged.length === 0 && acknowledged.length === 0 && (
          <div className="text-center py-8 text-muted-foreground">No quality alerts.</div>
        )}

        {unacknowledged.length > 0 && (
          <div className="space-y-3 mb-4">
            <p className="text-sm font-medium text-destructive">Active</p>
            {unacknowledged.map(alert => (
              <div key={alert.id} className="flex items-start gap-3 p-3 border rounded-lg border-destructive/50 bg-destructive/5">
                <Badge variant={severityVariant[alert.severity]}>{alert.severity}</Badge>
                <div className="flex-1 min-w-0">
                  <p className="font-medium text-sm">{alert.asset_name}: {alert.rule_name}</p>
                  <p className="text-xs text-muted-foreground mt-0.5">{alert.message}</p>
                  <p className="text-xs text-muted-foreground mt-1">
                    {new Date(alert.created_at).toLocaleString()}
                  </p>
                </div>
                <Button variant="ghost" onClick={() => acknowledgeAlert(alert.id)}>
                  <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>
                </Button>
              </div>
            ))}
          </div>
        )}

        {acknowledged.length > 0 && (
          <div className="space-y-3 opacity-60">
            <p className="text-sm font-medium text-muted-foreground">Acknowledged</p>
            {acknowledged.slice(0, 5).map(alert => (
              <div key={alert.id} className="flex items-center gap-3 p-3 border rounded-lg bg-muted/30">
                <Badge variant={severityVariant[alert.severity]}>{alert.severity}</Badge>
                <div className="flex-1 min-w-0">
                  <p className="font-medium text-sm">{alert.asset_name}: {alert.rule_name}</p>
                </div>
                <span className="text-xs text-muted-foreground">
                  {new Date(alert.created_at).toLocaleDateString()}
                </span>
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  )
}
