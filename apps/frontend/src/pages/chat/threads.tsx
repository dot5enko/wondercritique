/**
 * Chat threads. `ThreadsProvider`/`useThreads()` feed the Shell sidebar (the
 * latest chats, one `GET /threads` page); `usePagedThreads()` drives the chat
 * list page (search + "Load more" chunks).
 */
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useAsync, type UseAsyncResult } from '../../hooks'
import { listThreads } from '../../api/runs'
import { threadSummary, type ThreadSummary } from './rail'

interface ThreadsValue {
  /** Raw threads query (the latest 50 chats). */
  query: UseAsyncResult<Awaited<ReturnType<typeof listThreads>>>
  threads: ThreadSummary[]
}

const ThreadsContext = createContext<ThreadsValue | null>(null)

export function ThreadsProvider({ children }: { children: ReactNode }) {
  const query = useAsync((signal) => listThreads({ limit: 50 }, signal), [])
  const threads = useMemo(() => (query.data?.items ?? []).map(threadSummary), [query.data])
  const value = useMemo(() => ({ query, threads }), [query, threads])
  return <ThreadsContext.Provider value={value}>{children}</ThreadsContext.Provider>
}

export function useThreads(): ThreadsValue {
  const v = useContext(ThreadsContext)
  if (!v) throw new Error('useThreads must be used inside <ThreadsProvider>')
  return v
}

/** Chats per "Load more" chunk. */
export const THREADS_PAGE = 20

export interface PagedThreads {
  threads: ThreadSummary[]
  /** First chunk of the current search in flight (show skeletons when empty). */
  loading: boolean
  /** A further chunk in flight. */
  loadingMore: boolean
  error: unknown
  hasMore: boolean
  loadMore: () => void
  /** Reloads the current search from the first chunk. */
  reload: () => void
}

/**
 * The chat list for `search`, loaded in chunks of `THREADS_PAGE`. A new search
 * restarts from the first chunk; responses of a superseded search are dropped.
 */
export function usePagedThreads(search: string): PagedThreads {
  const [threads, setThreads] = useState<ThreadSummary[]>([])
  const [next, setNext] = useState<number | null>(null)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [epoch, setEpoch] = useState(0)
  const ctrl = useRef<AbortController | null>(null)

  const fetchChunk = useCallback(async (offset: number): Promise<void> => {
    ctrl.current?.abort()
    const c = new AbortController()
    ctrl.current = c
    if (offset === 0) setLoading(true)
    else setLoadingMore(true)
    setError(null)
    try {
      const res = await listThreads({ limit: THREADS_PAGE, offset, q: search.trim() }, c.signal)
      if (c.signal.aborted) return
      const page = res.items.map(threadSummary)
      setThreads((prev) => {
        if (offset === 0) return page
        // Activity between chunks shifts the order — never list a chat twice.
        const seen = new Set(prev.map((t) => t.id))
        return [...prev, ...page.filter((t) => !seen.has(t.id))]
      })
      setNext(res.next_offset)
    } catch (err: unknown) {
      if (!c.signal.aborted) setError(err)
    } finally {
      if (!c.signal.aborted) {
        setLoading(false)
        setLoadingMore(false)
      }
    }
  }, [search])

  useEffect(() => {
    void fetchChunk(0)
    return () => ctrl.current?.abort()
  }, [fetchChunk, epoch])

  const loadMore = useCallback(() => {
    if (next != null && !loading && !loadingMore) void fetchChunk(next)
  }, [next, loading, loadingMore, fetchChunk])
  const reload = useCallback(() => setEpoch((n) => n + 1), [])

  return { threads, loading, loadingMore, error, hasMore: next != null, loadMore, reload }
}
