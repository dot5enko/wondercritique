/**
 * User layout of one chat's canvas, on top of the automatic flex layout:
 *  - `pos`  — CSS `translate` of an element. `c:<artifact>`: offset of a moved
 *    component from its place in the group's flow. `g:<run>`: absolute canvas
 *    position — once any group is moved or arranged, every group is positioned
 *    ("free" layout, see the canvas page), so groups never shift when another
 *    one changes height;
 *  - `size` — manual frame size of a resized component (artifact id);
 *  - `dir`  — arrangement of a group's components (row / column; default wrap).
 * Kept per thread in localStorage (it's a view preference, not chat data).
 */
import { useCallback, useEffect, useState } from 'react'

export interface Point {
  x: number
  y: number
}
export interface Size {
  w: number
  h: number
}
export type GroupDir = 'row' | 'col'

export interface CanvasLayout {
  pos: Record<string, Point>
  size: Record<string, Size>
  dir: Record<string, GroupDir>
}

const EMPTY: CanvasLayout = { pos: {}, size: {}, dir: {} }
const storageKey = (threadId: string): string => `wc-canvas-layout:${threadId}`

function load(threadId: string | null): CanvasLayout {
  if (!threadId) return EMPTY
  try {
    const raw = localStorage.getItem(storageKey(threadId))
    const v = raw ? (JSON.parse(raw) as Partial<CanvasLayout>) : null
    return v ? { pos: v.pos ?? {}, size: v.size ?? {}, dir: v.dir ?? {} } : EMPTY
  } catch {
    return EMPTY
  }
}

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
      const empty = Object.keys(state.layout.pos).length + Object.keys(state.layout.size).length + Object.keys(state.layout.dir).length === 0
      if (empty) localStorage.removeItem(storageKey(threadId))
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

  const setSize = useCallback((id: string, size: Size | null): void => {
    update((l) => {
      const next = { ...l.size }
      if (size) next[id] = { w: Math.round(size.w), h: Math.round(size.h) }
      else delete next[id]
      return { ...l, size: next }
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

  const reset = useCallback((): void => update(() => EMPTY), [update])

  return { layout, setPos, setSize, setDir, reset }
}
