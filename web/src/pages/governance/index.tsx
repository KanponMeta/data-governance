import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { Label } from '@/components/ui/label'
import { Spinner } from '@/components/ui/spinner'
import { Bell } from 'lucide-react'

// Review interface matching the proto response
interface Review {
  id: string
  asset_name: string
  status: 'pending' | 'approved' | 'rejected' | string
  submitted_by: string
  submitted_at: string
  reviewed_by?: string
  reviewed_at?: string
  comments: Array<{
    author: string
    body: string
    created_at: string
  }>
}

interface SessionInfo {
  permissions: {
    canApprove: boolean
  }
}

// ReviewCard component
function ReviewCard({ review, onClick }: { review: Review; onClick: () => void }) {
  const statusVariant = {
    pending: 'secondary' as const,
    approved: 'default' as const,
    rejected: 'destructive' as const,
  }[review.status] || 'secondary'

  return (
    <Card
      className="cursor-pointer hover:shadow-md transition-shadow"
      onClick={onClick}
    >
      <CardContent className="p-4">
        <div className="flex items-start justify-between">
          <div className="flex-1 min-w-0">
            <h3 className="font-semibold truncate">{review.asset_name}</h3>
            <div className="flex items-center gap-4 mt-2 text-sm text-muted-foreground">
              <span className="flex items-center gap-1">
                {review.submitted_by}
              </span>
              <span className="flex items-center gap-1">
                {review.submitted_at ? new Date(review.submitted_at).toLocaleDateString() : '—'}
              </span>
            </div>
            {review.comments && review.comments.length > 0 && (
              <p className="mt-2 text-sm text-muted-foreground line-clamp-2">
                "{review.comments[review.comments.length - 1].body}"
              </p>
            )}
          </div>
          <Badge variant={statusVariant}>{review.status}</Badge>
        </div>
      </CardContent>
    </Card>
  )
}

// ReviewModal component
function ReviewModal({
  review,
  onClose,
  onAction,
}: {
  review: Review
  onClose: () => void
  onAction: () => void
}) {
  const [comment, setComment] = useState('')
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const handleApprove = async () => {
    if (!comment.trim()) {
      setError('Comment is required')
      return
    }
    await submitReview('approve')
  }

  const handleReject = async () => {
    if (!comment.trim()) {
      setError('Comment is required')
      return
    }
    await submitReview('reject')
  }

  const submitReview = async (action: 'approve' | 'reject') => {
    setIsSubmitting(true)
    setError(null)

    try {
      // Get CSRF token from cookie
      const csrfToken = document.cookie
        .split('; ')
        .find(row => row.startsWith('dg_session='))
        ?.split('=')[1] || ''

      const endpoint = action === 'approve'
        ? '/v1/connect/api.v1.GovernanceService/ApproveReview'
        : '/v1/connect/api.v1.GovernanceService/RejectReview'

      const res = await fetch(endpoint, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-CSRF-Token': csrfToken,
        },
        body: JSON.stringify({
          id: review.id,
          comment: comment,
        }),
      })

      if (!res.ok) {
        throw new Error(`Failed to ${action} review`)
      }

      onAction()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'An error occurred')
    } finally {
      setIsSubmitting(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Review: {review.asset_name}</DialogTitle>
        </DialogHeader>

        <div className="space-y-4 py-4">
          {/* Review details */}
          <div className="space-y-2 text-sm">
            <div className="flex justify-between">
              <span className="text-muted-foreground">Submitter</span>
              <span>{review.submitted_by}</span>
            </div>
            <div className="flex justify-between">
              <span className="text-muted-foreground">Submitted</span>
              <span>{review.submitted_at ? new Date(review.submitted_at).toLocaleString() : '—'}</span>
            </div>
            <div className="flex justify-between">
              <span className="text-muted-foreground">Status</span>
              <span className="capitalize">{review.status}</span>
            </div>
          </div>

          {/* Previous comments */}
          {review.comments && review.comments.length > 0 && (
            <div className="border rounded-lg p-3 space-y-2">
              <p className="text-sm font-medium">Comments</p>
              {review.comments.map((c, i) => (
                <div key={i} className="text-sm">
                  <span className="font-medium">{c.author}</span>
                  <span className="text-muted-foreground"> — {c.created_at ? new Date(c.created_at).toLocaleString() : ''}</span>
                  <p className="mt-0.5">{c.body}</p>
                </div>
              ))}
            </div>
          )}

          {/* Comment input */}
          <div className="space-y-2">
            <Label htmlFor="comment">Comment {review.status === 'pending' && '(required)'}</Label>
            <Textarea
              id="comment"
              value={comment}
              onChange={(e) => setComment(e.target.value)}
              placeholder="Enter your review comment..."
              rows={4}
            />
            {error && <p className="text-sm text-destructive">{error}</p>}
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={isSubmitting}>
            Cancel
          </Button>
          {review.status === 'pending' && (
            <>
              <Button variant="destructive" onClick={handleReject} disabled={isSubmitting}>
                {isSubmitting ? <Spinner className="h-4 w-4" /> : 'Reject'}
              </Button>
              <Button onClick={handleApprove} disabled={isSubmitting}>
                {isSubmitting ? <Spinner className="h-4 w-4" /> : 'Approve'}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// GovernancePage component
export function GovernancePage() {
  const [selectedReview, setSelectedReview] = useState<Review | null>(null)
  const [activeTab, setActiveTab] = useState<'pending' | 'approved' | 'rejected'>('pending')
  const queryClient = useQueryClient()

  // Get session to check canApprove permission
  const { data: session } = useQuery<SessionInfo>({
    queryKey: ['me'],
    queryFn: async () => {
      const res = await fetch('/v1/me')
      if (!res.ok) throw new Error('Not authenticated')
      return res.json()
    },
    staleTime: 5 * 60 * 1000,
  })

  const canApprove = session?.permissions?.canApprove ?? false

  // D-17: 15-30s polling for governance inbox (hot screen)
  const { data: pendingData, isLoading: pendingLoading } = useQuery({
    queryKey: ['governance', 'reviews', 'pending'],
    queryFn: async () => {
      const res = await fetch('/v1/connect/api.v1.GovernanceService/ListReviews?status=pending')
      if (!res.ok) throw new Error('Failed to fetch reviews')
      return res.json()
    },
    enabled: canApprove,
    staleTime: 15 * 1000,
    refetchInterval: 20 * 1000,
  })

  const { data: approvedData } = useQuery({
    queryKey: ['governance', 'reviews', 'approved'],
    queryFn: async () => {
      const res = await fetch('/v1/connect/api.v1.GovernanceService/ListReviews?status=approved')
      if (!res.ok) throw new Error('Failed to fetch reviews')
      return res.json()
    },
    enabled: canApprove,
    staleTime: 30 * 1000,
    refetchInterval: 60 * 1000,
  })

  const { data: rejectedData } = useQuery({
    queryKey: ['governance', 'reviews', 'rejected'],
    queryFn: async () => {
      const res = await fetch('/v1/connect/api.v1.GovernanceService/ListReviews?status=rejected')
      if (!res.ok) throw new Error('Failed to fetch reviews')
      return res.json()
    },
    enabled: canApprove,
    staleTime: 30 * 1000,
    refetchInterval: 60 * 1000,
  })

  const handleReviewAction = () => {
    setSelectedReview(null)
    queryClient.invalidateQueries({ queryKey: ['governance', 'reviews'] })
  }

  if (!canApprove) {
    return (
      <div className="flex items-center justify-center h-64">
        <Card className="max-w-md">
          <CardContent className="pt-6 text-center">
            <p className="text-muted-foreground">
              You do not have permission to access the governance inbox.
            </p>
          </CardContent>
        </Card>
      </div>
    )
  }

  const pendingReviews: Review[] = pendingData?.reviews || []
  const approvedReviews: Review[] = approvedData?.reviews || []
  const rejectedReviews: Review[] = rejectedData?.reviews || []

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        <Bell size={20} />
        <h1 className="text-2xl font-bold">Governance Inbox</h1>
      </div>

      <Tabs value={activeTab} onValueChange={(v) => setActiveTab(v as typeof activeTab)}>
        <TabsList>
          <TabsTrigger value="pending">
            Pending
            {pendingReviews.length > 0 && (
              <span className="ml-2 px-1.5 py-0.5 rounded-full bg-destructive text-destructive-foreground text-xs">
                {pendingReviews.length}
              </span>
            )}
          </TabsTrigger>
          <TabsTrigger value="approved">Approved</TabsTrigger>
          <TabsTrigger value="rejected">Rejected</TabsTrigger>
        </TabsList>

        <TabsContent value="pending" className="mt-4">
          {pendingLoading ? (
            <div className="flex justify-center py-12">
              <Spinner />
            </div>
          ) : pendingReviews.length === 0 ? (
            <Card>
              <CardContent className="pt-6 text-center text-muted-foreground">
                No pending reviews.
              </CardContent>
            </Card>
          ) : (
            <div className="space-y-3">
              {pendingReviews.map(review => (
                <ReviewCard
                  key={review.id}
                  review={review}
                  onClick={() => setSelectedReview(review)}
                />
              ))}
            </div>
          )}
        </TabsContent>

        <TabsContent value="approved" className="mt-4">
          <div className="space-y-3">
            {approvedReviews.map(review => (
              <ReviewCard key={review.id} review={review} onClick={() => setSelectedReview(review)} />
            ))}
            {approvedReviews.length === 0 && (
              <Card><CardContent className="pt-6 text-center text-muted-foreground">No approved reviews.</CardContent></Card>
            )}
          </div>
        </TabsContent>

        <TabsContent value="rejected" className="mt-4">
          <div className="space-y-3">
            {rejectedReviews.map(review => (
              <ReviewCard key={review.id} review={review} onClick={() => setSelectedReview(review)} />
            ))}
            {rejectedReviews.length === 0 && (
              <Card><CardContent className="pt-6 text-center text-muted-foreground">No rejected reviews.</CardContent></Card>
            )}
          </div>
        </TabsContent>
      </Tabs>

      {/* Review modal */}
      {selectedReview && (
        <ReviewModal
          review={selectedReview}
          onClose={() => setSelectedReview(null)}
          onAction={handleReviewAction}
        />
      )}
    </div>
  )
}