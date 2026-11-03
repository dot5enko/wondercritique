/**
 * One message of the chat on the canvas = one run = one group: a transparent
 * rounded block holding every component the run produced, headed by the
 * message itself (+ the components it referred to), live progress, Stop /
 * Details and failure handling. The block is selectable as a whole (`g:<run>`)
 * — selecting it means "all its components".
 */
import { useEffect, useMemo } from 'react'
import { Ban, ListTree, RotateCw } from 'lucide-react'
import { Button, cx } from '../../../ui'
import { cancelRun, isTerminalRunStatus, type Run, type RunStatus } from '../../../api/runs'
import { usePipelineBody } from '../../runs/usePipelineBody'
import { nodeLabel } from '../../runs/shared'
import { RunProgress } from '../RunProgress'
import { ComponentFrame, FRAME_WIDTH } from './ComponentFrame'
import { componentsOf, useRunLive, type RunComponent } from './useRunLive'

/**
 * Canvas chrome (headers, labels) keeps a readable screen size when zoomed out
 * (up to 4×), and grows naturally when zoomed in.
 */
export const CHROME_ZOOM = 'min(4, max(1, var(--inv-k, 1)))'

export function RunGroup({
  runId,
  index,
  brief,
  selected,
  isSelected,
  labelOf,
  onComponents,
  onStatus,
  onRetry,
  onDetails,
}: {
  runId: string
  /** 1-based position of the message in the chat (component labels are `index.n`). */
  index: number
  brief: string
  /** The whole group is selected. */
  selected: boolean
  isSelected: (componentId: string) => boolean
  /** Canvas label of any component in the chat (for "about 2.1" chips). */
  labelOf: (componentId: string) => string | null
  onComponents: (runId: string, components: RunComponent[]) => void
  onStatus: (runId: string, status: RunStatus) => void
  onRetry: (run: Run) => void
  onDetails: (runId: string) => void
}) {
  const { run, error, activity, streamError, retryStream } = useRunLive(runId)
  const components = useMemo(() => (run ? componentsOf(run) : []), [run])
  const componentsKey = components.map((c) => `${c.id}:${c.draft ? 1 : 0}`).join(',')

  useEffect(() => {
    onComponents(runId, components)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [runId, componentsKey, onComponents])
  useEffect(() => {
    if (run?.status) onStatus(runId, run.status)
  }, [runId, run?.status, onStatus])

  const bodyQ = usePipelineBody(run ?? undefined)
  const nodeLabels = useMemo(() => new Map((bodyQ.data?.nodes ?? []).map((n) => [n.id, nodeLabel(n)])), [bodyQ.data])
  const nodeOrder = useMemo(() => new Map((bodyQ.data?.nodes ?? []).map((n, i) => [n.id, i])), [bodyQ.data])

  const running = run != null && !isTerminalRunStatus(run.status)
  const refs = (run?.context_artifact_ids ?? []).map((id) => labelOf(id) ?? '·')

  return (
    <section
      data-sel={`g:${runId}`}
      data-group={runId}
      className="inline-flex max-w-[5600px] flex-col gap-8 rounded-[40px] bg-ink/[0.035] p-12"
      style={{
        outline: selected ? 'calc(3px * var(--inv-k, 1)) solid var(--color-accent)' : undefined,
        outlineOffset: 'calc(4px * var(--inv-k, 1))',
      }}
    >
      {/* header: the message + status */}
      <header className="flex max-w-[720px] flex-col gap-2 text-[15px]" style={{ zoom: CHROME_ZOOM }}>
        <div className="flex items-start gap-3">
          <span className={cx('shrink-0 font-semibold', selected ? 'text-accent' : 'text-ink-3')}>{index}</span>
          <p className="min-w-0 flex-1 font-medium whitespace-pre-wrap text-ink" style={{ maxWidth: '60ch' }}>
            {brief}
          </p>
        </div>
        {refs.length > 0 && (
          <p className="pl-6 text-xs text-ink-3">
            about {refs.join(', ')}
          </p>
        )}
        {run && (running || run.error || streamError != null) && (
          <div className="flex w-[min(560px,100%)] flex-col gap-2 pl-6 text-sm">
            {running && <RunProgress run={run} nodeLabels={nodeLabels} nodeOrder={nodeOrder} activity={activity} />}
            {run.error && (
              <p className={cx('break-words', run.status === 'interrupted' ? 'text-warning' : 'text-danger')}>
                {run.status === 'cancelled' ? 'Stopped' : run.status === 'interrupted' ? 'Interrupted' : 'Failed'}: {run.error}
              </p>
            )}
            {streamError != null && <p className="text-danger">Live updates lost.</p>}
          </div>
        )}
        <div data-canvas-ui className="flex flex-wrap items-center gap-1.5 pl-6">
          {running && (
            <Button size="sm" variant="secondary" icon={<Ban />} onClick={() => void cancelRun(runId).catch(() => {})}>
              Stop
            </Button>
          )}
          {run && (run.status === 'failed' || run.status === 'cancelled') && (
            <Button size="sm" variant="secondary" icon={<RotateCw />} onClick={() => onRetry(run)}>
              Try again
            </Button>
          )}
          {streamError != null && (
            <Button size="sm" variant="secondary" icon={<RotateCw />} onClick={retryStream}>
              Reconnect
            </Button>
          )}
          <Button size="sm" variant="ghost" icon={<ListTree />} onClick={() => onDetails(runId)}>
            Details
          </Button>
        </div>
      </header>

      {/* components */}
      {error != null ? (
        <p className="text-sm text-danger" style={{ zoom: CHROME_ZOOM }}>Couldn't load this message's run.</p>
      ) : components.length > 0 ? (
        <div className="flex max-w-[5400px] flex-wrap items-start gap-16">
          {components.map((c, i) => (
            <ComponentFrame
              key={c.id}
              id={c.id}
              label={`${index}.${i + 1}`}
              score={c.score}
              draft={c.draft}
              selected={selected || isSelected(c.id)}
            />
          ))}
        </div>
      ) : (run == null || running) ? (
        <div className="animate-pulse rounded-lg bg-surface/70" style={{ width: FRAME_WIDTH, height: 360 }} />
      ) : null}
    </section>
  )
}
