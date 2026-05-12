import { Badge } from '@/components/ui/badge'

interface TagFilterProps {
  tags: string[]
  selectedTag: string
  onSelect: (tag: string) => void
}

export function TagFilter({ tags, selectedTag, onSelect }: TagFilterProps) {
  return (
    <div className="flex items-center gap-2 flex-wrap">
      <span className="text-sm text-muted-foreground">Tags:</span>
      {tags.map(tag => (
        <Badge
          key={tag}
          variant={selectedTag === tag ? 'default' : 'outline'}
          className="cursor-pointer hover:bg-primary/80 transition-colors"
          onClick={() => onSelect(tag)}
        >
          {tag}
        </Badge>
      ))}
    </div>
  )
}