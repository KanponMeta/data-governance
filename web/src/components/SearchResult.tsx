import { useNavigate } from '@tanstack/react-router'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'

interface SearchResultProps {
  result: {
    type: 'asset' | 'column'
    name: string
    description?: string
    owner?: string
    tags?: string[]
    asset_name?: string
    highlight?: string
  }
  query: string
}

export function SearchResult({ result }: SearchResultProps) {
  const navigate = useNavigate()

  const handleClick = () => {
    if (result.type === 'asset') {
      navigate({ to: '/assets/$name', params: { name: result.name } })
    } else if (result.asset_name) {
      navigate({ to: '/assets/$name', params: { name: result.asset_name } })
    }
  }

  // Highlight matched terms in the highlight field (from FTS ts_headline)
  const highlightHtml = result.highlight || result.description || ''

  return (
    <Card
      className="cursor-pointer hover:shadow-md transition-shadow"
      onClick={handleClick}
    >
      <CardContent className="p-4">
        <div className="flex items-start gap-3">
          <Badge variant={result.type === 'asset' ? 'default' : 'secondary'}>
            {result.type}
          </Badge>
          <div className="flex-1 min-w-0">
            <h3 className="font-semibold truncate">{result.name}</h3>
            {result.asset_name && result.type === 'column' && (
              <p className="text-xs text-muted-foreground">in {result.asset_name}</p>
            )}
            <p
              className="text-sm text-muted-foreground mt-1 line-clamp-2"
              dangerouslySetInnerHTML={{ __html: highlightHtml }}
            />
            <div className="flex items-center gap-2 mt-2">
              {result.owner && (
                <span className="text-xs text-muted-foreground">Owner: {result.owner}</span>
              )}
              {result.tags?.slice(0, 5).map(tag => (
                <Badge key={tag} variant="outline" className="text-xs">{tag}</Badge>
              ))}
            </div>
          </div>
        </div>
      </CardContent>
    </Card>
  )
}