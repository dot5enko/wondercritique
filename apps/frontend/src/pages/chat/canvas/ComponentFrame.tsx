/**
 * One generated component on the canvas: its HTML in a sandboxed iframe
 * (`allow-scripts`, opaque origin — it can't reach the app or its tokens).
 *
 * The iframe never receives pointer events (the canvas owns every gesture);
 * an injected script reports the content size via postMessage so the frame
 * hugs its component: at the 1280px render width it measures the union of the
 * visible boxes that don't span the whole width — when the component is much
 * narrower the frame shrinks to it (once), and the height follows the
 * document (ResizeObserver + a few delayed re-measures). The resize handle
 * scales the whole component uniformly (`scale`): the document keeps its
 * measured size and is drawn bigger / smaller, so there is never empty space.
 *
 * Performance: an iframe is only created once its frame comes near the screen
 * (IntersectionObserver — it sees the canvas transform), and at most
 * `BOOT_CONCURRENCY` documents parse/lay out at the same time, so a chat full
 * of heavy hero sections doesn't block the main thread all at once.
 *
 * Handles (shown on hover and while selected): a pill on the top edge moves the
 * component inside its group, the bottom-right grip resizes it — both are
 * `data-handle` targets the canvas viewport turns into drags.
 */
import { memo, useEffect, useMemo, useRef, useState } from 'react'
import { fetchArtifact } from '../../../api/runs'
import { cx } from '../../../ui'
import type { Point } from './layout'

/** Same as `CHROME_ZOOM` in RunGroup (kept here to avoid an import cycle). */
const CHROME_ZOOM = 'min(4, max(1, var(--inv-k, 1)))'

/** Render width — the pipeline's Render node default, what the critics judged. */
export const FRAME_WIDTH = 1280
const MIN_WIDTH = 320
const MIN_HEIGHT = 64
const MAX_HEIGHT = 6000

/* ---------- boot queue: few iframes starting at once ---------- */

const BOOT_CONCURRENCY = 2
/** A boot slot is released after the first size report, or after this anyway. */
const BOOT_TIMEOUT_MS = 1500
let booting = 0
const bootQueue: (() => void)[] = []

function pumpBoot(): void {
  while (booting < BOOT_CONCURRENCY && bootQueue.length > 0) bootQueue.shift()?.()
}

/** Calls `start` when a slot is free; the returned function releases (or dequeues). Idempotent. */
function acquireBoot(start: () => void): () => void {
  let state: 'wait' | 'run' | 'done' = 'wait'
  const run = (): void => {
    state = 'run'
    booting++
    start()
  }
  bootQueue.push(run)
  pumpBoot()
  return () => {
    if (state === 'wait') {
      const i = bootQueue.indexOf(run)
      if (i >= 0) bootQueue.splice(i, 1)
    } else if (state === 'run') {
      booting--
      pumpBoot()
    }
    state = 'done'
  }
}

/* ---------- html cache (artifacts are immutable) ---------- */

const htmlCache = new Map<string, Promise<string>>()

/** The HTML of an artifact, fetched once per session. */
export function loadComponentHtml(id: string): Promise<string> {
  let p = htmlCache.get(id)
  if (!p) {
    p = fetchArtifact(id).then((c) => {
      URL.revokeObjectURL(c.url)
      return c.text ?? ''
    })
    p.catch(() => htmlCache.delete(id))
    htmlCache.set(id, p)
  }
  return p
}

export function useComponentHtml(id: string): { html: string | null; error: unknown } {
  const [state, setState] = useState<{ id: string; html: string | null; error: unknown }>({ id, html: null, error: null })
  useEffect(() => {
    if (!id) return
    let alive = true
    loadComponentHtml(id).then(
      (html) => alive && setState({ id, html, error: null }),
      (error: unknown) => alive && setState({ id, html: null, error }),
    )
    return () => {
      alive = false
    }
  }, [id])
  return state.id === id ? state : { html: null, error: null }
}

/* ---------- measuring script ---------- */

// Reports {w, h}: w = natural content width, only measured while the frame is
// at the full render width (w = 0 means "keep the width"); the parent applies
// it once. Measures on load, on body resizes and a few times after (fonts,
// images, frames laid out late while off-screen) — at most once per frame, and
// only posts when the size changed.
const MEASURE_SCRIPT = `<script>(function(){
var pending=0,lw=-1,lh=-1;
function size(){if(!pending)pending=requestAnimationFrame(measure)}
function measure(){
  pending=0;
  var d=document.documentElement,b=document.body;if(!b)return;
  var vw=d.clientWidth,w=0,full=vw>=${FRAME_WIDTH - 8};
  var l=1e9,r=-1e9,bottom=0,els=b.querySelectorAll('*');
  for(var i=0;i<els.length&&i<4000;i++){
    var rc=els[i].getBoundingClientRect();
    if(rc.width<=0||rc.height<=0)continue;
    if(rc.bottom>bottom)bottom=rc.bottom;
    if(!full||rc.width>=vw*0.97)continue;
    if(rc.left<l)l=rc.left;if(rc.right>r)r=rc.right;
  }
  if(full&&r>l&&(r-l)<vw*0.75)w=Math.ceil(r-l+2*Math.min(Math.max(l,0),48));
  // content height, not scrollHeight (which never drops below the frame height)
  var bs=getComputedStyle(b),br=b.getBoundingClientRect();
  var h=Math.ceil(Math.max(bottom,br.bottom+parseFloat(bs.marginBottom||'0'))+scrollY);
  if(vw>0&&h>0&&(w!==lw||h!==lh)){lw=w;lh=h;parent.postMessage({__wcFrame:1,w:w,h:h},'*');}
}
if(window.ResizeObserver){var ro=new ResizeObserver(size);ro.observe(document.documentElement);if(document.body)ro.observe(document.body);}
addEventListener('load',size);
[0,300,1000,2500].forEach(function(t){setTimeout(size,t)});
})();</script>`

/** Appends the measuring script (and a transparent-safe base) to a component document. */
function withMeasure(html: string): string {
  const base = '<style>html,body{margin:0}</style>'
  if (/<\/body\s*>/i.test(html)) return html.replace(/<\/body\s*>(?![\s\S]*<\/body\s*>)/i, `${MEASURE_SCRIPT}</body>`)
  if (/<html[\s>]/i.test(html)) return html + MEASURE_SCRIPT
  return `<!doctype html><html><head><meta charset="utf-8">${base}</head><body>${html}${MEASURE_SCRIPT}</body></html>`
}

/* ---------- frame ---------- */

export const ComponentFrame = memo(function ComponentFrame({
  id,
  label,
  score,
  draft,
  selected,
  handles,
  offset,
  scale = 1,
}: {
  id: string
  /** Short canvas label, e.g. `2.1` (group 2, component 1). */
  label: string
  score: number | null
  draft: boolean
  selected: boolean
  /** Show the move / resize handles (the component itself is selected, not just its group). */
  handles: boolean
  /** Position inside the group (moved with the handle). */
  offset?: Point
  /** Uniform scale set with the resize handle. */
  scale?: number
}) {
  const { html, error } = useComponentHtml(id)
  const rootRef = useRef<HTMLDivElement>(null)
  const ref = useRef<HTMLIFrameElement>(null)
  const [measured, setSize] = useState<{ w: number; h: number }>({ w: FRAME_WIDTH, h: 360 })
  const doc = useMemo(() => (html != null ? withMeasure(html) : null), [html])

  // Mount the iframe once the frame is near the screen, then through the boot queue.
  const [near, setNear] = useState(false)
  const [mounted, setMounted] = useState(false)
  const releaseRef = useRef<(() => void) | null>(null)
  useEffect(() => {
    const el = rootRef.current
    if (!el || near) return
    if (typeof IntersectionObserver === 'undefined') {
      setNear(true)
      return
    }
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) setNear(true)
    }, { rootMargin: '50%' })
    io.observe(el)
    return () => io.disconnect()
  }, [near])
  useEffect(() => {
    if (!near || doc == null) return
    let timer = 0
    const release = acquireBoot(() => {
      setMounted(true)
      timer = window.setTimeout(() => release(), BOOT_TIMEOUT_MS)
    })
    releaseRef.current = release
    return () => {
      window.clearTimeout(timer)
      release()
    }
  }, [near, doc])

  useEffect(() => {
    const onMessage = (e: MessageEvent): void => {
      if (e.source !== ref.current?.contentWindow) return
      const d = e.data as { __wcFrame?: number; w?: number; h?: number } | null
      if (!d || d.__wcFrame !== 1) return
      releaseRef.current?.()
      setSize((prev) => {
        // shrink once, from the full render width; later reports only move the height
        const w = prev.w === FRAME_WIDTH && typeof d.w === 'number' && d.w > 0 ? Math.max(MIN_WIDTH, Math.min(FRAME_WIDTH, d.w)) : prev.w
        const h = typeof d.h === 'number' && d.h > 0 ? Math.max(MIN_HEIGHT, Math.min(MAX_HEIGHT, d.h)) : prev.h
        return w === prev.w && h === prev.h ? prev : { w, h }
      })
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [])

  return (
    <div
      ref={rootRef}
      data-sel={`c:${id}`}
      data-dx={offset?.x || undefined}
      data-dy={offset?.y || undefined}
      className={cx('group/frame relative mt-7 shrink-0 data-dragging:z-30', selected && 'z-10')}
      style={offset ? { translate: `${offset.x}px ${offset.y}px` } : undefined}
    >
      {/* label — stays readable when zoomed out (see CHROME_ZOOM); out of flow,
          so zooming never changes the layout (it grows upward into the gap) */}
      <div className="absolute bottom-full left-0 mb-1.5 flex items-center gap-1.5 text-[13px] whitespace-nowrap text-ink-2" style={{ zoom: CHROME_ZOOM }}>
        <span className={cx('font-semibold', selected ? 'text-accent' : 'text-ink-2')}>{label}</span>
        {score != null && <span className="text-ink-3">· {score.toFixed(1)}</span>}
        {draft && <span className="text-ink-3">· draft</span>}
      </div>
      <div
        data-frame-box
        className={cx('relative rounded-lg bg-surface shadow-[0_1px_3px_rgba(0,0,0,.08),0_8px_24px_rgba(0,0,0,.06)]', draft && 'opacity-80')}
        style={{
          width: measured.w * scale,
          height: measured.h * scale,
          outline: selected ? 'calc(3px * var(--inv-k, 1)) solid var(--color-accent)' : undefined,
          outlineOffset: 'calc(2px * var(--inv-k, 1))',
        }}
      >
        <div className="h-full w-full overflow-hidden rounded-lg [contain:strict]">
          {/* the document at its natural size, drawn at `scale` */}
          <div
            data-frame-doc
            data-w={measured.w}
            data-h={measured.h}
            className="origin-top-left"
            style={{ width: measured.w, height: measured.h, transform: scale !== 1 ? `scale(${scale})` : undefined }}
          >
            {doc != null && mounted ? (
              <iframe
                ref={ref}
                title={`Component ${label}`}
                srcDoc={doc}
                sandbox="allow-scripts"
                className="pointer-events-none block h-full w-full border-0 bg-white"
                tabIndex={-1}
              />
            ) : error != null ? (
              <div className="grid h-full place-items-center text-sm text-danger">Couldn't load this component</div>
            ) : (
              <div className="h-full w-full animate-pulse bg-app" />
            )}
          </div>
        </div>
        <FrameHandles visible={handles} scope="frame" />
      </div>
    </div>
  )
})

/**
 * Android-style window handles: a pill straddling the top edge (move) and a
 * grip in the bottom-right corner (resize). Screen-sized at any zoom.
 */
const HANDLE_HOVER = {
  frame: 'pointer-events-none opacity-0 pointer-fine:group-hover/frame:pointer-events-auto pointer-fine:group-hover/frame:opacity-100',
  run: 'pointer-events-none opacity-0 pointer-fine:group-hover/run:pointer-events-auto pointer-fine:group-hover/run:opacity-100',
}

export function FrameHandles({ visible, scope, resize = true }: {
  visible: boolean
  /** Tailwind group the hover reveal follows (`group/frame`, `group/run`). */
  scope: keyof typeof HANDLE_HOVER
  resize?: boolean
}) {
  const show = visible ? 'pointer-events-auto opacity-100' : HANDLE_HOVER[scope]
  return (
    <>
      <div className="pointer-events-none absolute inset-x-0 top-0 flex justify-center" style={{ zoom: CHROME_ZOOM }}>
        <div
          data-handle="move"
          title="Drag to move"
          className={cx('-mt-4 grid h-8 w-28 cursor-grab touch-none place-items-center transition-opacity duration-100 active:cursor-grabbing', show)}
        >
          <span className="h-1.5 w-14 rounded-full bg-ink/45 shadow-[0_0_0_2px_rgba(255,255,255,.85)]" />
        </div>
      </div>
      {resize && (
        <div className="pointer-events-none absolute right-0 bottom-0" style={{ zoom: CHROME_ZOOM }}>
          <div
            data-handle="resize"
            title="Drag to resize"
            className={cx('-mr-3 -mb-3 grid size-9 cursor-nwse-resize touch-none place-items-center transition-opacity duration-100', show)}
          >
            <svg width="18" height="18" viewBox="0 0 18 18" aria-hidden className="drop-shadow-[0_0_1.5px_rgba(255,255,255,.9)]">
              <path d="M16 4v6a6 6 0 0 1-6 6H4" fill="none" stroke="currentColor" strokeWidth="3" strokeLinecap="round" className="text-ink/55" />
            </svg>
          </div>
        </div>
      )}
    </>
  )
}
