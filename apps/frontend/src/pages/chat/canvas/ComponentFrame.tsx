/**
 * One generated component on the canvas: its HTML in a sandboxed iframe
 * (`allow-scripts`, opaque origin — it can't reach the app or its tokens).
 *
 * The iframe never receives pointer events (the canvas owns every gesture);
 * an injected script reports the content size via postMessage so the frame
 * hugs its component: at the 1280px render width it measures the union of the
 * visible boxes that don't span the whole width — when the component is much
 * narrower the frame shrinks to it (once), and the height follows the
 * document (ResizeObserver + a few delayed re-measures).
 */
import { useEffect, useMemo, useRef, useState } from 'react'
import { fetchArtifact } from '../../../api/runs'
import { cx } from '../../../ui'

/** Render width — the pipeline's Render node default, what the critics judged. */
export const FRAME_WIDTH = 1280
const MIN_WIDTH = 320
const MIN_HEIGHT = 64
const MAX_HEIGHT = 6000

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
// images, frames laid out late while off-screen).
const MEASURE_SCRIPT = `<script>(function(){
function size(){
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
  if(vw>0&&h>0)parent.postMessage({__wcFrame:1,w:w,h:h},'*');
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

export function ComponentFrame({
  id,
  label,
  score,
  draft,
  selected,
}: {
  id: string
  /** Short canvas label, e.g. `2.1` (group 2, component 1). */
  label: string
  score: number | null
  draft: boolean
  selected: boolean
}) {
  const { html, error } = useComponentHtml(id)
  const ref = useRef<HTMLIFrameElement>(null)
  const [size, setSize] = useState<{ w: number; h: number }>({ w: FRAME_WIDTH, h: 360 })
  const doc = useMemo(() => (html != null ? withMeasure(html) : null), [html])

  useEffect(() => {
    const onMessage = (e: MessageEvent): void => {
      if (e.source !== ref.current?.contentWindow) return
      const d = e.data as { __wcFrame?: number; w?: number; h?: number } | null
      if (!d || d.__wcFrame !== 1) return
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
    <div data-sel={`c:${id}`} className="flex shrink-0 flex-col gap-2">
      {/* label row — stays readable when zoomed out (see CHROME_ZOOM) */}
      <div className="flex items-center gap-1.5 text-[13px] text-ink-2" style={{ zoom: 'min(4, max(1, var(--inv-k, 1)))' }}>
        <span className={cx('font-semibold', selected ? 'text-accent' : 'text-ink-2')}>{label}</span>
        {score != null && <span className="text-ink-3">· {score.toFixed(1)}</span>}
        {draft && <span className="text-ink-3">· draft</span>}
      </div>
      <div
        className={cx('relative overflow-hidden rounded-lg bg-surface shadow-[0_1px_3px_rgba(0,0,0,.08),0_8px_24px_rgba(0,0,0,.06)]', draft && 'opacity-80')}
        style={{
          width: size.w,
          height: size.h,
          outline: selected ? 'calc(3px * var(--inv-k, 1)) solid var(--color-accent)' : undefined,
          outlineOffset: 'calc(2px * var(--inv-k, 1))',
        }}
      >
        {doc != null ? (
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
  )
}
