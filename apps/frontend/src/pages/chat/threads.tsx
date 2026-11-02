/**
 * Shared chat-thread list. The Shell sidebar renders it and the design chat
 * page reads/reloads it (after sending, when a run finishes), so both stay in
 * sync off a single `listRuns` query. Provided by the Shell around the whole
 * authenticated app.
 */
import { createContext, useContext, useMemo, type ReactNode } from 'react'
import { useAsync, type UseAsyncResult } from '../../hooks'
import { listRuns } from '../../api/runs'
import { groupThreads, type ThreadSummary } from './rail'

interface ThreadsValue {
  /** Raw runs query (latest 100 runs). */
  query: UseAsyncResult<Awaited<ReturnType<typeof listRuns>>>
  threads: ThreadSummary[]
}

const ThreadsContext = createContext<ThreadsValue | null>(null)

export function ThreadsProvider({ children }: { children: ReactNode }) {
  const query = useAsync((signal) => listRuns({ limit: 100 }, signal), [])
  const threads = useMemo(() => groupThreads(query.data?.items ?? []), [query.data])
  const value = useMemo(() => ({ query, threads }), [query, threads])
  return <ThreadsContext.Provider value={value}>{children}</ThreadsContext.Provider>
}

export function useThreads(): ThreadsValue {
  const v = useContext(ThreadsContext)
  if (!v) throw new Error('useThreads must be used inside <ThreadsProvider>')
  return v
}
