import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider, createRouter, createRootRoute, Route } from '@tanstack/react-router'
import './index.css'

function ScaffoldApp() {
  return (
    <div className="min-h-screen bg-background text-foreground">
      <header className="border-b">
        <div className="container mx-auto px-4 py-4">
          <h1 className="text-xl font-semibold">Data Governance Platform</h1>
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

// Root route — actual child routes added in subsequent plans (06-04, 06-05)
const rootRoute = createRootRoute({
  component: ScaffoldApp,
})

// Index route at '/'
const indexRoute = new Route({
  getParentRoute: () => rootRoute,
  path: '/',
})

const routeTree = rootRoute.addChildren([indexRoute])

// Scaffold router — actual route tree expanded in 06-04, 06-05
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