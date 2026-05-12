import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { SearchBar } from '@/components/SearchBar'
import { SearchResult } from '@/components/SearchResult'
import { TagFilter } from '@/components/TagFilter'
import { OwnerSelect } from '@/components/OwnerSelect'
import { Spinner } from '@/components/ui/spinner'

export function CatalogPage() {
  const [query, setQuery] = useState('')
  const [selectedTag, setSelectedTag] = useState('')
  const [selectedOwner, setSelectedOwner] = useState('')
  const [searchType, setSearchType] = useState<string>('')
  const [page, setPage] = useState(1)

  const { data, isLoading } = useQuery({
    queryKey: ['catalog', 'search', query, selectedTag, selectedOwner, searchType, page],
    queryFn: async () => {
      const params = new URLSearchParams({
        page: String(page),
        page_size: '20',
      })
      if (query.trim()) params.set('q', query)
      if (selectedTag) params.set('tag', selectedTag)
      if (selectedOwner) params.set('owner', selectedOwner)
      if (searchType) params.set('type', searchType)
      const res = await fetch(`/v1/catalog/search?${params}`)
      if (!res.ok) throw new Error('Search failed')
      return res.json()
    },
    staleTime: 60 * 1000,
    refetchInterval: 60 * 1000, // D-17 cold screen
  })

  // Clear page when filters change
  const handleTagSelect = (tag: string) => {
    setSelectedTag(prev => prev === tag ? '' : tag)
    setPage(1)
  }

  const handleOwnerSelect = (owner: string) => {
    setSelectedOwner(owner)
    setPage(1)
  }

  const handleSearch = () => {
    setPage(1)
  }

  return (
    <div className="space-y-4 max-w-4xl mx-auto">
      <h1 className="text-2xl font-bold">Data Catalog</h1>

      {/* Search controls */}
      <div className="space-y-3">
        <SearchBar
          value={query}
          onChange={setQuery}
          onSearch={handleSearch}
          placeholder="Search assets, columns, descriptions..."
        />

        {/* Tag filter chips (UI-03) */}
        {data?.tags?.length > 0 && (
          <TagFilter
            tags={data.tags}
            selectedTag={selectedTag}
            onSelect={handleTagSelect}
          />
        )}

        {/* Owner filter dropdown (UI-03) */}
        {data?.owners?.length > 0 && (
          <OwnerSelect
            owners={data.owners}
            selectedOwner={selectedOwner}
            onSelect={handleOwnerSelect}
          />
        )}

        {/* Type filter */}
        <div className="flex items-center gap-2">
          <span className="text-sm text-muted-foreground">Type:</span>
          {['all', 'asset', 'column'].map(t => (
            <button
              key={t}
              onClick={() => { setSearchType(t === 'all' ? '' : t); setPage(1) }}
              className={`px-3 py-1 rounded text-sm ${
                (t === 'all' && !searchType) || searchType === t
                  ? 'bg-primary text-primary-foreground'
                  : 'bg-muted hover:bg-muted/80'
              }`}
            >
              {t === 'all' ? 'All' : t.charAt(0).toUpperCase() + t.slice(1)}
            </button>
          ))}
        </div>
      </div>

      {/* Results */}
      {isLoading && (
        <div className="flex justify-center py-12">
          <Spinner />
        </div>
      )}

      {data?.results?.length === 0 && !isLoading && (
        <div className="text-center py-12 text-muted-foreground">
          {query || selectedTag || selectedOwner
            ? `No results found for current filters`
            : 'No assets in catalog yet'}
        </div>
      )}

      {data?.results?.length > 0 && (
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            {data.total} result{data.total !== 1 ? 's' : ''}
            {selectedTag && <> tagged with <strong>"{selectedTag}"</strong></>}
            {selectedOwner && <> owned by <strong>"{selectedOwner}"</strong></>}
            {query && <> matching <strong>"{query}"</strong></>}
          </p>
          {data.results.map((result: any, i: number) => (
            <SearchResult
              key={`${result.type}-${result.name}-${i}`}
              result={result}
              query={query}
            />
          ))}
        </div>
      )}

      {/* Pagination */}
      {data?.total > 20 && (
        <div className="flex items-center justify-center gap-2 pt-4">
          <button
            onClick={() => setPage(p => p - 1)}
            disabled={page === 1}
            className="px-3 py-1 rounded border disabled:opacity-50"
          >
            Previous
          </button>
          <span className="text-sm">
            Page {page} of {Math.ceil(data.total / 20)}
          </span>
          <button
            onClick={() => setPage(p => p + 1)}
            disabled={page >= Math.ceil(data.total / 20)}
            className="px-3 py-1 rounded border disabled:opacity-50"
          >
            Next
          </button>
        </div>
      )}
    </div>
  )
}