import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'

interface User {
  id: string
  email: string
  name: string
  roles: string[]
}

interface Role {
  name: string
  description: string
}

export function UsersTab() {
  const queryClient = useQueryClient()
  const [page] = useState(1)

  const { data, isLoading } = useQuery<{ users: User[]; total: number }>({
    queryKey: ['admin', 'users', page],
    queryFn: async () => {
      const res = await fetch(`/v1/connect/api.v1.AdminService/ListUsers?page=${page}&page_size=20`)
      if (!res.ok) throw new Error('Failed to fetch users')
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

  const assignRole = useMutation({
    mutationFn: async ({ userId, role }: { userId: string; role: string }) => {
      const res = await fetch('/v1/connect/api.v1.AdminService/AssignRole', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ user_id: userId, role }),
      })
      if (!res.ok) throw new Error('Failed to assign role')
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['admin', 'users'] })
    },
  })

  const removeRole = useMutation({
    mutationFn: async ({ userId, role }: { userId: string; role: string }) => {
      const res = await fetch('/v1/connect/api.v1.AdminService/RemoveRole', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ user_id: userId, role }),
      })
      if (!res.ok) throw new Error('Failed to remove role')
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['admin', 'users'] })
    },
  })

  if (isLoading) {
    return <div className="flex justify-center py-12">Loading...</div>
  }

  const roles = rolesData?.roles || []

  return (
    <div className="space-y-4">
      <div className="rounded-lg border">
        <table className="w-full">
          <thead>
            <tr className="border-b bg-muted/50">
              <th className="px-4 py-3 text-left text-sm font-medium">Email</th>
              <th className="px-4 py-3 text-left text-sm font-medium">Name</th>
              <th className="px-4 py-3 text-left text-sm font-medium">Roles</th>
              <th className="px-4 py-3 text-left text-sm font-medium">Actions</th>
            </tr>
          </thead>
          <tbody>
            {data?.users?.map((user) => (
              <tr key={user.id} className="border-b">
                <td className="px-4 py-3 text-sm">{user.email}</td>
                <td className="px-4 py-3 text-sm">{user.name || '—'}</td>
                <td className="px-4 py-3">
                  <div className="flex flex-wrap gap-1">
                    {user.roles.map((role) => (
                      <span
                        key={role}
                        className="inline-flex items-center rounded-md bg-secondary px-2 py-0.5 text-xs font-medium"
                      >
                        {role}
                        <button
                          className="ml-1 hover:text-destructive"
                          onClick={() => removeRole.mutate({ userId: user.id, role })}
                        >
                          ×
                        </button>
                      </span>
                    ))}
                  </div>
                </td>
                <td className="px-4 py-3">
                  <select
                    className="rounded border bg-background px-2 py-1 text-sm"
                    onChange={(e) => {
                      if (e.target.value) {
                        assignRole.mutate({ userId: user.id, role: e.target.value })
                        e.target.value = ''
                      }
                    }}
                    defaultValue=""
                  >
                    <option value="">Add role...</option>
                    {roles
                      .filter((r: Role) => !user.roles.includes(r.name))
                      .map((role: Role) => (
                        <option key={role.name} value={role.name}>
                          {role.name}
                        </option>
                      ))}
                  </select>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {data?.users?.length === 0 && (
        <div className="text-center py-8 text-muted-foreground">No users found.</div>
      )}
    </div>
  )
}
