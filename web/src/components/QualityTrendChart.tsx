import { useMemo } from 'react'
import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  ReferenceLine,
} from 'recharts'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

interface QualityTrendPoint {
  run_id: string
  finished_at: string
  score: number
  state: string
  rule_results?: Array<{
    rule_name: string
    passed: boolean
    value: string
    threshold: string
  }>
}

interface QualityTrendChartProps {
  assetName: string
  data: QualityTrendPoint[] | null
  isLoading: boolean
}

export function QualityTrendChart({ assetName, data, isLoading }: QualityTrendChartProps) {
  const chartData = useMemo(() => {
    if (!data) return []
    return data.map(point => ({
      run: point.run_id.slice(0, 8),
      score: point.score,
      state: point.state,
      date: new Date(point.finished_at).toLocaleDateString(),
      fullDate: new Date(point.finished_at).toLocaleString(),
      ruleResults: point.rule_results || [],
    }))
  }, [data])

  const avgScore = useMemo(() => {
    if (!data || data.length === 0) return 0
    return Math.round(data.reduce((sum, p) => sum + p.score, 0) / data.length)
  }, [data])

  const stateToColor = (state: string) => {
    switch (state) {
      case 'success': return '#22c55e' // green
      case 'failed': return '#ef4444' // red
      case 'quality_failed': return '#f97316' // orange
      default: return '#94a3b8' // gray
    }
  }

  const CustomDot = (props: any) => {
    const { cx, cy, payload } = props
    if (!cx || !cy) return null
    return (
      <circle
        cx={cx}
        cy={cy}
        r={4}
        fill={stateToColor(payload.state)}
        stroke="#fff"
        strokeWidth={2}
      />
    )
  }

  const CustomTooltip = ({ active, payload }: any) => {
    if (!active || !payload?.length) return null
    const d = payload[0].payload
    return (
      <div className="bg-background border rounded-lg p-3 shadow-lg text-sm">
        <p className="font-medium">Run {d.run}</p>
        <p className="text-muted-foreground">{d.fullDate}</p>
        <p className="mt-1">
          Score: <span className="font-bold" style={{ color: stateToColor(d.state) }}>{d.score}</span>/100
        </p>
        <p>State: {d.state}</p>
        {d.ruleResults.length > 0 && (
          <div className="mt-2 border-t pt-2">
            <p className="font-medium mb-1">Rule results:</p>
            {d.ruleResults.map((r: any, i: number) => (
              <div key={i} className="flex items-center gap-2 text-xs">
                <span className={r.passed ? 'text-green-600' : 'text-red-600'}>
                  {r.passed ? 'PASS' : 'FAIL'}
                </span>
                <span>{r.rule_name}</span>
                <span className="text-muted-foreground">
                  {r.value} {r.threshold && `/ ${r.threshold}`}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    )
  }

  if (isLoading) {
    return (
      <Card>
        <CardHeader><CardTitle>Quality Trend</CardTitle></CardHeader>
        <CardContent>
          <div className="h-64 flex items-center justify-center text-muted-foreground">
            Loading...
          </div>
        </CardContent>
      </Card>
    )
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardTitle>Quality Trend: {assetName}</CardTitle>
          {avgScore > 0 && (
            <div className="text-sm">
              Avg: <span className="font-bold">{avgScore}</span>/100
            </div>
          )}
        </div>
      </CardHeader>
      <CardContent>
        {chartData.length === 0 ? (
          <div className="h-64 flex items-center justify-center text-muted-foreground">
            No quality data available.
          </div>
        ) : (
          <ResponsiveContainer width="100%" height={300}>
            <LineChart data={chartData} margin={{ top: 5, right: 20, bottom: 5, left: 0 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis
                dataKey="date"
                tick={{ fontSize: 12 }}
                tickLine={{ stroke: '#e0e0e0' }}
              />
              <YAxis
                domain={[0, 100]}
                tick={{ fontSize: 12 }}
                tickLine={{ stroke: '#e0e0e0' }}
                label={{ value: 'Score', angle: -90, position: 'insideLeft', fontSize: 12 }}
              />
              <Tooltip content={<CustomTooltip />} />
              <ReferenceLine y={100} stroke="#22c55e" strokeDasharray="3 3" label={{ value: 'Target', position: 'right', fontSize: 10 }} />
              <Line
                type="monotone"
                dataKey="score"
                stroke="#6366f1"
                strokeWidth={2}
                dot={<CustomDot />}
                activeDot={{ r: 6, stroke: '#6366f1', strokeWidth: 2 }}
              />
            </LineChart>
          </ResponsiveContainer>
        )}
      </CardContent>
    </Card>
  )
}
