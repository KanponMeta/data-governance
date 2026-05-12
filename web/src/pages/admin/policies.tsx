import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'

interface Policy {
  id: string
  role: string
  asset: string
  column: string
  action: string
}

interface Role {
  name: string
}

function TrashIcon() {
  return (
    <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <polyline points="3 6 5 6 21 6" />
      <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
    </svg>
  )
}

export function PoliciesTab() {
  const queryClient = useQueryClient()
  const [showCreate, setShowCreate] = useState(false)
  const [role, setRole] = useState('')
  const [asset, setAsset] = useState('')
  const [column, setColumn] = useState('')
  const [action, setAction] = useState('read')

  const { data: policies, isLoading } = useQuery<{ policies: Policy[] }>({
    queryKey: ['admin', 'policies'],
    queryFn: async () => {
      const res = await fetch('/v1/connect/api.v1.AdminService/ListPolicies')
      if (!res.ok) throw new Error('Failed to fetch policies')
      return res.json()
    },
    staleTime: 30 * 1000,
  })

  const { data: rolesData } = useQuery<{ roles: Role[] }>({
    queryKey: ['admin', 'roles'],
    queryFn: async () => {
      const res = await fetch('/v1/connect/api.v1.AdminService/ListRoles')
      if (!res.ok) throw new Error('Failed to fetch roles')
      return res.json()
    },
    staleTime: 60 * 1000,
  })

  const createPolicy = useMutation({
    mutationFn: async (policy: { role: string; asset: string; column: string; action: string }) => {
      const res = await fetch('/v1/connect/api.v1.AdminService/CreatePolicy', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(policy),
      })
      if (!res.ok) throw new Error('Failed to create policy')
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['admin', 'policies'] })
      setShowCreate(false)
      setRole('')
      setAsset('')
      setColumn('')
      setAction('read')
    },
  })

  const deletePolicy = useMutation({
    mutationFn: async (id: string) => {
      const res = await fetch('/v1/connect/api.v1.AdminService/DeletePolicy', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id }),
      })
      if (!res.ok) throw new Error('Failed to delete policy')
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['admin', 'policies'] })
    },
  })

  if (isLoading) {
    return <div className="flex justify-center py-12">Loading...</div>
  }

  const roles = rolesData?.roles || []

  return (
    <div className="space-y-4">
      <div className="flex justify-between items-center">
        <h2 className="text-lg font-semibold">Column Access Policies</h2>
        <button
          className="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90"
          onClick={() => setShowCreate(true)}
        >
          Create Policy
        </button>
      </div>

      {showCreate && (
        <div className="rounded-lg border bg-card p-4">
          <h3 className="text-base font-semibold mb-4">Create Policy</h3>
          <form
            onSubmit={(e) => {
              e.preventDefault()
              if (role && asset && column) {
                createPolicy.mutate({ role, asset, column, action })
              }
            }}
            className="space-y-4"
          >
            <div className="grid grid-cols-2 gap-4">
              <div>
                <label className="block text-sm font-medium mb-1">Role</label>
                <select
                  value={role}
                  onChange={(e) => setRole(e.target.value)}
                  className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
                  required
                >
                  <option value="">Select role...</option>
                  {roles.map((r: Role) => (
                    <option key={r.name} value={r.name}>
                      {r.name}
                    </option>
                  ))}
                </select>
              </div>
              <div>
                <label className="block text-sm font-medium mb-1">Action</label>
                <select
                  value={action}
                  onChange={(e) => setAction(e.target.value)}
                  className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
                >
                  <option value="read">Read</option>
                  <option value="write">Write</option>
                  <option value="masked">Masked</option>
                </select>
              </div>
            </div>
            <div>
              <label className="block text-sm font-medium mb-1">Asset Name</label>
              <input
                type="text"
                value={asset}
                onChange={(e) => setAsset(e.target.value)}
                placeholder="e.g., customers_table"
                className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
                required
              />
            </div>
            <div>
              <label className="block text-sm font-medium mb-1">Column Name</label>
              <input
                type="text"
                value={column}
                onChange={(e) => setColumn(e.target.value)}
                placeholder="e.g., ssn, email"
                className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
                required
              />
            </div>
            <div className="flex gap-2 justify-end">
              <button
                type="button"
                className="rounded-md border border-input bg-background px-4 py-2 text-sm font-medium hover:bg-accent"
                onClick={() => setShowCreate(false)}
              >
                Cancel
              </button>
              <button
                type="submit"
                className="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90"
                disabled={createPolicy.isPending || !role || !asset || !column}
              >
                {createPolicy.isPending ? 'Creating...' : 'Create Policy'}
              </button>
            </div>
          </form>
        </div>
      )}

      <div className="space-y-2">
        {policies?.policies?.map((policy: Policy) => (
          <div key={policy.id} className="rounded-lg border bg-card p-4 flex items-center justify-between">
            <div className="flex items-center gap-3">
              <span className="inline-flex items-center rounded-md bg-secondary px-2.5 py-0.5 text-xs font-medium">
                {policy.role}
              </span>
              <span className="font-mono text-sm">{policy.asset}.{policy.column}</span>
              <span className="inline-flex items-center rounded-md border border-input px-2.5 py-0.5 text-xs font-medium">
                {policy.action}
              </span>
            </div>
            <button
              className="rounded-md p-2 hover:bg-destructive/10 text-destructive"
              onClick={() => {
                if (confirm('Delete this policy?')) {
                  deletePolicy.mutate(policy.id)
                }
              }}
            >
              <TrashIcon />
            </button>
          </div>
        ))}
        {policies?.policies?.length === 0 && (
          <div className="text-center py-8 text-muted-foreground">No policies defined.</div>
        )}
      </div>
    </div>
  )
}
