/**
 * RunProgress — the live "what is happening and how long did it take" strip of
 * a run, deliberately quiet and borderless: a large light timer, a thin flowing
 * line with one segment per step (the running one shimmers) and a single soft
 * activity phrase. Finished runs keep the per-step times under the line.
 */
import { useEffect, useState } from 'react'
import { cx } from '../../ui'
import type { Run, RunStep } from '../../api/runs'
import { isTerminalRunStatus } from '../../api/runs'

/** Re-render every `ms` while `active`; returns the current time. */
function useNow(active: boolean, ms = 100): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    setNow(Date.now())
    const t = window.setInterval(() => setNow(Date.now()), ms)
    return () => window.clearInterval(t)
  }, [active, ms])
  return now
}

function ms(iso?: string | null): number | null {
  if (!iso) return null
  const t = Date.parse(iso)
  return Number.isFinite(t) ? t : null
}

/** 83.4s → { main: "1:23", tenth: "4" }. */
function clock(totalMs: number): { main: string; tenth: string } {
  const t = Math.max(0, totalMs)
  const m = Math.floor(t / 60000)
  const s = Math.floor((t % 60000) / 1000)
  return { main: `${m}:${String(s).padStart(2, '0')}`, tenth: String(Math.floor((t % 1000) / 100)) }
}

function short(totalMs: number): string {
  const t = Math.max(0, totalMs)
  if (t < 60000) return `${(t / 1000).toFixed(1)}s`
  return `${Math.floor(t / 60000)}m ${String(Math.floor((t % 60000) / 1000)).padStart(2, '0')}s`
}

function stepEnd(s: RunStep, now: number): number | null {
  const start = ms(s.started_at)
  if (start == null) return null
  return ms(s.completed_at) ?? (s.status === 'running' ? now : start)
}

export function RunProgress({
  run,
  nodeLabels,
  nodeOrder,
  activity,
}: {
  run: Run
  nodeLabels: Map<string, string>
  nodeOrder: Map<string, number>
  /** node id → live label of what that step is doing. */
  activity: Record<string, string>
}) {
  const running = !isTerminalRunStatus(run.status)
  const now = useNow(running)

  const start = ms(run.created_at) ?? now
  const lastStepEnd = run.steps.reduce((acc, s) => Math.max(acc, ms(s.completed_at) ?? 0), 0)
  const end = running ? now : (ms(run.updated_at) ?? (lastStepEnd || now))
  const elapsed = Math.max(0, end - start)
  const t = clock(elapsed)

  const steps = run.steps
    .filter((s) => s.started_at)
    .sort(
      (a, b) =>
        (ms(a.started_at) ?? 0) - (ms(b.started_at) ?? 0) ||
        (nodeOrder.get(a.node_id) ?? 99) - (nodeOrder.get(b.node_id) ?? 99),
    )
  const active = steps.filter((s) => s.status === 'running')
  const phrase = active
    .map((s) => activity[s.node_id])
    .filter(Boolean)
    .pop()
  const failed = run.status === 'failed' || run.status === 'cancelled' || run.status === 'interrupted'

  return (
    <div className="px-0.5 pb-1 pt-2">
      <div className="flex items-baseline gap-x-4 gap-y-1">
        <div
          className={cx(
            'text-[34px] font-extralight leading-none tracking-tight tabular-nums',
            running ? 'text-ink' : failed ? 'text-ink-3' : 'text-accent-ink',
          )}
        >
          {t.main}
          <span className="ml-0.5 text-lg text-ink-3">.{t.tenth}</span>
        </div>
        <div className="min-w-0 flex-1 truncate text-sm">
          {running ? (
            <span className="shimmer-text">
              {phrase ??
                (active.length > 0 ? `Working on ${nodeLabels.get(active[0].node_id) ?? active[0].node_id}…` : 'Getting started…')}
            </span>
          ) : (
            <span className="text-ink-3">{failed ? `Stopped after ${short(elapsed)}` : 'Done'}</span>
          )}
        </div>
      </div>

      {steps.length > 0 && (
        <ol className="mt-3 flex gap-1">
          {steps.map((s) => {
            const a = ms(s.started_at)
            const b = stepEnd(s, now)
            const dur = a != null && b != null ? b - a : null
            const live = s.status === 'running'
            const bad = s.status === 'failed' || s.status === 'cancelled' || s.status === 'interrupted'
            return (
              <li key={s.id} className="min-w-0 flex-1 basis-0" style={{ flexGrow: Math.max(1, Math.min(4, (dur ?? 0) / 4000 + 1)) }}>
                <div
                  className={cx(
                    'h-[3px] rounded-full',
                    live ? 'shimmer-bar' : bad ? 'bg-danger/60' : 'bg-accent-muted',
                  )}
                />
                <div className="mt-1.5 flex items-baseline justify-between gap-1.5 text-2xs">
                  <span className={cx('truncate', live ? 'text-ink-2' : 'text-ink-3')}>
                    {nodeLabels.get(s.node_id) ?? s.node_id}
                  </span>
                  {dur != null && <span className="shrink-0 tabular-nums text-ink-3">{short(dur)}</span>}
                </div>
              </li>
            )
          })}
        </ol>
      )}
    </div>
  )
}
