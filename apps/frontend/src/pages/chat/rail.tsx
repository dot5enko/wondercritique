/**
 * Design-chat rail: one entry per chat thread (a thread = a chain of pipeline
 * runs — the first message starts run #1, follow-ups continue the thread).
 * Titles come from the thread's first brief; the status dot mirrors the
 * thread's latest run.
 */
import { Plus, RotateCw } from 'lucide-react'
import { Button, IconButton, Skeleton, cx } from '../../ui'
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

export function ThreadRail({
  threads,
  loading,
  error,
  activeId,
  creating,
  onNew,
  onSelect,
  onRetry,
}: {
  threads: ThreadSummary[]
  loading: boolean
  error: unknown
  activeId: string | null
  creating: boolean
  onNew: () => void
  onSelect: (id: string) => void
  onRetry: () => void
}) {
  return (
    <div className="flex h-full w-64 shrink-0 flex-col border-r border-line bg-app">
      <div className="flex items-center justify-between gap-2 px-3 py-2.5">
        <p className="text-2xs font-semibold tracking-wide text-ink-3 uppercase">Chats</p>
        <Button size="sm" variant="secondary" icon={<Plus />} loading={creating} onClick={onNew}>
          New
        </Button>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-3">
        {loading ? (
          <div className="space-y-2 px-1">
            <Skeleton className="h-12" />
            <Skeleton className="h-12" />
            <Skeleton className="h-12" />
          </div>
        ) : error != null ? (
          <div className="px-1 pt-2">
            <Button size="sm" variant="secondary" icon={<RotateCw />} onClick={onRetry}>
              Retry
            </Button>
          </div>
        ) : threads.length === 0 ? (
          <p className="px-2 pt-2 text-xs leading-relaxed text-ink-3">
            No chats yet. Describe what you want to design to start one.
          </p>
        ) : (
          <div className="flex flex-col gap-0.5">
            {threads.map((t) => {
              const { tone } = runStatusTone(t.status)
              return (
                <button
                  key={t.id}
                  type="button"
                  onClick={() => onSelect(t.id)}
                  className={cx(
                    'group flex w-full cursor-pointer flex-col gap-0.5 rounded-lg px-2.5 py-2 text-left outline-none transition duration-100',
                    t.id === activeId
                      ? 'bg-accent-soft ring-1 ring-accent/30'
                      : 'hover:bg-surface focus-visible:ring-2 focus-visible:ring-accent/30',
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
                    <span className="min-w-0 flex-1 truncate text-sm font-medium text-ink">{t.title}</span>
                  </span>
                  <span className="flex items-center gap-1.5 pl-3 text-2xs text-ink-3">
                    <span>{relativeTime(t.at)}</span>
                    {t.runs > 1 && <span>· {t.runs} runs</span>}
                  </span>
                </button>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
}

/** Small "new chat" hint button used in the empty thread view. */
export function NewChatHint({ onClick }: { onClick: () => void }) {
  return (
    <IconButton icon={<Plus />} label="New chat" size="sm" onClick={onClick} />
  )
}
