/**
 * RunCard — one pipeline run rendered as a chat "turn": per-iteration
 * sections with step transcripts, generated HTML / screenshots / critique
 * cards inline, a "regenerating" separator between iterations and a final
 * banner (best design, or failure with retry). Live via the run SSE stream;
 * the same applyRunEvent reducer the run-detail page uses merges events.
 */
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Ban, ListTree, RotateCw } from 'lucide-react'
import { Button, cx } from '../../ui'
import { Callout, Skeleton } from '../../ui/Display'
import { RunProgress } from './RunProgress'
import { useAsync } from '../../hooks'
import {
  cancelRun,
  getRun,
  isTerminalRunStatus,
  streamRunEvents,
  type Run,
  type RunEvent,
  type RunStatus,
} from '../../api/runs'
import { usePipelineBody } from '../runs/usePipelineBody'
import { RunStatusBadge, nodeLabel } from '../runs/shared'
import { DesignStage } from './DesignStage'
import { applyRunEvent } from '../runs/detail'

export function RunCard({
  runId,
  onOpenDetails,
  onRetry,
  onStatus,
  className,
}: {
  runId: string
  /** Opens the full run detail (drawer). */
  onOpenDetails: () => void
  /** Re-run after failure/cancel — the parent re-sends the same message. */
  onRetry?: () => void
  /** Live status relay (the chat page uses it to enable/disable the composer). */
  onStatus?: (runId: string, status: RunStatus) => void
  className?: string
}) {
  const runQ = useAsync((signal) => getRun(runId, signal), [runId])
  const { data: run, setData, reload } = runQ
  const [eventScores, setEventScores] = useState<Record<string, number>>({})
  const [streamError, setStreamError] = useState<unknown>(null)
  const [streamEpoch, setStreamEpoch] = useState(0)
  const [cancelling, setCancelling] = useState(false)
  // node id → what that step is doing right now (live, from step_activity events)
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
      if (ev.event === 'iteration_completed' && typeof ev.best_score === 'number') {
        setEventScores((prev) => ({ ...prev, [String(ev.iteration)]: ev.best_score as number }))
      }
      if (ev.event === 'step_activity') {
        setActivity((prev) => ({ ...prev, [ev.node_id]: ev.label }))
      } else if (ev.event === 'step_completed') {
        setActivity((prev) => {
          const { [ev.node_id]: _gone, ...rest } = prev
          return rest
        })
      }
      setData((prev) => (prev ? applyRunEvent(prev, ev) : prev))
      if (ev.event === 'iteration_completed' || ev.event === 'run_completed' || ev.event === 'run_failed') {
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

  const bodyQ = usePipelineBody(run)

  // Status relay for the chat page (composer busy state).
  useEffect(() => {
    if (status && onStatus) onStatus(runId, status)
  }, [runId, status, onStatus])

  const nodeLabels = useMemo(() => {
    const m = new Map<string, string>()
    for (const n of bodyQ.data?.nodes ?? []) m.set(n.id, nodeLabel(n))
    return m
  }, [bodyQ.data])
  const nodeTypes = useMemo(() => {
    const m = new Map<string, string>()
    for (const n of bodyQ.data?.nodes ?? []) m.set(n.id, n.type)
    return m
  }, [bodyQ.data])

  const iterations = useMemo(() => {
    if (!run) return []
    const set = new Set<number>()
    for (const s of run.steps) set.add(s.iteration)
    for (const a of run.artifacts) set.add(a.iteration)
    for (const k of Object.keys(eventScores)) set.add(Number(k))
    return [...set].sort((a, b) => a - b)
  }, [run, eventScores])

  const scoreFor = useCallback(
    (n: number): number | null => {
      const ev = eventScores[String(n)]
      const art = (run?.artifacts ?? [])
        .filter((a) => a.iteration === n && a.score != null && Number.isFinite(a.score))
        .reduce((acc, a) => Math.max(acc, a.score as number), Number.NEGATIVE_INFINITY)
      const best = Math.max(ev ?? Number.NEGATIVE_INFINITY, art)
      return Number.isFinite(best) ? best : null
    },
    [eventScores, run],
  )

  const doCancel = async (): Promise<void> => {
    setCancelling(true)
    try {
      await cancelRun(runId)
      reload()
    } catch {
      // surfaced through the refreshed run state (failed/cancelled) or stream error
    } finally {
      setCancelling(false)
    }
  }

  if (runQ.error != null) {
    return (
      <Callout tone="danger" title="Couldn't load this run" className={className}>
        {runQ.error instanceof Error ? runQ.error.message : 'Something went wrong.'}
      </Callout>
    )
  }
  if (!run) {
    return <Skeleton className={cx('h-40', className)} />
  }

  const running = !isTerminalRunStatus(run.status)

  const nodeOrder = new Map(bodyQ.data?.nodes.map((node, i) => [node.id, i]) ?? [])
  const bestIteration = bestIterationOf(run, iterations, scoreFor)

  return (
    <div className={cx('flex flex-col gap-2.5', className)}>
      {/* header */}
      <div className="flex items-center gap-2">
        <RunStatusBadge status={run.status} />
        <span className="min-w-0 flex-1 truncate text-xs text-ink-2">
          {run.pipeline.name}
          {run.pipeline.version != null && ` · v${run.pipeline.version}`}
        </span>
        {running && (
          <Button size="sm" variant="secondary" icon={<Ban />} loading={cancelling} onClick={() => void doCancel()}>
            Stop
          </Button>
        )}
        <Button size="sm" variant="ghost" icon={<ListTree />} onClick={onOpenDetails}>
          Details
        </Button>
      </div>

      {streamError != null && (
        <Callout tone="danger" title="Live updates lost">
          <div className="mt-1.5 flex items-center gap-2.5">
            <span>The event stream dropped after several retries — data may be stale.</span>
            <Button
              size="sm"
              variant="secondary"
              icon={<RotateCw />}
              className="shrink-0"
              onClick={() => {
                setStreamError(null)
                setStreamEpoch((e) => e + 1)
              }}
            >
              Retry
            </Button>
          </div>
        </Callout>
      )}

      {run.error && (
        <Callout
          tone={run.status === 'interrupted' ? 'warning' : 'danger'}
          title={run.status === 'interrupted' ? 'Run interrupted' : run.status === 'cancelled' ? 'Run cancelled' : 'Run failed'}
        >
          <div className="mt-1 flex flex-wrap items-center gap-2.5">
            <span className="min-w-0 break-words">{run.error}</span>
            {onRetry && run.status !== 'interrupted' && (
              <Button size="sm" variant="secondary" icon={<RotateCw />} className="shrink-0" onClick={onRetry}>
                Try again
              </Button>
            )}
          </div>
        </Callout>
      )}

      <RunProgress run={run} nodeLabels={nodeLabels} nodeOrder={nodeOrder} activity={activity} />

      {iterations.length === 0 && running && <Skeleton className="h-56" />}
      {iterations.length > 0 && (
        <DesignStage
          run={run}
          iterations={iterations}
          scoreFor={scoreFor}
          nodeLabels={nodeLabels}
          nodeTypes={nodeTypes}
          nodeOrder={nodeOrder}
          running={running}
          bestIteration={bestIteration}
        />
      )}
    </div>
  )
}

/** Best iteration by score (falls back to the last one). */
function bestIterationOf(
  run: Run,
  iterations: number[],
  scoreFor: (n: number) => number | null,
): number | null {
  if (run.best_artifact_id) {
    const art = run.artifacts.find((a) => a.id === run.best_artifact_id)
    if (art) return art.iteration
  }
  let best: number | null = null
  let bestScore = Number.NEGATIVE_INFINITY
  for (const n of iterations) {
    const s = scoreFor(n)
    if (s != null && s > bestScore) {
      bestScore = s
      best = n
    }
  }
  return best
}
