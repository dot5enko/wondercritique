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
import type { RunListItem, RunStatus } from '../../api/runs'

/** One chat thread as shown in the rail. */
export interface ThreadSummary {
  id: string
  title: string
  status: RunStatus
  /** ISO time of the thread's latest run. */
  at: string
  runs: number
}

/** Groups a flat run list into threads (`thread_id ?? id`), newest first. */
export function groupThreads(items: RunListItem[]): ThreadSummary[] {
  const map = new Map<string, { runs: RunListItem[] }>()
  for (const r of items) {
    const key = r.thread_id || r.id
    const bucket = map.get(key) ?? { runs: [] }
    bucket.runs.push(r)
    map.set(key, bucket)
  }
  const threads: ThreadSummary[] = []
  for (const [id, { runs }] of map) {
    const sorted = [...runs].sort((a, b) => a.created_at.localeCompare(b.created_at))
    const first = sorted[0]
    const last = sorted[sorted.length - 1]
    const brief = first.input?.['brief']
    threads.push({
      id,
      title: typeof brief === 'string' && brief.trim() ? brief.trim() : `Chat · ${new Date(first.created_at).toLocaleDateString()}`,
      status: last.status,
      at: last.created_at,
      runs: sorted.length,
    })
  }
  return threads.sort((a, b) => b.at.localeCompare(a.at))
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
