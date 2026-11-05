/**
 * User layout of one chat's canvas, on top of the automatic flex layout. Keys
 * are canvas paths (the server remaps them when files / folders move):
 *  - `pos`  — CSS `translate` of an element. `c:<file>`: offset of a moved
 *    component from its place in its folder's flow (any direction — the group
 *    block grows around it). `g:<folder>`: a top-level group's absolute canvas
 *    position — once any group is moved or arranged, every top-level group is
 *    positioned ("free" layout, see the canvas page), so groups never shift
 *    when another one changes height; a nested group's offset in its parent;
 *  - `scale` — uniform scale of a resized component (file path);
 *  - `dir`  — arrangement of a folder's items (row / column / pack; default
 *    wrap). `pack` = "fill the free space": items absolutely placed by
 *    `packRects` in several columns, re-packed live as they resize.
 * Stored on the server per thread (`PUT /threads/{id}/canvas/layout`,
 * debounced, flushed on unmount / `pagehide`); the agents never see it.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { putCanvasLayout, type CanvasLayout as ServerLayout } from '../../../api/canvas'

export interface Point {
  x: number
  y: number
}
/** A folder's flow: one row, one column, or packed into several columns (`packRects`). */
export type GroupDir = 'row' | 'col' | 'pack'

export interface CanvasLayout {
  pos: Record<string, Point>
  scale: Record<string, number>
  dir: Record<string, GroupDir>
}

export const MIN_SCALE = 0.25
export const MAX_SCALE = 4
/** Layout changes are saved this long after the last one. */
export const SAVE_DEBOUNCE_MS = 500

const EMPTY: CanvasLayout = { pos: {}, scale: {}, dir: {} }

/** Gap between packed items (components and nested groups). */
export const PACK_GAP = 64
/** Shape a packed block aims for: the screen's, within reason. */
export const packAspect = (): number => Math.min(2, Math.max(0.75, window.innerWidth / Math.max(1, window.innerHeight)))

/**
 * "Arrange to fill space": packs boxes into a block about `aspect` (w / h)
 * wide using a skyline — tallest first, each box goes to the spot where its
 * top is highest (leftmost on ties), so short boxes fill the holes next to
 * tall ones and several columns are used. Returns top-left corners relative
 * to the block's origin, in the input order.
 */
export function packRects(boxes: { w: number; h: number }[], gap: number, aspect: number): Point[] {
  if (boxes.length === 0) return []
  const area = boxes.reduce((s, b) => s + (b.w + gap) * (b.h + gap), 0)
  const widest = Math.max(...boxes.map((b) => b.w + gap))
  const width = Math.max(widest, Math.sqrt(area * aspect))
  /** The skyline: contiguous segments [x, x + w) whose free space starts at y. */
  let sky: { x: number; w: number; y: number }[] = [{ x: 0, w: width, y: 0 }]
  const order = boxes.map((_, i) => i).sort((a, b) => boxes[b].h - boxes[a].h || boxes[b].w - boxes[a].w)
  const out: Point[] = new Array<Point>(boxes.length)
  for (const i of order) {
    const w = boxes[i].w + gap
    let best: { x: number; y: number } | null = null
    for (const s of sky) {
      // the box's last gap may stick out of the block
      if (s.x + w - gap > width + 0.5) break
      const y = Math.max(...sky.filter((t) => t.x < s.x + w && t.x + t.w > s.x).map((t) => t.y))
      if (!best || y < best.y) best = { x: s.x, y }
    }
    const at = best ?? { x: 0, y: Math.max(...sky.map((t) => t.y)) }
    out[i] = at
    // raise the skyline under the box
    const top = at.y + boxes[i].h + gap
    const end = at.x + w
    const next: typeof sky = []
    for (const t of sky) {
      const tEnd = t.x + t.w
      if (tEnd <= at.x || t.x >= end) {
        next.push(t)
        continue
      }
      if (t.x < at.x) next.push({ x: t.x, w: at.x - t.x, y: t.y })
      if (tEnd > end) next.push({ x: end, w: tEnd - end, y: t.y })
    }
    next.push({ x: at.x, w, y: top })
    next.sort((a, b) => a.x - b.x)
    // merge neighbours at the same height
    sky = next.reduce<typeof sky>((acc, t) => {
      const last = acc[acc.length - 1]
      if (last && Math.abs(last.y - t.y) < 0.5 && Math.abs(last.x + last.w - t.x) < 0.5) last.w += t.w
      else acc.push({ ...t })
      return acc
    }, [])
  }
  return out
}

/** Anything the user arranged (what "Reset layout" would undo). */
export const hasArrangement = (l: CanvasLayout): boolean =>
  Object.keys(l.pos).length + Object.keys(l.scale).length + Object.keys(l.dir).length > 0

const normalize = (l: Partial<ServerLayout> | null | undefined): CanvasLayout => ({
  pos: { ...(l?.pos ?? {}) },
  scale: { ...(l?.scale ?? {}) },
  dir: { ...(l?.dir ?? {}) },
})

export function useCanvasLayout(threadId: string | null) {
  const [layout, setLayout] = useState<CanvasLayout>(EMPTY)
  const current = useRef<CanvasLayout>(EMPTY)
  const thread = useRef<string | null>(threadId)
  /** Unsaved local changes (a server copy must not overwrite them). */
  const dirty = useRef(false)
  const timer = useRef(0)

  const save = useCallback((keepalive = false): Promise<void> => {
    window.clearTimeout(timer.current)
    timer.current = 0
    const id = thread.current
    if (!id || !dirty.current) return Promise.resolve()
    const sent = current.current
    return putCanvasLayout(id, sent, { keepalive }).then(
      () => {
        // newer edits keep the layout dirty (their own save is scheduled)
        if (current.current === sent && thread.current === id) dirty.current = false
      },
      () => {
        // offline / failed: keep it dirty, the next change retries
      },
    )
  }, [])

  const schedule = useCallback((): void => {
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => void save(), SAVE_DEBOUNCE_MS)
  }, [save])

  // Thread switch: flush the old one; a `/chat/new` layout carries over.
  useEffect(() => {
    const prev = thread.current
    if (prev === threadId) return
    if (prev) void save()
    thread.current = threadId
    if (prev === null && threadId && dirty.current) {
      schedule()
      return
    }
    dirty.current = false
    current.current = EMPTY
    setLayout(EMPTY)
  }, [threadId, save, schedule])

  useEffect(() => {
    const onHide = (): void => void save(true)
    window.addEventListener('pagehide', onHide)
    return () => {
      window.removeEventListener('pagehide', onHide)
      void save(true)
    }
  }, [save])

  const update = useCallback((fn: (l: CanvasLayout) => CanvasLayout): void => {
    const next = fn(current.current)
    current.current = next
    dirty.current = true
    setLayout(next)
    if (thread.current) schedule()
  }, [schedule])

  /** The server's copy (page load, after an operation): taken unless local edits are unsaved. */
  const adopt = useCallback((server: Partial<ServerLayout> | null | undefined): void => {
    if (dirty.current) return
    const next = normalize(server)
    current.current = next
    setLayout(next)
  }, [])

  /** Sets (or with `null` clears) the offsets of several elements at once. */
  const setPos = useCallback((entries: Record<string, Point | null>): void => {
    update((l) => {
      const pos = { ...l.pos }
      for (const [k, p] of Object.entries(entries)) {
        // a component back at its flow place needs no offset; a group position is kept even at 0,0
        if (p && (k.startsWith('g:') || Math.abs(p.x) >= 0.5 || Math.abs(p.y) >= 0.5)) pos[k] = { x: Math.round(p.x), y: Math.round(p.y) }
        else delete pos[k]
      }
      return { ...l, pos }
    })
  }, [update])

  const setScale = useCallback((path: string, scale: number | null): void => {
    update((l) => {
      const next = { ...l.scale }
      if (scale != null && Math.abs(scale - 1) > 0.01) next[path] = Math.round(scale * 1000) / 1000
      else delete next[path]
      return { ...l, scale: next }
    })
  }, [update])

  const setDir = useCallback((folder: string, dir: GroupDir | null): void => {
    update((l) => {
      const next = { ...l.dir }
      if (dir) next[folder] = dir
      else delete next[folder]
      return { ...l, dir: next }
    })
  }, [update])

  /** Back to the automatic layout. */
  const reset = useCallback((): void => update(() => EMPTY), [update])

  return { layout, setPos, setScale, setDir, reset, adopt, flush: save }
}
