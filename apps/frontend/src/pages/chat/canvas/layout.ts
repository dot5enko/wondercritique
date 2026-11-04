/**
 * User layout of one chat's canvas, on top of the automatic flex layout:
 *  - `pos`  — CSS `translate` of an element. `c:<artifact>`: offset of a moved
 *    component from its place in the group's flow (any direction — the group
 *    block grows around it). `g:<run>`: absolute canvas position — once any
 *    group is moved or arranged, every group is positioned ("free" layout, see
 *    the canvas page), so groups never shift when another one changes height;
 *  - `scale` — uniform scale of a resized component (artifact id);
 *  - `dir`  — arrangement of a group's components (row / column; default wrap);
 *  - `removed` — groups / components deleted from the canvas (hidden, out of
 *    every context), `pending` — what of that the agent hasn't been told yet
 *    (key → note; sent as `<meta>` with the next message, then cleared).
 * Kept per thread in localStorage (a view of the chat, not chat data).
 */
import { useCallback, useEffect, useState } from 'react'

export interface Point {
  x: number
  y: number
}
export type GroupDir = 'row' | 'col'

export interface CanvasLayout {
  pos: Record<string, Point>
  scale: Record<string, number>
  dir: Record<string, GroupDir>
  removed: Record<string, true>
  pending: Record<string, string>
}

export const MIN_SCALE = 0.25
export const MAX_SCALE = 4

const EMPTY: CanvasLayout = { pos: {}, scale: {}, dir: {}, removed: {}, pending: {} }
const storageKey = (threadId: string): string => `wc-canvas-layout:${threadId}`

function load(threadId: string | null): CanvasLayout {
  if (!threadId) return EMPTY
  try {
    const raw = localStorage.getItem(storageKey(threadId))
    const v = raw ? (JSON.parse(raw) as Partial<CanvasLayout>) : null
    return v
      ? { pos: v.pos ?? {}, scale: v.scale ?? {}, dir: v.dir ?? {}, removed: v.removed ?? {}, pending: v.pending ?? {} }
      : EMPTY
  } catch {
    return EMPTY
  }
}

const isEmpty = (l: CanvasLayout): boolean => Object.values(l).every((m) => Object.keys(m).length === 0)

/** Anything the user arranged (what "Reset layout" would undo). */
export const hasArrangement = (l: CanvasLayout): boolean =>
  Object.keys(l.pos).length + Object.keys(l.scale).length + Object.keys(l.dir).length > 0

export function useCanvasLayout(threadId: string | null) {
  const [state, setState] = useState<{ thread: string | null; layout: CanvasLayout }>(() => ({ thread: threadId, layout: load(threadId) }))
  // A new chat (`/chat/new` → `/chat/<id>`) keeps what was arranged before it had an id.
  const layout = state.thread === threadId || state.thread === null ? state.layout : load(threadId)

  useEffect(() => {
    setState((prev) => (prev.thread === threadId ? prev : { thread: threadId, layout: prev.thread === null ? prev.layout : load(threadId) }))
  }, [threadId])

  useEffect(() => {
    if (!threadId || state.thread !== threadId) return
    try {
      if (isEmpty(state.layout)) localStorage.removeItem(storageKey(threadId))
      else localStorage.setItem(storageKey(threadId), JSON.stringify(state.layout))
    } catch {
      // storage full / disabled: the layout just isn't remembered
    }
  }, [threadId, state])

  const update = useCallback((fn: (l: CanvasLayout) => CanvasLayout): void => {
    setState((prev) => ({ thread: threadId, layout: fn(prev.thread === threadId || prev.thread === null ? prev.layout : load(threadId)) }))
  }, [threadId])

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

  const setScale = useCallback((id: string, scale: number | null): void => {
    update((l) => {
      const next = { ...l.scale }
      if (scale != null && Math.abs(scale - 1) > 0.01) next[id] = Math.round(scale * 1000) / 1000
      else delete next[id]
      return { ...l, scale: next }
    })
  }, [update])

  const setDir = useCallback((runId: string, dir: GroupDir | null): void => {
    update((l) => {
      const next = { ...l.dir }
      if (dir) next[runId] = dir
      else delete next[runId]
      return { ...l, dir: next }
    })
  }, [update])

  /** Deletes elements from the canvas; `notes` (key → text) are queued for the agent. */
  const remove = useCallback((notes: Record<string, string>): void => {
    update((l) => {
      const removed = { ...l.removed }
      for (const k of Object.keys(notes)) removed[k] = true
      return { ...l, removed, pending: { ...l.pending, ...notes } }
    })
  }, [update])

  /** Undoes a deletion (and drops its unsent note). */
  const restore = useCallback((keys: string[]): void => {
    update((l) => {
      const removed = { ...l.removed }
      const pending = { ...l.pending }
      for (const k of keys) {
        delete removed[k]
        delete pending[k]
      }
      return { ...l, removed, pending }
    })
  }, [update])

  /** The notes were delivered with a message. */
  const clearPending = useCallback((keys: string[]): void => {
    update((l) => {
      const pending = { ...l.pending }
      for (const k of keys) delete pending[k]
      return { ...l, pending }
    })
  }, [update])

  /** Back to the automatic layout (deletions stay). */
  const reset = useCallback((): void => update((l) => ({ ...EMPTY, removed: l.removed, pending: l.pending })), [update])

  return { layout, setPos, setScale, setDir, remove, restore, clearPending, reset }
}
