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
import { Badge, Callout, Skeleton } from '../../ui/Display'
import { InlineSpinner } from '../../components/Errors'
import { useAsync } from '../../hooks'
import {
  cancelRun,
  getRun,
  isTerminalRunStatus,
  streamRunEvents,
  type Artifact,
  type Run,
  type RunEvent,
  type RunStatus,
} from '../../api/runs'
import { usePipelineBody } from '../runs/usePipelineBody'
import { GOOD_SCORE, RunStatusBadge, formatScore, nodeLabel } from '../runs/shared'
import { HtmlArtifactCard, ArtifactCritiqueCard, ShotsGrid, useArtifactContent } from '../runs/ArtifactViews'
import { StepsAccordion } from '../runs/StepsAccordion'
import { applyRunEvent } from '../runs/detail'

/** Final-design preview: the best artifact rendered large (html or image). */
function BestArtifact({ artifact }: { artifact: Artifact }) {
  const { content } = useArtifactContent(artifact.id)
  return (
    <div className="overflow-hidden rounded-lg border border-accent/40">
      {artifact.kind === 'image' && content?.kind === 'image' ? (
        <img src={content.url} alt="Final design" className="w-full bg-white" />
      ) : content?.text != null ? (
        <iframe srcDoc={content.text} sandbox="" title="Final design" className="h-96 w-full bg-white" />
      ) : (
        <Skeleton className="h-40" />
      )}
    </div>
  )
}

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
  const bestArtifact = run.best_artifact_id
    ? (run.artifacts.find((a) => a.id === run.best_artifact_id) ?? null)
    : null

  return (
    <div className={cx('overflow-hidden rounded-xl border border-line bg-surface', className)}>
      {/* header */}
      <div className="flex items-center gap-2 border-b border-line bg-app px-3 py-2">
        <RunStatusBadge status={run.status} />
        <span className="min-w-0 flex-1 truncate text-xs text-ink-2">
          {run.pipeline.name}
          {run.pipeline.version != null && ` · v${run.pipeline.version}`}
        </span>
        {running && (
          <Button
            size="sm"
            variant="secondary"
            icon={<Ban />}
            loading={cancelling}
            onClick={() => void doCancel()}
          >
            Stop
          </Button>
        )}
        <Button size="sm" variant="ghost" icon={<ListTree />} onClick={onOpenDetails}>
          Details
        </Button>
      </div>

      <div className="flex flex-col gap-3 p-3">
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

        {/* iterations */}
        {iterations.length === 0 && running && (
          <InlineSpinner label="Waiting for the engine to schedule the first iteration…" />
        )}
        {iterations.map((n) => {
          const steps = run.steps.filter((s) => s.iteration === n)
          const arts = run.artifacts.filter((a) => a.iteration === n)
          const score = scoreFor(n)
          const anyRunning = steps.some((s) => s.status === 'running')
          const order = new Map(bodyQ.data?.nodes.map((node, i) => [node.id, i]) ?? [])
          const sortedArts = [...arts].sort(
            (a, b) => (order.get(a.node_id) ?? 99) - (order.get(b.node_id) ?? 99),
          )
          return (
            <div key={n} className="flex flex-col gap-2">
              {n > 1 && (
                <p className="flex items-center gap-2 text-xs text-ink-3">
                  <span className="h-px flex-1 bg-line" />
                  {scoreFor(n - 1) != null
                    ? `Score ${formatScore(scoreFor(n - 1))} — critic requested changes`
                    : 'Critic requested changes'}
                  <span className="h-px flex-1 bg-line" />
                </p>
              )}
              <div className="flex items-center gap-2">
                <p className="text-2xs font-semibold tracking-wide text-ink-3 uppercase">Iteration {n}</p>
                {score != null && <Badge tone={score >= GOOD_SCORE ? 'success' : 'neutral'}>score {formatScore(score)}</Badge>}
                {anyRunning && <Badge tone="accent">running</Badge>}
              </div>

              {sortedArts.length > 0 && (
                <div className="flex flex-col gap-2">
                  {sortedArts.map((a) => {
                    if (a.kind === 'image') {
                      return <ShotsGrid key={a.id} artifacts={[a]} bestArtifactId={run.best_artifact_id} />
                    }
                    if (a.kind === 'html') {
                      return <HtmlArtifactCard key={a.id} artifact={a} title={`${nodeLabels.get(a.node_id) ?? a.node_id} — HTML`} />
                    }
                    if (nodeTypes.get(a.node_id) === 'agent') {
                      return <ArtifactCritiqueCard key={a.id} artifact={a} title={nodeLabels.get(a.node_id) ?? a.node_id} />
                    }
                    return null
                  })}
                </div>
              )}

              {steps.length > 0 && <StepsAccordion steps={steps} nodeLabels={nodeLabels} nodeTypes={nodeTypes} />}
            </div>
          )
        })}

        {/* final banner */}
        {run.status === 'completed' && (
          <div className="rounded-lg border border-success/30 bg-success-soft px-3 py-2.5">
            <p className="flex flex-wrap items-center gap-2 text-sm font-medium text-ink">
              Done
              {bestIterationOf(run, iterations, scoreFor) != null && (
                <Badge tone="success">best · iteration {bestIterationOf(run, iterations, scoreFor)}</Badge>
              )}
              {bestArtifact?.score != null && <Badge tone="accent">score {formatScore(bestArtifact.score)}</Badge>}
            </p>
            {bestArtifact && (
              <div className="mt-2">
                <BestArtifact artifact={bestArtifact} />
              </div>
            )}
          </div>
        )}
      </div>
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
