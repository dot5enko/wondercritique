/**
 * Chat thread list, rendered in the Shell sidebar: one entry per chat thread
 * (a thread = a chain of pipeline runs — the first message starts run #1,
 * follow-ups continue the thread). Titles come from the thread's first brief;
 * the status dot mirrors the thread's latest run.
 */
import { RotateCw } from 'lucide-react'
import { Button, Skeleton, cx } from '../../ui'
import { relativeTime } from '../../components'
import { runStatusTone } from '../runs/shared'
import type { RunStatus, ThreadListItem } from '../../api/runs'

/** One chat thread as shown in the rail. */
export interface ThreadSummary {
  id: string
  title: string
  status: RunStatus
  /** ISO time of the thread's latest run. */
  at: string
  runs: number
  styleguideId: string
}

/** Title of a chat: its first brief, else its start date. */
export function threadTitle(brief: string, createdAt: string): string {
  return brief.trim() || `Chat · ${new Date(createdAt).toLocaleDateString()}`
}

/** A `GET /threads` item as shown in the lists. */
export function threadSummary(t: ThreadListItem): ThreadSummary {
  return { id: t.id, title: threadTitle(t.title, t.created_at), status: t.status, at: t.updated_at, runs: t.runs, styleguideId: t.styleguide_id ?? '' }
}

export function ThreadList({
  threads,
  loading,
  error,
  activeId,
  onSelect,
  onRetry,
}: {
  threads: ThreadSummary[]
  /** True only for the first load (keep the list visible while refetching). */
  loading: boolean
  error: unknown
  activeId: string | null
  onSelect: (id: string) => void
  onRetry: () => void
}) {
  if (loading) {
    return (
      <div className="space-y-1.5 px-1">
        <Skeleton className="h-10" />
        <Skeleton className="h-10" />
        <Skeleton className="h-10" />
      </div>
    )
  }
  if (error != null && threads.length === 0) {
    return (
      <div className="flex flex-col items-start gap-1.5 px-2 pt-1">
        <p className="text-xs text-ink-3">Couldn't load chats.</p>
        <Button size="sm" variant="secondary" icon={<RotateCw />} onClick={onRetry}>
          Retry
        </Button>
      </div>
    )
  }
  if (threads.length === 0) {
    return (
      <p className="px-2 pt-1 text-xs leading-relaxed text-ink-3">
        No chats yet. Describe what you want to design to start one.
      </p>
    )
  }
  return (
    <div className="flex flex-col gap-0.5">
      {threads.map((t) => {
        const { tone } = runStatusTone(t.status)
        const active = t.id === activeId
        return (
          <button
            key={t.id}
            type="button"
            onClick={() => onSelect(t.id)}
            aria-current={active || undefined}
            className={cx(
              'group flex w-full cursor-pointer flex-col gap-0.5 rounded-md px-2 py-1.5 text-left outline-none transition-colors duration-100 focus-visible:ring-2 focus-visible:ring-accent/40',
              active ? 'bg-surface shadow-xs' : 'hover:bg-ink/5',
            )}
          >
            <span className="flex items-center gap-1.5">
              <span
                className={cx(
                  'size-1.5 shrink-0 rounded-full',
                  tone === 'accent' && 'animate-pulse bg-accent',
                  tone === 'success' && 'bg-success',
                  tone === 'danger' && 'bg-danger',
                  tone === 'warning' && 'bg-warning',
                  tone === 'neutral' && 'bg-ink-3',
                )}
              />
              <span className={cx('min-w-0 flex-1 truncate text-base', active ? 'text-ink' : 'text-ink-2 group-hover:text-ink')}>
                {t.title}
              </span>
            </span>
            <span className="flex items-center gap-1.5 pl-3 text-2xs text-ink-3">
              <span>{relativeTime(t.at)}</span>
              {t.runs > 1 && <span>· {t.runs} runs</span>}
            </span>
          </button>
        )
      })}
    </div>
  )
}
