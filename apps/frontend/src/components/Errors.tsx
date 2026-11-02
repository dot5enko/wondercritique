/**
 * Error/empty page states. `ErrorState` maps `ApiError` 403/404 to dedicated
 * states; `Loadable` combines skeleton loading, error and content.
 */
import type { ReactNode } from 'react'
import { FileQuestion, RotateCw, ShieldOff } from 'lucide-react'
import { cx } from '../ui/cx'
import { Button, Spinner } from '../ui/Button'
import { Callout, EmptyState, Skeleton } from '../ui/Display'
import { ApiError } from '../api/client'

export function ErrorState({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  if (error instanceof ApiError && error.status === 403) return <ForbiddenState />
  if (error instanceof ApiError && error.status === 404) return <NotFoundState />

  const message =
    error instanceof ApiError
      ? error.message
      : error instanceof Error
        ? error.message
        : 'Something went wrong.'

  return (
    <div className="grid place-items-center p-6">
      <Callout tone="danger" title="Something went wrong" className="max-w-md">
        <div className="mt-1.5 flex items-center gap-2.5">
          <span className="min-w-0 break-words">{message}</span>
          {onRetry && (
            <Button size="sm" variant="secondary" icon={<RotateCw />} onClick={onRetry} className="shrink-0">
              Retry
            </Button>
          )}
        </div>
      </Callout>
    </div>
  )
}

/** True when an error looks like "this part of the backend doesn't exist yet": 404/501 from the API
 * or a network-level failure. Studio/runs endpoints are still being built, so UIs surface a
 * friendlier message for this case instead of a generic error. */
export function isBackendMissing(err: unknown): boolean {
  if (err instanceof ApiError) return err.status === 404 || err.status === 501 || err.status === 503
  return err instanceof TypeError // fetch network failure
}

export function ForbiddenState() {
  return (
    <div className="grid h-full place-items-center p-6">
      <EmptyState
        icon={ShieldOff}
        title="Access denied"
        description="You need admin access to view this page."
      />
    </div>
  )
}

export function NotFoundState() {
  return (
    <div className="grid h-full place-items-center p-6">
      <EmptyState
        icon={FileQuestion}
        title="Not found"
        description="This item doesn't exist or was deleted."
      />
    </div>
  )
}

export interface LoadableProps {
  loading: boolean
  error: unknown
  children: ReactNode
  onRetry?: () => void
  className?: string
}

/** Renders skeletons while loading, an error state on failure, else children. */
export function Loadable({ loading, error, children, onRetry, className }: LoadableProps) {
  if (loading) {
    return (
      <div className={cx('space-y-3', className)} aria-busy="true">
        <Skeleton className="h-6 w-1/3" />
        <Skeleton className="h-24" />
        <Skeleton className="h-24 w-4/5" />
      </div>
    )
  }
  if (error) return <ErrorState error={error} onRetry={onRetry} />
  return <div className={className}>{children}</div>
}

/** Inline busy indicator for small areas (kept next to the error states). */
export function InlineSpinner({ label }: { label?: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-ink-3">
      <Spinner size={13} />
      {label}
    </span>
  )
}
