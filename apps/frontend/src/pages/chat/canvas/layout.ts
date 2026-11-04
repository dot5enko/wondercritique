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
 *  - `dir`  — arrangement of a folder's items (row / column; default wrap).
 * Stored on the server per thread (`PUT /threads/{id}/canvas/layout`,
 * debounced, flushed on unmount / `pagehide`); the agents never see it.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { putCanvasLayout, type CanvasLayout as ServerLayout } from '../../../api/canvas'

export interface Point {
  x: number
  y: number
}
export type GroupDir = 'row' | 'col'

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
