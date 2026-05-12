import { X } from 'lucide-react'

interface OwnerSelectProps {
  owners: string[]
  selectedOwner: string
  onSelect: (owner: string) => void
}

export function OwnerSelect({ owners, selectedOwner, onSelect }: OwnerSelectProps) {
  const handleClear = () => {
    onSelect('')
  }

  return (
    <div className="flex items-center gap-2">
      <span className="text-sm text-muted-foreground">Owner:</span>
      <select
        value={selectedOwner}
        onChange={e => onSelect(e.target.value)}
        className="flex h-9 w-[200px] items-center justify-between rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-ring"
      >
        <option value="">All owners</option>
        {owners.map(owner => (
          <option key={owner} value={owner}>
            {owner}
          </option>
        ))}
      </select>
      {selectedOwner && (
        <button
          onClick={handleClear}
          className="p-1 hover:bg-muted rounded"
        >
          <X size={14} />
        </button>
      )}
    </div>
  )
}