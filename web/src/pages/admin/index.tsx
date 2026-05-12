import { useQuery } from '@tanstack/react-query'

interface SessionInfo {
  permissions: {
    canManageUsers: boolean
    canEditPolicies: boolean
  }
}

// Inline SVG icons
function UsersIcon() {
  return (
    <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
      <circle cx="9" cy="7" r="4" />
      <path d="M22 21v-2a4 4 0 0 0-3-3.87" />
      <path d="M16 3.13a4 4 0 0 1 0 7.75" />
    </svg>
  )
}

function ShieldIcon() {
  return (
    <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
    </svg>
  )
}

function KeyIcon() {
  return (
    <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="m21 2-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0 3 3L22 7l-3-3m-3.5 3.5L19 4" />
    </svg>
  )
}

export function AdminPage() {
  // Check permissions
  const { data: session } = useQuery<SessionInfo>({
    queryKey: ['me'],
    queryFn: async () => {
      const res = await fetch('/v1/me')
      if (!res.ok) throw new Error('Not authenticated')
      return res.json()
    },
    staleTime: 5 * 60 * 1000,
  })

  const canManage = session?.permissions?.canManageUsers ?? false
  const canEditPolicies = session?.permissions?.canEditPolicies ?? false

  if (!canManage && !canEditPolicies) {
    return (
      <div className="max-w-md mx-auto mt-12">
        <div className="rounded-lg border bg-card text-card-foreground shadow-sm p-6 text-center">
          <p className="text-muted-foreground">
            You do not have permission to access the admin panel.
          </p>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-bold">Admin Panel</h1>

      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        {canManage && (
          <AdminCard
            title="Users"
            description="Manage user accounts and role assignments"
            icon={<UsersIcon />}
            href="/admin/users"
          />
        )}
        {canManage && (
          <AdminCard
            title="Roles"
            description="Create and manage roles with permissions"
            icon={<ShieldIcon />}
            href="/admin/roles"
          />
        )}
        {canEditPolicies && (
          <AdminCard
            title="Policies"
            description="Define column-level access policies"
            icon={<KeyIcon />}
            href="/admin/policies"
          />
        )}
      </div>
    </div>
  )
}

interface AdminCardProps {
  title: string
  description: string
  icon: React.ReactNode
  href: string
}

function AdminCard({ title, description, icon, href }: AdminCardProps) {
  return (
    <a
      href={href}
      className="rounded-lg border bg-card text-card-foreground shadow-sm p-6 hover:bg-muted/50 transition-colors"
    >
      <div className="flex items-start gap-4">
        <div className="text-muted-foreground">{icon}</div>
        <div>
          <h2 className="text-lg font-semibold">{title}</h2>
          <p className="text-sm text-muted-foreground">{description}</p>
        </div>
      </div>
    </a>
  )
}

export function AdminLayout() {
  return <AdminPage />
}
