import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'

interface Role {
  name: string
  description: string
}

function TrashIcon() {
  return (
    <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <polyline points="3 6 5 6 21 6" />
      <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
    </svg>
  )
}

export function RolesTab() {
  const queryClient = useQueryClient()
  const [showCreate, setShowCreate] = useState(false)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')

  const { data: roles, isLoading } = useQuery<{ roles: Role[] }>({
    queryKey: ['admin', 'roles'],
    queryFn: async () => {
      const res = await fetch('/v1/connect/api.v1.AdminService/ListRoles')
      if (!res.ok) throw new Error('Failed to fetch roles')
      return res.json()
    },
    staleTime: 60 * 1000,
  })

  const createRole = useMutation({
    mutationFn: async ({ name, description }: { name: string; description: string }) => {
      const res = await fetch('/v1/connect/api.v1.AdminService/CreateRole', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, description }),
      })
      if (!res.ok) throw new Error('Failed to create role')
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['admin', 'roles'] })
      setShowCreate(false)
      setName('')
      setDescription('')
    },
  })

  const deleteRole = useMutation({
    mutationFn: async (name: string) => {
      const res = await fetch('/v1/connect/api.v1.AdminService/DeleteRole', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name }),
      })
      if (!res.ok) throw new Error('Failed to delete role')
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['admin', 'roles'] })
    },
  })

  if (isLoading) {
    return <div className="flex justify-center py-12">Loading...</div>
  }

  return (
    <div className="space-y-4">
      <div className="flex justify-between items-center">
        <h2 className="text-lg font-semibold">Roles</h2>
        <button
          className="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90"
          onClick={() => setShowCreate(true)}
        >
          Create Role
        </button>
      </div>

      {showCreate && (
        <div className="rounded-lg border bg-card p-4">
          <h3 className="text-base font-semibold mb-4">Create Role</h3>
          <form
            onSubmit={(e) => {
              e.preventDefault()
              if (name.trim()) {
                createRole.mutate({ name: name.trim(), description: description.trim() })
              }
            }}
            className="space-y-4"
          >
            <div>
              <label className="block text-sm font-medium mb-1">Role Name</label>
              <input
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g., data-engineer"
                className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
                required
              />
            </div>
            <div>
              <label className="block text-sm font-medium mb-1">Description</label>
              <textarea
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="Describe this role's responsibilities"
                className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
                rows={2}
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
                disabled={createRole.isPending || !name.trim()}
              >
                {createRole.isPending ? 'Creating...' : 'Create Role'}
              </button>
            </div>
          </form>
        </div>
      )}

      <div className="space-y-2">
        {roles?.roles?.map((role: Role) => (
          <div key={role.name} className="rounded-lg border bg-card p-4 flex items-center justify-between">
            <div>
              <p className="font-medium">{role.name}</p>
              <p className="text-sm text-muted-foreground">{role.description}</p>
            </div>
            <button
              className="rounded-md p-2 hover:bg-destructive/10 text-destructive"
              onClick={() => {
                if (confirm(`Delete role ${role.name}?`)) {
                  deleteRole.mutate(role.name)
                }
              }}
            >
              <TrashIcon />
            </button>
          </div>
        ))}
      </div>
    </div>
  )
}
