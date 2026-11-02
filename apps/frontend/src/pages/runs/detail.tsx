/**
 * Run detail (plan §5 Runs UI): header with status/usage/cancel, a read-only
 * pipeline graph, iterations (single + compare) with shots / HTML preview /
 * critiques / score chart, and per-node transcripts.
 *
 * Data flow: `getRun` via useAsync; while the run is not terminal the SSE
 * event stream is subscribed and events are merged into the run object
 * (steps-by-node-per-iteration + artifact ids + scores); `iteration_completed`
 * and terminal events trigger a debounced full refresh so artifact metadata
 * lands. Stream drops retry up to 3× with backoff, then surface a Retry.
 */
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useParams } from 'react-router'
import { Ban, RotateCw } from 'lucide-react'
import { Button } from '../../ui/Button'
import { Badge, Callout, Card, CardBody, CardHeader, EmptyState, Skeleton } from '../../ui/Display'
import { Segmented } from '../../ui/Form'
import { ConfirmDialog, ErrorState, InlineSpinner, PageHeader, relativeTime } from '../../components'
import { useAsync } from '../../hooks'
import {
  cancelRun,
  getRun,
  isTerminalRunStatus,
  streamRunEvents,
  type Run,
  type RunEvent,
  type RunStep,
} from '../../api/runs'
import { getDef, getVersion, type PipelineBody } from '../../api/studio'
import { RunGraph, type RunNodeStepStatus } from './RunGraph'
import { ScoreChart } from './ScoreChart'
import { IterationPanel } from './IterationPanel'
import { CompareView } from './CompareView'
import { StepsAccordion } from './StepsAccordion'
import { RunStatusBadge, StatChip, formatCost, formatTokens, nodeLabel, shortId } from './shared'

/* ---------- SSE event → run merge ---------- */

function upsertStep(
  run: Run,
  iteration: number,
  nodeId: string,
  patch: Partial<RunStep> & { status: RunStep['status'] },
): Run {
  const idx = run.steps.findIndex((s) => s.iteration === iteration && s.node_id === nodeId)
  if (idx === -1) {
    const step: RunStep = {
      id: `live:${run.id}:${iteration}:${nodeId}`,
      run_id: run.id,
      iteration,
      node_id: nodeId,
      ...patch,
    }
    return { ...run, steps: [...run.steps, step] }
  }
  const steps = run.steps.slice()
  steps[idx] = { ...steps[idx], ...patch }
  return { ...run, steps }
}

export function applyRunEvent(run: Run, ev: RunEvent): Run {
  switch (ev.event) {
    case 'step_started':
      return upsertStep(run, ev.iteration, ev.node_id, { status: 'running', error: null })
    case 'step_completed': {
      const patch: Partial<RunStep> & { status: RunStep['status'] } = {
        status: ev.status,
        usage: ev.usage ?? null,
        error: ev.error ?? null,
      }
      if (ev.artifact_ids) patch.artifact_ids = ev.artifact_ids
      return upsertStep(run, ev.iteration, ev.node_id, patch)
    }
    case 'run_completed':
      return {
        ...run,
        status: 'completed',
        best_artifact_id: ev.best_artifact_id ?? run.best_artifact_id,
        usage: ev.usage ?? run.usage,
        cost: ev.cost ?? run.cost,
        updated_at: new Date().toISOString(),
      }
    case 'run_failed':
      return { ...run, status: 'failed', error: ev.error, updated_at: new Date().toISOString() }
    default:
      // iteration_started / iteration_completed only feed scores + refreshes
      return run
  }
}

export default function RunDetailPage() {
  const { id = '' } = useParams()
  return <RunDetailView runId={id} />
}

/** The whole run detail UI, parameterized by run id so the chat can embed it in a drawer. */
export function RunDetailView({ runId: id }: { runId: string }) {
  const runQ = useAsync((signal) => getRun(id, signal), [id])
  const { data: run, setData, reload } = runQ

  const [eventScores, setEventScores] = useState<Record<string, number>>({})
  const [streamError, setStreamError] = useState<unknown>(null)
  const [streamEpoch, setStreamEpoch] = useState(0)
  const [mode, setMode] = useState<'single' | 'compare'>('single')
  const [pinnedIter, setPinnedIter] = useState<string | null>(null)
  const [cancelOpen, setCancelOpen] = useState(false)
  const [cancelBusy, setCancelBusy] = useState(false)
  const [cancelError, setCancelError] = useState<unknown>(null)

  // Per-run UI state resets when navigating between runs.
  useEffect(() => {
    setEventScores({})
    setStreamError(null)
    setStreamEpoch(0)
    setMode('single')
    setPinnedIter(null)
    setCancelError(null)
  }, [id])

  /* ---------- live event stream ---------- */

  const status = run?.status
  useEffect(() => {
    if (!id || !status || isTerminalRunStatus(status)) return
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
      if (ev.event === 'iteration_completed') {
        const score = ev.best_score
        if (typeof score === 'number') {
          setEventScores((prev) => ({ ...prev, [String(ev.iteration)]: score }))
        }
      }
      setData((prev) => (prev ? applyRunEvent(prev, ev) : prev))
      if (
        ev.event === 'iteration_completed' ||
        ev.event === 'run_completed' ||
        ev.event === 'run_failed'
      ) {
        scheduleRefresh()
      }
    }

    const connect = async (): Promise<void> => {
      while (!cancelled && !ctrl.signal.aborted) {
        attempt++
        try {
          await streamRunEvents(id, { signal: ctrl.signal, onEvent })
          // Clean close: the run may have just finished server-side. Refresh
          // (flips the status and tears this effect down) and reconnect once
          // the debounce window has passed.
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
  }, [id, status, streamEpoch, reload, setData])

  /* ---------- pinned pipeline body (graph + labels) ---------- */

  const pipelineId = run?.pipeline.id
  const pipelineVersion = run?.pipeline.version
  const bodyQ = useAsync(
    async (signal): Promise<PipelineBody | null> => {
      if (!pipelineId) return null
      if (pipelineVersion != null) {
        const ver = await getVersion('pipeline', pipelineId, pipelineVersion, signal).catch(() => null)
        if (ver) return ver.body
      }
      const def = await getDef('pipeline', pipelineId, signal).catch(() => null)
      return def?.draft ?? null
    },
    [pipelineId, pipelineVersion],
  )

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

  /* ---------- derived iteration data ---------- */

  const maxIter = useMemo(() => {
    if (!run) return 0
    let m = run.iteration
    for (const s of run.steps) m = Math.max(m, s.iteration)
    for (const a of run.artifacts) m = Math.max(m, a.iteration)
    for (const k of Object.keys(eventScores)) m = Math.max(m, Number(k))
    return m
  }, [run, eventScores])

  const iterations = useMemo(() => Array.from({ length: maxIter }, (_, i) => i + 1), [maxIter])

  const artifactScoreByIter = useMemo(() => {
    const m = new Map<number, number>()
    if (!run) return m
    for (const a of run.artifacts) {
      if (a.score == null || !Number.isFinite(a.score)) continue
      m.set(a.iteration, Math.max(m.get(a.iteration) ?? Number.NEGATIVE_INFINITY, a.score))
    }
    return m
  }, [run])

  const scoreFor = useCallback(
    (n: number): number | null => {
      const ev = eventScores[String(n)]
      const art = artifactScoreByIter.get(n)
      const best = Math.max(ev ?? Number.NEGATIVE_INFINITY, art ?? Number.NEGATIVE_INFINITY)
      return Number.isFinite(best) ? best : null
    },
    [eventScores, artifactScoreByIter],
  )

  const bestIteration = useMemo<number | null>(() => {
    if (!run) return null
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
  }, [run, iterations, scoreFor])

  // Follows the latest iteration while the user hasn't pinned a selection.
  const selectedIter: number | 'best' =
    pinnedIter === 'best' ? 'best' : pinnedIter != null ? Number(pinnedIter) : Math.max(maxIter, 1)
  const panelIter = selectedIter === 'best' ? (bestIteration ?? Math.max(maxIter, 1)) : selectedIter

  const graphStatuses = useMemo(() => {
    const m: Record<string, RunNodeStepStatus> = {}
    if (!run) return m
    for (const s of run.steps) {
      if (s.iteration === panelIter) m[s.node_id] = s.status
    }
    return m
  }, [run, panelIter])

  const scorePoints = useMemo(
    () =>
      iterations
        .map((n) => ({ iteration: n, score: scoreFor(n) }))
        .filter((p): p is { iteration: number; score: number } => p.score != null),
    [iterations, scoreFor],
  )

  const iterOptions = useMemo(
    () => [
      ...iterations.map((n) => ({ value: String(n), label: `Iter ${n}` })),
      { value: 'best', label: bestIteration != null ? `Best (${bestIteration})` : 'Best' },
    ],
    [iterations, bestIteration],
  )

  const panelSteps = useMemo(
    () => (run ? run.steps.filter((s) => s.iteration === panelIter) : []),
    [run, panelIter],
  )

  /* ---------- actions ---------- */

  const doCancel = async (): Promise<void> => {
    setCancelBusy(true)
    try {
      await cancelRun(id)
      setCancelOpen(false)
      reload()
    } catch (err: unknown) {
      setCancelError(err)
    } finally {
      setCancelBusy(false)
    }
  }

  const running = run != null && !isTerminalRunStatus(run.status)

  /* ---------- render ---------- */

  return (
    <div className="p-5">
      <PageHeader
        back={{ to: '/chat', label: 'Design chat' }}
        title={
          <span className="flex min-w-0 flex-wrap items-center gap-2">
            <span className="truncate">{run?.pipeline.name ?? 'Run'}</span>
            {run && <RunStatusBadge status={run.status} />}
            {run?.is_test && <Badge tone="neutral">test</Badge>}
          </span>
        }
        description={
          run
            ? `Run ${shortId(run.id)}` +
              (run.pipeline.version != null ? ` · pipeline v${run.pipeline.version}` : '') +
              ` · started ${relativeTime(run.created_at)}` +
              (isTerminalRunStatus(run.status) && run.updated_at
                ? ` · finished ${relativeTime(run.updated_at)}`
                : '')
            : undefined
        }
        actions={
          run && (
            <div className="flex items-center gap-1.5">
              {run.usage && (
                <>
                  <StatChip
                    title={`Input tokens: ${run.usage.input_tokens} (cached ${run.usage.cached_input_tokens})`}
                  >
                    {formatTokens(run.usage.input_tokens)} in
                  </StatChip>
                  <StatChip
                    title={`Output tokens: ${run.usage.output_tokens} (reasoning ${run.usage.reasoning_tokens})`}
                  >
                    {formatTokens(run.usage.output_tokens)} out
                  </StatChip>
                </>
              )}
              {run.cost != null && <StatChip title="Total cost">{formatCost(run.cost)}</StatChip>}
              <StatChip title="Iterations">{maxIter} iters</StatChip>
              {running && (
                <button
                  type="button"
                  onClick={() => {
                    setCancelError(null)
                    setCancelOpen(true)
                  }}
                  className="inline-flex h-6 cursor-pointer items-center gap-1.5 rounded-md border border-danger/30 bg-surface px-2 text-xs font-medium text-danger outline-none transition duration-100 hover:bg-danger-soft active:scale-[0.98] focus-visible:ring-2 focus-visible:ring-danger/30 [&_svg]:size-3.5"
                >
                  <Ban />
                  Cancel
                </button>
              )}
            </div>
          )
        }
      />

      {cancelError != null && (
        <Callout tone="danger" title="Couldn't cancel the run" className="mt-3">
          {cancelError instanceof Error ? cancelError.message : 'Something went wrong.'}
        </Callout>
      )}
      {streamError != null && (
        <Callout tone="danger" title="Live updates lost" className="mt-3">
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
      {run?.error && (
        <Callout
          tone="danger"
          title={run.status === 'interrupted' ? 'Run interrupted' : 'Run failed'}
          className="mt-3"
        >
          {run.error}
        </Callout>
      )}

      {!run ? (
        runQ.error ? (
          <div className="mt-4">
            <ErrorState error={runQ.error} onRetry={reload} />
          </div>
        ) : (
          <div className="mt-4 space-y-4">
            <Skeleton className="h-64" />
            <Skeleton className="h-40" />
            <Skeleton className="h-64" />
          </div>
        )
      ) : (
        <>
          {/* Pipeline graph */}
          <Card className="mt-4">
            <CardHeader
              title="Pipeline"
              description={
                run.pipeline.version != null ? `Pinned version v${run.pipeline.version}` : undefined
              }
            />
            <div className="h-64 border-t border-line">
              {bodyQ.loading ? (
                <Skeleton className="m-4 h-56" />
              ) : (
                <RunGraph body={bodyQ.data} statusByNode={graphStatuses} />
              )}
            </div>
          </Card>

          {/* Iterations */}
          <Card className="mt-4">
            <CardHeader
              title="Iterations"
              actions={
                <Segmented
                  options={[
                    { value: 'single', label: 'Iterations' },
                    { value: 'compare', label: 'Compare' },
                  ]}
                  value={mode}
                  onChange={setMode}
                />
              }
            />
            <CardBody className="p-4">
              {maxIter === 0 ? (
                running ? (
                  <InlineSpinner label="Waiting for the engine to schedule the first iteration…" />
                ) : (
                  <EmptyState
                    title="No iterations recorded"
                    description="This run never got past scheduling."
                  />
                )
              ) : mode === 'compare' ? (
                <CompareView
                  run={run}
                  iterations={iterations}
                  scoreFor={scoreFor}
                  nodeLabels={nodeLabels}
                  defaultA={bestIteration ?? 1}
                  defaultB={Math.max(maxIter, 1)}
                />
              ) : (
                <div className="flex flex-col gap-4">
                  <div className="flex flex-wrap items-center gap-2">
                    <Segmented
                      options={iterOptions}
                      value={selectedIter === 'best' ? 'best' : String(selectedIter)}
                      onChange={setPinnedIter}
                    />
                    {running && selectedIter === Math.max(maxIter, 1) && (
                      <span className="inline-flex items-center gap-1.5 text-2xs text-ink-3">
                        latest
                      </span>
                    )}
                  </div>
                  {scorePoints.length > 0 && (
                    <ScoreChart
                      points={scorePoints}
                      highlight={panelIter}
                      onSelect={(n) => setPinnedIter(String(n))}
                    />
                  )}
                  <IterationPanel run={run} iteration={panelIter} nodeLabels={nodeLabels} />
                </div>
              )}
            </CardBody>
          </Card>

          {/* Node transcripts (selected iteration) */}
          {mode === 'single' && maxIter > 0 && (
            <Card className="mt-4">
              <CardHeader title="Node transcripts" description={`Steps of iteration ${panelIter}`} />
              <CardBody className="p-3">
                <StepsAccordion steps={panelSteps} nodeLabels={nodeLabels} nodeTypes={nodeTypes} />
              </CardBody>
            </Card>
          )}
        </>
      )}

      <ConfirmDialog
        open={cancelOpen}
        onClose={() => setCancelOpen(false)}
        title="Cancel this run?"
        body="The run will be marked as cancelled. Steps that already finished keep their results."
        confirmLabel="Cancel run"
        danger
        busy={cancelBusy}
        onConfirm={() => void doCancel()}
      />
    </div>
  )
}
