/**
 * One message of the chat on the canvas = one run = one group: a transparent
 * rounded block holding every component the run produced, headed by the
 * message itself (+ the components it referred to), live progress, Stop /
 * Details and failure handling. The block is selectable as a whole (`g:<run>`)
 * — selecting it means "all its components".
 *
 * Layout: components flow in a wrapping row (or a single row / column, see
 * `dir`); moved components are translated by their offset and the block grows
 * around them: right / bottom by padding (pushes what follows in the stack),
 * left / top by extending its background (the layout never shifts). The block itself can be moved on the canvas
 * by its top-edge handle (then it's absolutely positioned — `free`).
 */
import { memo, useEffect, useLayoutEffect, useMemo, useRef } from 'react'
import { Ban, ListTree, RotateCw } from 'lucide-react'
import { Button, cx } from '../../../ui'
import { cancelRun, isTerminalRunStatus, type Run, type RunStatus } from '../../../api/runs'
import { usePipelineBody } from '../../runs/usePipelineBody'
import { nodeLabel } from '../../runs/shared'
import { RunProgress } from '../RunProgress'
import { ComponentFrame, FRAME_WIDTH, FrameHandles } from './ComponentFrame'
import { componentsOf, useRunLive, type RunComponent } from './useRunLive'
import type { GroupDir, Point } from './layout'

const COMPONENTS_LAYOUT: Record<GroupDir | 'wrap', string> = {
  wrap: 'max-w-[5400px] flex-wrap',
  row: 'flex-nowrap',
  col: 'flex-col',
}

/**
 * Canvas chrome (headers, labels) keeps a readable screen size when zoomed out
 * (up to 4×), and grows naturally when zoomed in.
 */
export const CHROME_ZOOM = 'min(4, max(1, var(--inv-k, 1)))'

export const RunGroup = memo(function RunGroup({
  runId,
  index,
  brief,
  selected,
  isSelected,
  labelOf,
  offset,
  free,
  dir,
  pos,
  scales,
  removed,
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
  /** Canvas offset of the moved group. */
  offset?: Point
  /** Absolutely positioned at `offset` (free canvas layout) instead of stacked. */
  free?: boolean
  dir?: GroupDir
  /** Layout maps of the chat (`c:<id>` offsets, scales by id, deleted `c:<id>` keys). */
  pos: Record<string, Point>
  scales: Record<string, number>
  removed: Record<string, true>
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

  // index in the run's full list: labels stay stable when a component is deleted
  const visible = components.map((c, i) => ({ c, i })).filter(({ c }) => !removed[`c:${c.id}`])

  // Grow the block around moved components (translate doesn't affect layout).
  const boxRef = useRef<HTMLDivElement>(null)
  const bgRef = useRef<HTMLDivElement>(null)
  const posKey = visible.map(({ c }) => { const p = pos[`c:${c.id}`]; return p ? `${p.x},${p.y}` : '' }).join(';')
  useLayoutEffect(() => {
    const box = boxRef.current
    const bg = bgRef.current
    if (!box || !bg) return
    let raf = 0
    const fit = (): void => {
      raf = 0
      let left = 0
      let top = 0
      let right = 0
      let bottom = 0
      for (const el of Array.from(box.children) as HTMLElement[]) {
        const x = el.offsetLeft + Number(el.dataset.dx || 0)
        // the label sits above the frame (out of flow, in its top margin)
        const y = el.offsetTop + Number(el.dataset.dy || 0) - parseFloat(getComputedStyle(el).marginTop || '0')
        left = Math.min(left, x)
        top = Math.min(top, y)
        right = Math.max(right, x + el.offsetWidth)
        bottom = Math.max(bottom, el.offsetTop + Number(el.dataset.dy || 0) + el.offsetHeight)
      }
      const pr = parseFloat(box.style.paddingRight || '0')
      const pb = parseFloat(box.style.paddingBottom || '0')
      const nr = Math.max(0, Math.ceil(right - (box.clientWidth - pr)))
      const nb = Math.max(0, Math.ceil(bottom - (box.clientHeight - pb)))
      if (nr !== pr) box.style.paddingRight = nr ? `${nr}px` : ''
      if (nb !== pb) box.style.paddingBottom = nb ? `${nb}px` : ''
      // bg is positioned in the section: the container starts after its padding (and the header)
      const pad = parseFloat(getComputedStyle(box.parentElement as HTMLElement).paddingTop || '0')
      bg.style.left = `${Math.min(0, Math.floor(box.offsetLeft + left - pad))}px`
      bg.style.top = `${Math.min(0, Math.floor(box.offsetTop + top - pad))}px`
    }
    const schedule = (): void => {
      if (!raf) raf = requestAnimationFrame(fit)
    }
    const ro = new ResizeObserver(schedule)
    for (const el of Array.from(box.children)) ro.observe(el)
    schedule()
    return () => {
      ro.disconnect()
      cancelAnimationFrame(raf)
    }
  }, [posKey, componentsKey, dir])

  return (
    <section
      data-sel={`g:${runId}`}
      data-group={runId}
      data-dx={offset?.x || undefined}
      data-dy={offset?.y || undefined}
      className={cx('group/run isolate inline-flex', free ? 'absolute top-0 left-0 w-max' : 'relative', 'flex-col gap-8 p-12 data-dragging:z-30', selected && 'z-10')}
      style={{ translate: offset ? `${offset.x}px ${offset.y}px` : undefined }}
    >
      {/* the block's background: its box, extended left / up around moved components */}
      <div
        ref={bgRef}
        aria-hidden
        className="absolute inset-0 -z-10 rounded-[40px] bg-ink/[0.035]"
        style={{
          outline: selected ? 'calc(3px * var(--inv-k, 1)) solid var(--color-accent)' : undefined,
          outlineOffset: 'calc(4px * var(--inv-k, 1))',
        }}
      />
      <FrameHandles visible={selected} scope="run" resize={false} />
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
      ) : visible.length > 0 ? (
        <div ref={boxRef} className={cx('relative flex items-start gap-x-16 gap-y-20', COMPONENTS_LAYOUT[dir ?? 'wrap'])}>
          {visible.map(({ c, i }) => (
            <ComponentFrame
              key={c.id}
              id={c.id}
              label={`${index}.${i + 1}`}
              score={c.score}
              draft={c.draft}
              selected={selected || isSelected(c.id)}
              handles={isSelected(c.id)}
              offset={pos[`c:${c.id}`]}
              scale={scales[c.id]}
            />
          ))}
        </div>
      ) : (run == null || running) ? (
        <div className="animate-pulse rounded-lg bg-surface/70" style={{ width: FRAME_WIDTH, height: 360 }} />
      ) : null}
    </section>
  )
})
