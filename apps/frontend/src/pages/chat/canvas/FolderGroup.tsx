/**
 * One folder of the chat's canvas = one group: a transparent rounded block
 * holding the folder's component files, then its subfolders as nested blocks
 * (recursively), plus a pending card for every message still working on a
 * result that goes into this folder. The block is selectable as a whole
 * (`g:<folder>`) — selecting it means "every file below it".
 *
 * Layout: the folder's items flow in a wrapping row (or a single row / column,
 * or packed into several columns — see `dir`); moved items are translated by their offset and the block grows
 * around them: right / bottom by padding (pushes what follows in the stack),
 * left / top by extending its background (the layout never shifts). A
 * top-level block can be moved on the canvas by its top-edge handle (then it's
 * absolutely positioned — `free`); nested blocks move inside their parent.
 */
import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { Ban, RotateCw } from 'lucide-react'
import { Button, cx } from '../../../ui'
import { Markdown } from '../../../components'
import { type CanvasFile } from '../../../api/canvas'
import { cancelRun, isTerminalRunStatus, type Run, type RunStatus } from '../../../api/runs'
import { usePipelineBody } from '../../runs/usePipelineBody'
import { nodeLabel } from '../../runs/shared'
import { RunProgress } from '../RunProgress'
import { ComponentFrame, FRAME_WIDTH, FrameHandles } from './ComponentFrame'
import { componentsOf, useRunLive } from './useRunLive'
import { componentLabel, folderLabel, type FolderNode } from './tree'
import { PACK_ASPECT, PACK_GAP, packRects, type CanvasLayout, type GroupDir } from './layout'

const ITEMS_LAYOUT: Record<GroupDir | 'wrap', string> = {
  wrap: 'max-w-[5400px] flex-wrap',
  row: 'flex-nowrap',
  col: 'flex-col',
  // items placed absolutely by the layout effect (packRects)
  pack: '',
}

/** Places a packed folder's items; returns the packed block's size. */
function packItems(kids: HTMLElement[]): { w: number; h: number } {
  for (const el of kids) el.style.width = 'max-content'
  const mt = kids.map((el) => parseFloat(getComputedStyle(el).marginTop || '0'))
  const at = packRects(kids.map((el, i) => ({ w: el.offsetWidth, h: el.offsetHeight + mt[i] })), PACK_GAP, PACK_ASPECT)
  let w = 0
  let h = 0
  kids.forEach((el, i) => {
    el.style.position = 'absolute'
    el.style.left = `${at[i].x}px`
    el.style.top = `${at[i].y}px`
    w = Math.max(w, at[i].x + el.offsetWidth)
    h = Math.max(h, at[i].y + mt[i] + el.offsetHeight)
  })
  return { w, h }
}

/** Back to the flex flow after `pack`. */
function unpackItems(box: HTMLElement): void {
  if (!box.dataset.packed) return
  delete box.dataset.packed
  box.style.width = ''
  box.style.height = ''
  for (const el of Array.from(box.children) as HTMLElement[]) {
    el.style.position = ''
    el.style.left = ''
    el.style.top = ''
    el.style.width = ''
  }
}

/**
 * Canvas chrome (headers, labels) keeps a readable screen size when zoomed out
 * (up to 4×), and grows naturally when zoomed in.
 */
export const CHROME_ZOOM = 'min(4, max(1, var(--inv-k, 1)))'

/** What every group of the canvas needs from the page (stable references). */
export interface GroupEnv {
  selection: Set<string>
  layout: CanvasLayout
  /** message number + brief (+ the chat agent's reply) of a run of the chat. */
  messageOf: (runId: string) => { index: number; brief: string; reply: string } | null
  onRetry: (run: Run) => void
  onStatus: (runId: string, status: RunStatus) => void
}

export const FolderGroup = memo(function FolderGroup({
  node,
  top,
  free,
  env,
}: {
  node: FolderNode
  /** A canvas group (top-level folder or the root's loose files) vs a nested block. */
  top: boolean
  /** Top-level groups are absolutely positioned (free canvas layout). */
  free?: boolean
  env: GroupEnv
}) {
  const { selection, layout } = env
  const key = `g:${node.path}`
  const selected = selection.has(key)
  const offset = layout.pos[key]
  const dir = layout.dir[node.path]

  // Grow the block around moved items (translate doesn't affect layout).
  const boxRef = useRef<HTMLDivElement>(null)
  const bgRef = useRef<HTMLDivElement>(null)
  const itemKeys = [...node.files.map((f) => `c:${f.path}`), ...node.folders.map((f) => `g:${f.path}`)]
  const posKey = itemKeys.map((k) => { const p = layout.pos[k]; return p ? `${k}=${p.x},${p.y}` : k }).join(';') + `|${node.pending.join(',')}`
  useLayoutEffect(() => {
    const box = boxRef.current
    const bg = bgRef.current
    if (!box || !bg) return
    let raf = 0
    const fit = (): void => {
      raf = 0
      const kids = Array.from(box.children) as HTMLElement[]
      // pack: the items are absolutely placed, the box gets the packed size
      let packed: { w: number; h: number } | null = null
      if (dir === 'pack') {
        box.dataset.packed = ''
        packed = packItems(kids)
      } else {
        unpackItems(box)
      }
      let left = 0
      let top = 0
      let right = 0
      let bottom = 0
      for (const el of kids) {
        const x = el.offsetLeft + Number(el.dataset.dx || 0)
        // a component's label sits above its frame (out of flow, in its top margin)
        const y = el.offsetTop + Number(el.dataset.dy || 0) - parseFloat(getComputedStyle(el).marginTop || '0')
        left = Math.min(left, x)
        top = Math.min(top, y)
        right = Math.max(right, x + el.offsetWidth)
        bottom = Math.max(bottom, el.offsetTop + Number(el.dataset.dy || 0) + el.offsetHeight)
      }
      const pr = parseFloat(box.style.paddingRight || '0')
      const pb = parseFloat(box.style.paddingBottom || '0')
      const nr = Math.max(0, Math.ceil(right - (packed ? packed.w : box.clientWidth - pr)))
      const nb = Math.max(0, Math.ceil(bottom - (packed ? packed.h : box.clientHeight - pb)))
      if (nr !== pr) box.style.paddingRight = nr ? `${nr}px` : ''
      if (nb !== pb) box.style.paddingBottom = nb ? `${nb}px` : ''
      if (packed) {
        // border-box: the size includes the padding around moved items
        box.style.width = `${Math.ceil(packed.w) + nr}px`
        box.style.height = `${Math.ceil(packed.h) + nb}px`
      }
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
    // now, before paint: switching to / from pack must not flash the flex flow
    fit()
    return () => {
      ro.disconnect()
      cancelAnimationFrame(raf)
    }
  }, [posKey, dir])

  const empty = node.files.length === 0 && node.folders.length === 0 && node.pending.length === 0

  return (
    <section
      data-sel={key}
      data-group={top ? node.path : undefined}
      data-subgroup={top ? undefined : node.path}
      data-dx={offset?.x || undefined}
      data-dy={offset?.y || undefined}
      className={cx(
        'group/run isolate inline-flex flex-col gap-8 data-dragging:z-30',
        top ? 'p-12' : 'p-10',
        top && free ? 'absolute top-0 left-0 w-max' : 'relative',
        selected && 'z-10',
      )}
      style={{ translate: offset ? `${offset.x}px ${offset.y}px` : undefined }}
    >
      {/* the block's background: its box, extended left / up around moved items */}
      <div
        ref={bgRef}
        aria-hidden
        className={cx('absolute inset-0 -z-10', top ? 'rounded-[40px] bg-ink/[0.035]' : 'rounded-[32px] bg-ink/[0.04]', node.virtual && 'border-2 border-dashed border-line')}
        style={{
          outline: selected ? 'calc(3px * var(--inv-k, 1)) solid var(--color-accent)' : undefined,
          outlineOffset: 'calc(4px * var(--inv-k, 1))',
        }}
      />
      <FrameHandles visible={selected} scope="run" resize={false} />
      <header className="flex max-w-[720px] flex-col gap-1.5 text-[15px]" style={{ zoom: CHROME_ZOOM }}>
        <div className="flex items-baseline gap-3">
          <p className={cx('min-w-0 truncate font-semibold', selected ? 'text-accent' : 'text-ink')} title={node.path}>
            {folderLabel(node.path)}
          </p>
          {node.virtual && <span className="shrink-0 text-xs text-ink-3">new folder</span>}
        </div>
      </header>

      {empty ? null : (
        <div ref={boxRef} className={cx('relative flex items-start gap-x-16 gap-y-20', ITEMS_LAYOUT[dir ?? 'wrap'])}>
          {node.files.map((f) => (
            <FileFrame key={f.path} file={f} env={env} groupSelected={selected} />
          ))}
          {node.pending.map((id) => (
            <PendingCard key={id} runId={id} env={env} />
          ))}
          {node.folders.map((sub) => (
            <FolderGroup key={sub.path} node={sub} top={false} env={env} />
          ))}
        </div>
      )}
    </section>
  )
})

function FileFrame({ file, env, groupSelected }: { file: CanvasFile; env: GroupEnv; groupSelected: boolean }) {
  const key = `c:${file.path}`
  const own = env.selection.has(key)
  return (
    <ComponentFrame
      id={file.artifact_id}
      selKey={key}
      label={componentLabel(file.path)}
      score={file.score}
      draft={false}
      selected={groupSelected || own}
      handles={own}
      offset={env.layout.pos[key]}
      scale={env.layout.scale[file.path]}
    />
  )
}

/** The chat agent's answer to a message (markdown, clamped; click to expand). */
function Reply({ text }: { text: string }) {
  const [open, setOpen] = useState(false)
  return (
    <div
      data-canvas-ui
      className={cx('max-w-[60ch] cursor-pointer rounded-2xl bg-surface/80 px-3.5 py-2.5 text-sm text-ink-2', !open && 'line-clamp-4')}
      title={open ? undefined : 'Show the whole reply'}
      onClick={() => setOpen((v) => !v)}
    >
      <Markdown text={text} />
    </div>
  )
}

/**
 * A message still working on its result (or one that failed / stopped, or one
 * the agent only answered in text): the brief, live progress, the reply, the
 * latest drafts and Stop / Try again / Details. Completed messages with files
 * show nothing extra — their files are in the tree.
 */
const PendingCard = memo(function PendingCard({ runId, env }: { runId: string; env: GroupEnv }) {
  const { run, error, activity, streamError, retryStream } = useRunLive(runId)
  const drafts = useMemo(() => (run ? componentsOf(run) : []), [run])
  const { onStatus } = env
  useEffect(() => {
    if (run?.status) onStatus(runId, run.status)
  }, [runId, run?.status, onStatus])

  const bodyQ = usePipelineBody(run ?? undefined)
  const nodeLabels = useMemo(() => new Map((bodyQ.data?.nodes ?? []).map((n) => [n.id, nodeLabel(n)])), [bodyQ.data])
  const nodeOrder = useMemo(() => new Map((bodyQ.data?.nodes ?? []).map((n, i) => [n.id, i])), [bodyQ.data])

  const running = run == null || !isTerminalRunStatus(run.status)
  const msg = env.messageOf(runId)
  const refs = (run?.context_paths ?? []).map(componentLabel)
  const reply = run?.reply || msg?.reply || ''

  return (
    <div data-pending={runId} className="relative flex shrink-0 flex-col gap-6 rounded-[28px] border-2 border-dashed border-line-strong p-8">
      <header className="flex max-w-[720px] flex-col gap-2 text-[15px]" style={{ zoom: CHROME_ZOOM }}>
        <div className="flex items-start gap-3">
          <span className="shrink-0 font-semibold text-ink-3">{msg?.index ?? ''}</span>
          <p className="min-w-0 flex-1 font-medium whitespace-pre-wrap text-ink" style={{ maxWidth: '60ch' }}>
            {msg?.brief ?? ''}
          </p>
        </div>
        {refs.length > 0 && <p className="pl-6 text-xs text-ink-3">about {refs.join(', ')}</p>}
        {reply && (
          <div className="pl-6">
            <Reply text={reply} />
          </div>
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
        {error != null && <p className="pl-6 text-sm text-danger">Couldn't load this message's run.</p>}
        <div data-canvas-ui className="flex flex-wrap items-center gap-1.5 pl-6">
          {run && !isTerminalRunStatus(run.status) && (
            <Button size="sm" variant="secondary" icon={<Ban />} onClick={() => void cancelRun(runId).catch(() => {})}>
              Stop
            </Button>
          )}
          {run && (run.status === 'failed' || run.status === 'cancelled' || run.status === 'interrupted') && (
            <Button size="sm" variant="secondary" icon={<RotateCw />} onClick={() => env.onRetry(run)}>
              Try again
            </Button>
          )}
          {streamError != null && (
            <Button size="sm" variant="secondary" icon={<RotateCw />} onClick={retryStream}>
              Reconnect
            </Button>
          )}
        </div>
      </header>
      {drafts.length > 0 ? (
        <div className="flex flex-wrap items-start gap-x-16 gap-y-20">
          {drafts.map((c, i) => (
            <ComponentFrame key={c.id} id={c.id} label={`draft ${i + 1}`} score={c.score} draft selected={false} handles={false} />
          ))}
        </div>
      ) : running ? (
        <div className="animate-pulse rounded-lg bg-surface/70" style={{ width: FRAME_WIDTH, height: 360 }} />
      ) : null}
    </div>
  )
})
