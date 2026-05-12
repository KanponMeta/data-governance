import { StrictMode, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query'
import { RouterProvider, createRouter, createRootRoute, Route } from '@tanstack/react-router'
import './index.css'

// Simple scaffold app with basic nav
function ScaffoldApp() {
  return (
    <div className="min-h-screen bg-background text-foreground">
      <header className="border-b">
        <div className="container mx-auto px-4 py-4 flex items-center gap-6">
          <h1 className="text-xl font-semibold">Data Governance Platform</h1>
          <nav className="flex gap-4 text-sm">
            <a href="/" className="text-muted-foreground hover:text-foreground">Home</a>
            <a href="/assets" className="text-muted-foreground hover:text-foreground">Assets</a>
            <a href="/catalog" className="text-muted-foreground hover:text-foreground">Catalog</a>
          </nav>
        </div>
      </header>
      <main className="container mx-auto px-4 py-8">
        <p>Welcome. UI scaffold is in place.</p>
      </main>
    </div>
  )
}

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 60 * 1000,
      retry: 1,
    },
  },
})

// Root route
const rootRoute = createRootRoute({
  component: ScaffoldApp,
})

// Index route at '/'
const indexRoute = new Route({
  getParentRoute: () => rootRoute,
  path: '/',
})

// Assets layout route at '/assets'
const assetsLayoutRoute = new Route({
  getParentRoute: () => rootRoute,
  path: '/assets',
})

// Admin layout route at '/admin'
const adminLayoutRoute = new Route({
  getParentRoute: () => rootRoute,
  path: '/admin',
})

// Governance layout route at '/governance'
const governanceLayoutRoute = new Route({
  getParentRoute: () => rootRoute,
  path: '/governance',
})

// Catalog layout route at '/catalog'
const catalogLayoutRoute = new Route({
  getParentRoute: () => rootRoute,
  path: '/catalog',
})

// Lineage layout route at '/lineage'
const lineageLayoutRoute = new Route({
  getParentRoute: () => rootRoute,
  path: '/lineage',
})

// Asset dashboard page at '/assets'
const AssetDashboardPage = () => {
  const [search, setSearch] = useState('')
  const [page] = useState(1)

  const { data, isLoading } = useQuery({
    queryKey: ['assets', page, search],
    queryFn: async () => {
      const params = new URLSearchParams({ page: String(page), page_size: '50' })
      if (search) params.set('q', search)
      const res = await fetch(`/v1/connect/api.v1.AssetService/ListAssets?${params}`)
      if (!res.ok) throw new Error('Failed to fetch assets')
      return res.json()
    },
    staleTime: 60 * 1000,
    refetchInterval: 60 * 1000,
  })

  if (isLoading) {
    return <div className="flex items-center justify-center h-64"><div className="animate-spin h-6 w-6 border-2 border-primary border-t-transparent rounded-full" /></div>
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-4">
        <h1 className="text-2xl font-bold">Assets</h1>
        <input
          placeholder="Search assets..."
          value={search}
          onChange={e => setSearch(e.target.value)}
          className="max-w-xs px-3 py-1 border rounded text-sm"
        />
      </div>
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        {data?.assets?.map((asset: any) => (
          <AssetCardPage key={asset.name} asset={asset} />
        ))}
      </div>
      {data?.assets?.length === 0 && (
        <div className="text-center py-12 text-muted-foreground">No assets found.</div>
      )}
    </div>
  )
}

// Inline AssetCard component
function AssetCardPage({ asset }: { asset: any }) {
  const stateVariant: Record<string, string> = {
    active: 'bg-primary text-primary-foreground',
    draft: 'bg-secondary text-secondary-foreground',
    in_review: 'border border-input',
  }

  return (
    <div
      className="rounded-lg border bg-card p-4 cursor-pointer hover:shadow-md transition-shadow"
      onClick={() => window.location.href = `/assets/${asset.name}`}
    >
      <div className="flex items-start justify-between">
        <h3 className="text-base font-semibold">{asset.name}</h3>
        <span className={`inline-flex items-center rounded-md px-2.5 py-0.5 text-xs font-semibold ${stateVariant[asset.state] || 'bg-secondary'}`}>
          {asset.state}
        </span>
      </div>
      {asset.description && (
        <p className="text-sm text-muted-foreground mt-1 line-clamp-2">{asset.description}</p>
      )}
      <div className="flex items-center justify-between text-sm mt-2">
        <span className="text-muted-foreground">Last run</span>
        <span>{asset.last_materialized_at ? new Date(asset.last_materialized_at).toLocaleString() : 'Never'}</span>
      </div>
      <div className="flex items-center justify-between text-sm">
        <span className="text-muted-foreground">Quality</span>
        <QualityBadge state={asset.last_materialize_state} />
      </div>
    </div>
  )
}

function QualityBadge({ state }: { state: string }) {
  const variant = {
    success: 'bg-primary text-primary-foreground',
    failed: 'bg-destructive text-destructive-foreground',
    quality_failed: 'bg-destructive text-destructive-foreground',
    running: 'bg-secondary text-secondary-foreground',
    queued: 'border border-input',
  }[state] || 'border border-input'

  const label = {
    success: 'Passed',
    failed: 'Failed',
    quality_failed: 'Quality Failed',
    running: 'Running',
    queued: 'Queued',
  }[state] || state || 'Unknown'

  return <span className={`inline-flex items-center rounded-md px-2.5 py-0.5 text-xs font-semibold ${variant}`}>{label}</span>
}

const assetsIndexRoute = new Route({
  getParentRoute: () => assetsLayoutRoute,
  path: '/',
  component: AssetDashboardPage,
})

// Asset detail route at '/assets/:name'
const AssetDetailPage = () => {
  const params = useParams({ from: '/assets/$name' })
  const assetName = params.name

  // Fetch asset metadata
  const { data: asset, isLoading: assetLoading } = useQuery({
    queryKey: ['assets', assetName],
    queryFn: async () => {
      const res = await fetch(`/v1/connect/api.v1.AssetService/GetAsset?name=${encodeURIComponent(assetName)}`)
      if (!res.ok) throw new Error('Asset not found')
      return res.json()
    },
    staleTime: 30 * 1000,
  })

  // D-17: hot screen (asset with active runs) = 4s polling, cold = 60s
  const latestRunState = asset?.last_materialize_state
  const isHot = latestRunState === 'running' || latestRunState === 'queued'
  const pollingInterval = isHot ? 4 * 1000 : 60 * 1000

  const { data: runsData, isLoading: runsLoading } = useQuery({
    queryKey: ['assets', assetName, 'runs'],
    queryFn: async () => {
      const res = await fetch(`/v1/connect/api.v1.AssetService/ListRuns?asset_name=${encodeURIComponent(assetName)}`)
      if (!res.ok) throw new Error('Failed to fetch runs')
      return res.json()
    },
    staleTime: 30 * 1000,
    refetchInterval: pollingInterval,
  })

  if (assetLoading || runsLoading) {
    return <div className="flex items-center justify-center h-64"><div className="animate-spin h-6 w-6 border-2 border-primary border-t-transparent rounded-full" /></div>
  }

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-bold">{assetName}</h1>
          <div className="flex items-center gap-2 mt-1">
            <span className={`inline-flex items-center rounded-md px-2.5 py-0.5 text-xs font-semibold ${asset?.state === 'active' ? 'bg-primary text-primary-foreground' : 'bg-secondary'}`}>
              {asset?.state || 'unknown'}
            </span>
            <QualityBadge state={asset?.last_materialize_state} />
          </div>
        </div>
        <button onClick={() => history.back()} className="px-4 py-2 border rounded text-sm">Back</button>
      </div>
      <div>
        <h2 className="text-lg font-semibold mb-3">Run History</h2>
        <RunHistoryPage runs={runsData?.runs || []} />
      </div>
    </div>
  )
}

// Run history with step expansion
function RunHistoryPage({ runs }: { runs: any[] }) {
  const [expandedRunId, setExpandedRunId] = useState<string | null>(null)

  if (!runs || runs.length === 0) {
    return <div className="text-muted-foreground py-8 text-center">No runs yet.</div>
  }

  return (
    <div className="space-y-3">
      {runs.map(run => (
        <div key={run.id}>
          <div
            className="flex items-center gap-3 p-3 border rounded-lg cursor-pointer hover:bg-muted/50"
            onClick={() => setExpandedRunId(expandedRunId === run.id ? null : run.id)}
          >
            <RunStatusBadgeInline state={run.state} />
            <span className="font-mono text-sm">{run.id?.slice(0, 8)}</span>
            <span className="text-sm text-muted-foreground">{run.asset_name}</span>
            <span className="text-sm text-muted-foreground ml-auto">
              {run.started_at ? new Date(run.started_at).toLocaleString() : '—'}
            </span>
          </div>
          {expandedRunId === run.id && run.steps?.length > 0 && (
            <div className="ml-8 mt-2 space-y-2">
              {run.steps.map((step: any, i: number) => (
                <div key={i} className="flex items-center gap-3 p-2 bg-muted/30 rounded border">
                  <RunStatusBadgeInline state={step.state} />
                  <span className="text-sm font-medium">{step.name}</span>
                  <span className="text-xs text-muted-foreground ml-auto">
                    {step.started_at && new Date(step.started_at).toLocaleString()}
                    {step.error && <span className="text-destructive ml-2">{step.error}</span>}
                  </span>
                </div>
              ))}
            </div>
          )}
        </div>
      ))}
    </div>
  )
}

function RunStatusBadgeInline({ state }: { state: string }) {
  const variant = {
    success: 'bg-primary text-primary-foreground',
    failed: 'bg-destructive text-destructive-foreground',
    quality_failed: 'bg-destructive text-destructive-foreground',
    running: 'bg-secondary text-secondary-foreground',
    queued: 'border border-input',
    pending: 'border border-input',
    skipped: 'bg-secondary text-secondary-foreground',
  }[state] || 'border border-input'

  const label = {
    success: 'Success',
    failed: 'Failed',
    quality_failed: 'Q-Failed',
    running: 'Running',
    queued: 'Queued',
    pending: 'Pending',
    skipped: 'Skipped',
  }[state] || state

  return <span className={`inline-flex items-center rounded-md px-2.5 py-0.5 text-xs font-semibold ${variant}`}>{label}</span>
}

// Use useParams hook
import { useParams } from '@tanstack/react-router'

const assetDetailRoute = new Route({
  getParentRoute: () => assetsLayoutRoute,
  path: '/$name',
  component: AssetDetailPage,
})

const routeTree = rootRoute.addChildren([
  indexRoute,
  assetsLayoutRoute.addChildren([assetsIndexRoute, assetDetailRoute]),
  adminLayoutRoute.addChildren([
    new Route({
      getParentRoute: () => adminLayoutRoute,
      path: '/',
      component: () => import('./pages/admin/index').then(m => m.AdminPage),
    }),
  ]),
  governanceLayoutRoute.addChildren([
    new Route({
      getParentRoute: () => governanceLayoutRoute,
      path: '/',
      component: () => import('./pages/governance/index').then(m => m.GovernancePage),
    }),
  ]),
  catalogLayoutRoute.addChildren([
    new Route({
      getParentRoute: () => catalogLayoutRoute,
      path: '/',
      component: () => import('./pages/catalog/index').then(m => m.CatalogPage),
    }),
  ]),
  lineageLayoutRoute.addChildren([
    new Route({
      getParentRoute: () => lineageLayoutRoute,
      path: '/$id',
      component: () => import('./pages/lineage/[id]').then(m => m.LineagePage),
    }),
  ]),
])

const router = createRouter({
  routeTree,
  context: { queryClient },
})

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
)
