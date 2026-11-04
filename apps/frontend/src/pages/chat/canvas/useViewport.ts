/**
 * Pan/zoom viewport for the design canvas (Figma-like, touch-first).
 *
 * The view (`x`, `y` = screen offset of the world origin, `k` = zoom) lives in
 * a ref and is applied imperatively to the world element's transform, so a
 * gesture never re-renders React (`onView` gets every change, e.g. for the %
 * readout). While the view moves the world is a promoted layer (`will-change`)
 * so frames are only composited; ~140 ms after it stops ("settle") the layer is
 * dropped (iframes re-raster crisply at the new scale) and `--inv-k` (1/k, used
 * by the zoom-independent chrome) is updated — never per frame, since it
 * restyles and re-lays out the whole world. `onMotion` reports the start of a
 * motion and the settle (the page freezes the live component documents in
 * between, so the compositor only moves bitmaps it already has).
 *
 * Gestures (all on the viewport element; the hook owns every pointer):
 *  - one pointer drag → pan (touch, pen and mouse)
 *  - drag on a `[data-handle="move"|"resize"]` element → `onHandle` with the
 *    world-space delta (element dragging / resizing; the page moves the DOM)
 *  - two pointers → pinch zoom around the midpoint + pan
 *  - wheel → pan; ctrl/⌘+wheel and trackpad pinch (ctrlKey wheel) → zoom at the cursor
 *  - tap → `onTap(target)`; long press (touch/pen, 450 ms) or right click → `onMenu`
 * Targets are the nearest ancestor with `data-sel` (e.g. `c:<artifact>`,
 * `g:<run>`), or null for empty canvas.
 */
import { useCallback, useEffect, useRef, type RefObject } from 'react'

export interface View {
  x: number
  y: number
  k: number
}

export interface TapInfo {
  target: string | null
  /** Shift / ⌘ / Ctrl held (desktop multi-select). */
  additive: boolean
}

export interface MenuInfo {
  target: string | null
  clientX: number
  clientY: number
}

export interface HandleInfo {
  kind: 'move' | 'resize'
  /** `data-sel` of the element the handle belongs to. */
  target: string
  phase: 'start' | 'move' | 'end' | 'cancel'
  /** World-space delta since the start. */
  dx: number
  dy: number
}

export const MIN_ZOOM = 0.05
export const MAX_ZOOM = 4

const LONG_PRESS_MS = 450
const MOVE_SLOP = 8
const SETTLE_MS = 140
/** Spacing of the background dots, screen px. */
const DOT_STEP = 24

const clampK = (k: number): number => Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, k))

function targetOf(el: EventTarget | null): string | null {
  const node = el instanceof Element ? el.closest<HTMLElement>('[data-sel]') : null
  return node?.dataset.sel ?? null
}

export function useViewport(
  viewportRef: RefObject<HTMLDivElement | null>,
  worldRef: RefObject<HTMLDivElement | null>,
  handlers: {
    onTap: (info: TapInfo) => void
    onMenu: (info: MenuInfo) => void
    /** Drag of a move / resize handle. */
    onHandle?: (info: HandleInfo) => void
    /** Every view change (cheap, imperative consumers only). */
    onView?: (view: View) => void
    /** Any user pan/zoom (the page stops auto-fitting after it). */
    onInteract?: () => void
    /** The view started moving (`true`, once per motion) / settled (`false`, with the final view). */
    onMotion?: (moving: boolean, view: View) => void
  },
) {
  const view = useRef<View>({ x: 0, y: 0, k: 1 })
  const settleTimer = useRef(0)
  const moving = useRef(false)
  const h = useRef(handlers)
  h.current = handlers

  const settle = useCallback((): void => {
    settleTimer.current = 0
    moving.current = false
    h.current.onMotion?.(false, view.current)
    const world = worldRef.current
    if (!world) return
    world.style.willChange = ''
    const inv = String(Math.round(1e4 / view.current.k) / 1e4)
    if (world.style.getPropertyValue('--inv-k') !== inv) world.style.setProperty('--inv-k', inv)
  }, [worldRef])

  const apply = useCallback((animate = false, immediate = false): void => {
    const world = worldRef.current
    if (!world) return
    const { x, y, k } = view.current
    world.style.transition = animate ? 'transform 220ms cubic-bezier(.2,.7,.2,1)' : ''
    // Composite-only while moving; settle() drops the layer so the iframes
    // re-raster at the final scale instead of staying a stretched bitmap.
    if (!immediate) world.style.willChange = 'transform'
    if (!immediate && !moving.current) {
      moving.current = true
      h.current.onMotion?.(true, view.current)
    }
    world.style.transform = `translate(${x}px, ${y}px) scale(${k})`
    // The dotted background is just texture: constant dot size and spacing at
    // any zoom, it only slides with the pan (so moving still feels anchored).
    const vp = viewportRef.current
    if (vp) {
      vp.style.backgroundSize = `${DOT_STEP}px ${DOT_STEP}px`
      vp.style.backgroundPosition = `${x % DOT_STEP}px ${y % DOT_STEP}px`
    }
    window.clearTimeout(settleTimer.current)
    if (immediate) settle()
    else settleTimer.current = window.setTimeout(settle, animate ? 260 : SETTLE_MS)
    h.current.onView?.(view.current)
  }, [viewportRef, worldRef, settle])

  const setView = useCallback((next: View, animate = false): void => {
    view.current = { ...next, k: clampK(next.k) }
    apply(animate)
  }, [apply])

  /** Zooms by `factor` keeping the screen point (cx, cy) fixed (default: viewport center). */
  const zoomBy = useCallback((factor: number, cx?: number, cy?: number, animate = false): void => {
    const vp = viewportRef.current
    if (!vp) return
    const r = vp.getBoundingClientRect()
    const px = (cx ?? r.left + r.width / 2) - r.left
    const py = (cy ?? r.top + r.height / 2) - r.top
    const { x, y, k } = view.current
    const nk = clampK(k * factor)
    const f = nk / k
    setView({ k: nk, x: px - (px - x) * f, y: py - (py - y) * f }, animate)
  }, [setView, viewportRef])

  /**
   * Fits a world-space rect into the viewport, leaving `inset` screen pixels
   * free on each side (top bar, composer). Never zooms in beyond `maxK`.
   */
  const fitRect = useCallback((
    rect: { x: number; y: number; w: number; h: number },
    inset: { top: number; right: number; bottom: number; left: number },
    maxK = 1,
    animate = true,
  ): void => {
    const vp = viewportRef.current
    if (!vp || rect.w <= 0 || rect.h <= 0) return
    const vw = vp.clientWidth - inset.left - inset.right
    const vh = vp.clientHeight - inset.top - inset.bottom
    if (vw <= 0 || vh <= 0) return
    const k = clampK(Math.min(maxK, vw / rect.w, vh / rect.h))
    const next = {
      k,
      x: inset.left + (vw - rect.w * k) / 2 - rect.x * k,
      // tall content: align its top instead of centering it
      y: inset.top + Math.max(0, (vh - rect.h * k) / 2) - rect.y * k,
    }
    // Content that grows a little (frames measuring themselves) must not
    // restart the view animation over and over.
    const cur = view.current
    if (Math.abs(next.k - cur.k) / cur.k < 0.02 && Math.abs(next.x - cur.x) < 6 && Math.abs(next.y - cur.y) < 6) return
    setView(next, animate)
  }, [setView, viewportRef])

  /**
   * World-space rect of an element inside the world, from layout offsets (the
   * world is the offset parent) plus the canvas offsets of moved elements
   * (`data-dx` / `data-dy`) — unaffected by the view transform, even mid-animation.
   */
  const rectOf = useCallback((el: HTMLElement): { x: number; y: number; w: number; h: number } | null => {
    const world = worldRef.current
    if (!world) return null
    let x = 0
    let y = 0
    let node: HTMLElement | null = el
    while (node && node !== world) {
      x += node.offsetLeft + Number(node.dataset.dx || 0)
      y += node.offsetTop + Number(node.dataset.dy || 0)
      node = node.offsetParent as HTMLElement | null
    }
    if (node !== world) return null
    return { x, y, w: el.offsetWidth, h: el.offsetHeight }
  }, [worldRef])

  useEffect(() => {
    const vp = viewportRef.current
    if (!vp) return
    apply(false, true)

    const pointers = new Map<number, { x: number; y: number }>()
    let start: { x: number; y: number; vx: number; vy: number; target: string | null; additive: boolean } | null = null
    let moved = false
    let pinch: { dist: number; mx: number; my: number; view: View } | null = null
    let pressTimer = 0
    let menuFiredAt = 0
    let handle: { id: number; kind: 'move' | 'resize'; target: string; x: number; y: number } | null = null

    const clearPress = (): void => {
      window.clearTimeout(pressTimer)
      pressTimer = 0
    }
    const startPinch = (): void => {
      const [a, b] = [...pointers.values()]
      pinch = {
        dist: Math.hypot(b.x - a.x, b.y - a.y) || 1,
        mx: (a.x + b.x) / 2,
        my: (a.y + b.y) / 2,
        view: { ...view.current },
      }
    }

    const onDown = (e: PointerEvent): void => {
      if (e.pointerType === 'mouse' && e.button !== 0) return
      // UI chrome inside the viewport (buttons in group headers) keeps its own clicks.
      if (e.target instanceof Element && e.target.closest('[data-canvas-ui]')) return
      if (handle) return // one handle drag at a time, extra fingers ignored
      const hEl = e.target instanceof Element ? e.target.closest<HTMLElement>('[data-handle]') : null
      const hTarget = hEl ? targetOf(hEl) : null
      if (hEl && hTarget && pointers.size === 0) {
        handle = { id: e.pointerId, kind: hEl.dataset.handle === 'resize' ? 'resize' : 'move', target: hTarget, x: e.clientX, y: e.clientY }
        try {
          vp.setPointerCapture(e.pointerId)
        } catch {
          // pointer already gone
        }
        h.current.onInteract?.()
        h.current.onHandle?.({ kind: handle.kind, target: hTarget, phase: 'start', dx: 0, dy: 0 })
        return
      }
      pointers.set(e.pointerId, { x: e.clientX, y: e.clientY })
      try {
        vp.setPointerCapture(e.pointerId)
      } catch {
        // pointer already gone
      }
      if (worldRef.current) worldRef.current.style.transition = ''
      if (pointers.size === 1) {
        moved = false
        start = {
          x: e.clientX, y: e.clientY, vx: view.current.x, vy: view.current.y,
          target: targetOf(e.target), additive: e.shiftKey || e.metaKey || e.ctrlKey,
        }
        if (e.pointerType !== 'mouse') {
          const { clientX, clientY } = e
          pressTimer = window.setTimeout(() => {
            pressTimer = 0
            if (!start || moved || pointers.size !== 1) return
            menuFiredAt = Date.now()
            moved = true // the release must not count as a tap
            navigator.vibrate?.(8)
            h.current.onMenu({ target: start.target, clientX, clientY })
          }, LONG_PRESS_MS)
        }
      } else if (pointers.size === 2) {
        clearPress()
        moved = true
        startPinch()
      }
    }

    const handleDelta = (e: PointerEvent): { dx: number; dy: number } => {
      const k = view.current.k
      return { dx: (e.clientX - (handle?.x ?? 0)) / k, dy: (e.clientY - (handle?.y ?? 0)) / k }
    }

    const onMove = (e: PointerEvent): void => {
      if (handle) {
        if (e.pointerId === handle.id) h.current.onHandle?.({ kind: handle.kind, target: handle.target, phase: 'move', ...handleDelta(e) })
        return
      }
      const p = pointers.get(e.pointerId)
      if (!p) return
      p.x = e.clientX
      p.y = e.clientY
      if (pointers.size >= 2 && pinch) {
        const [a, b] = [...pointers.values()]
        const dist = Math.hypot(b.x - a.x, b.y - a.y) || 1
        const mx = (a.x + b.x) / 2
        const my = (a.y + b.y) / 2
        const r = vp.getBoundingClientRect()
        const k = clampK(pinch.view.k * (dist / pinch.dist))
        const f = k / pinch.view.k
        // keep the world point under the initial midpoint under the current midpoint
        const ox = pinch.mx - r.left
        const oy = pinch.my - r.top
        setView({
          k,
          x: mx - r.left - (ox - pinch.view.x) * f,
          y: my - r.top - (oy - pinch.view.y) * f,
        })
        h.current.onInteract?.()
        return
      }
      if (pointers.size === 1 && start) {
        const dx = e.clientX - start.x
        const dy = e.clientY - start.y
        if (!moved && Math.hypot(dx, dy) > MOVE_SLOP) {
          moved = true
          clearPress()
        }
        if (moved && Date.now() - menuFiredAt > 600) {
          setView({ ...view.current, x: start.vx + dx, y: start.vy + dy })
          h.current.onInteract?.()
        }
      }
    }

    const onUp = (e: PointerEvent): void => {
      if (handle) {
        if (e.pointerId !== handle.id) return
        const phase = e.type === 'pointerup' ? 'end' : 'cancel'
        const info = { kind: handle.kind, target: handle.target, phase, ...handleDelta(e) } as const
        handle = null
        h.current.onHandle?.(info)
        return
      }
      if (!pointers.has(e.pointerId)) return
      pointers.delete(e.pointerId)
      clearPress()
      if (pointers.size === 0) {
        if (start && !moved && e.type === 'pointerup') {
          h.current.onTap({ target: start.target, additive: start.additive })
        }
        start = null
        pinch = null
      } else if (pointers.size === 1) {
        // pinch → pan with the remaining finger, without a jump
        const [rest] = [...pointers.values()]
        pinch = null
        start = { x: rest.x, y: rest.y, vx: view.current.x, vy: view.current.y, target: null, additive: false }
      } else {
        startPinch()
      }
    }

    const onWheel = (e: WheelEvent): void => {
      e.preventDefault()
      if (worldRef.current) worldRef.current.style.transition = ''
      if (e.ctrlKey || e.metaKey) {
        const factor = Math.exp(-e.deltaY * (e.deltaMode === 1 ? 0.05 : 0.01))
        zoomBy(factor, e.clientX, e.clientY)
      } else {
        const scale = e.deltaMode === 1 ? 16 : 1
        const dx = e.shiftKey && !e.deltaX ? e.deltaY : e.deltaX
        const dy = e.shiftKey && !e.deltaX ? 0 : e.deltaY
        setView({ ...view.current, x: view.current.x - dx * scale, y: view.current.y - dy * scale })
      }
      h.current.onInteract?.()
    }

    const onContextMenu = (e: MouseEvent): void => {
      e.preventDefault()
      // The press that opened the menu must not end as a tap: Android may fire contextmenu
      // before the long-press timer, and macOS ctrl+click is a left-button press — the
      // release would toggle the just-selected target off and the menu would turn global.
      moved = true
      // Android fires contextmenu for the same long press the timer already handled.
      if (Date.now() - menuFiredAt < 800) return
      menuFiredAt = Date.now()
      clearPress()
      h.current.onMenu({ target: targetOf(e.target), clientX: e.clientX, clientY: e.clientY })
    }

    // Safari's proprietary pinch events would zoom the whole page.
    const prevent = (e: Event): void => e.preventDefault()

    vp.addEventListener('pointerdown', onDown)
    vp.addEventListener('pointermove', onMove)
    vp.addEventListener('pointerup', onUp)
    vp.addEventListener('pointercancel', onUp)
    vp.addEventListener('wheel', onWheel, { passive: false })
    vp.addEventListener('contextmenu', onContextMenu)
    vp.addEventListener('gesturestart', prevent)
    vp.addEventListener('gesturechange', prevent)
    return () => {
      clearPress()
      vp.removeEventListener('pointerdown', onDown)
      vp.removeEventListener('pointermove', onMove)
      vp.removeEventListener('pointerup', onUp)
      vp.removeEventListener('pointercancel', onUp)
      vp.removeEventListener('wheel', onWheel)
      vp.removeEventListener('contextmenu', onContextMenu)
      vp.removeEventListener('gesturestart', prevent)
      vp.removeEventListener('gesturechange', prevent)
      window.clearTimeout(settleTimer.current)
    }
  }, [apply, setView, zoomBy, viewportRef, worldRef])

  return { view, setView, zoomBy, fitRect, rectOf }
}
