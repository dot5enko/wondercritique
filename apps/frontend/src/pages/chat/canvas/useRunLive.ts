/**
 * One run, kept live: `getRun` + the SSE event stream (merged with the shared
 * `applyRunEvent` reducer) + a debounced refetch after iteration/terminal
 * events (SSE step events only carry artifact ids, the refetch brings their
 * kinds and the final `results`). Reconnects up to 3× before reporting
 * `streamError`.
 */
import { useEffect, useState } from 'react'
import { useAsync } from '../../../hooks'
import { getRun, isTerminalRunStatus, streamRunEvents, type Run, type RunEvent } from '../../../api/runs'
import { applyRunEvent } from '../../runs/detail'

export interface RunLive {
  run: Run | null
  error: unknown
  /** node id → what that step is doing right now (`step_activity`). */
  activity: Record<string, string>
  streamError: unknown
  retryStream: () => void
  reload: () => void
}

export function useRunLive(runId: string): RunLive {
  const runQ = useAsync((signal) => getRun(runId, signal), [runId])
  const { data: run, setData, reload } = runQ
  const [streamError, setStreamError] = useState<unknown>(null)
  const [streamEpoch, setStreamEpoch] = useState(0)
  const [activity, setActivity] = useState<Record<string, string>>({})

  const status = run?.status
  useEffect(() => {
    if (!status || isTerminalRunStatus(status)) return
    const ctrl = new AbortController()
    let cancelled = false
    let attempt = 0
    let refreshTimer: number | undefined

    const scheduleRefresh = (): void => {
      window.clearTimeout(refreshTimer)
      refreshTimer = window.setTimeout(() => {
        if (!cancelled && !ctrl.signal.aborted) reload()
      }, 350)
    }
    const onEvent = (ev: RunEvent): void => {
      if (ev.event === 'step_activity') {
        setActivity((prev) => ({ ...prev, [ev.node_id]: ev.label }))
      } else if (ev.event === 'step_completed') {
        setActivity((prev) => {
          const { [ev.node_id]: _gone, ...rest } = prev
          return rest
        })
      }
      setData((prev) => (prev ? applyRunEvent(prev, ev) : prev))
      if (ev.event === 'step_completed' || ev.event === 'iteration_completed' || ev.event === 'run_completed' || ev.event === 'run_failed') {
        scheduleRefresh()
      }
    }
    const connect = async (): Promise<void> => {
      while (!cancelled && !ctrl.signal.aborted) {
        attempt++
        try {
          await streamRunEvents(runId, { signal: ctrl.signal, onEvent })
          scheduleRefresh()
          if (attempt >= 8) return
        } catch (err) {
          if (cancelled || ctrl.signal.aborted) return
          if (err instanceof DOMException && err.name === 'AbortError') return
          if (attempt >= 3) {
            setStreamError(err)
            return
          }
        }
        await new Promise((resolve) => window.setTimeout(resolve, 600))
      }
    }
    void connect()
    return () => {
      cancelled = true
      window.clearTimeout(refreshTimer)
      ctrl.abort()
    }
  }, [runId, status, streamEpoch, reload, setData])

  return {
    run: run ?? null,
    error: runQ.error,
    activity,
    streamError,
    retryStream: () => {
      setStreamError(null)
      setStreamEpoch((e) => e + 1)
    },
    reload,
  }
}

/** One component a run shows on the canvas. */
export interface RunComponent {
  /** html artifact id — what selections and `context_artifact_ids` carry. */
  id: string
  score: number | null
  /** Intermediate output (run still working, or it failed before a result). */
  draft: boolean
}

/**
 * The components of a run: its final `results` (one or many), else the best
 * artifact, else — while running or after a failure — the html artifacts of
 * the latest iteration as drafts.
 */
export function componentsOf(run: Run): RunComponent[] {
  if (run.results && run.results.length > 0) {
    return [...run.results]
      .sort((a, b) => a.position - b.position)
      .map((r) => ({ id: r.artifact_id, score: r.score, draft: false }))
  }
  const html = run.artifacts.filter((a) => a.kind === 'html')
  const done = isTerminalRunStatus(run.status)
  if (done && run.status === 'completed' && run.best_artifact_id) {
    const best = html.find((a) => a.id === run.best_artifact_id)
    return [{ id: run.best_artifact_id, score: best?.score ?? null, draft: false }]
  }
  if (html.length === 0) return []
  const last = Math.max(...html.map((a) => a.iteration))
  return html
    .filter((a) => a.iteration === last)
    .map((a) => ({ id: a.id, score: a.score ?? null, draft: true }))
}
