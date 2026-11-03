/**
 * Pan/zoom viewport for the design canvas (Figma-like, touch-first).
 *
 * The view (`x`, `y` = screen offset of the world origin, `k` = zoom) lives in
 * a ref and is applied imperatively to the world element's transform, so a
 * gesture never re-renders React. `zoom` state is only for the % readout.
 *
 * Gestures (all on the viewport element; the hook owns every pointer):
 *  - one pointer drag → pan (touch, pen and mouse; there is no element dragging)
 *  - two pointers → pinch zoom around the midpoint + pan
 *  - wheel → pan; ctrl/⌘+wheel and trackpad pinch (ctrlKey wheel) → zoom at the cursor
 *  - tap → `onTap(target)`; long press (touch/pen, 450 ms) or right click → `onMenu`
 * Targets are the nearest ancestor with `data-sel` (e.g. `c:<artifact>`,
 * `g:<run>`), or null for empty canvas.
 */
import { useCallback, useEffect, useRef, useState, type RefObject } from 'react'

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

export const MIN_ZOOM = 0.05
export const MAX_ZOOM = 4

const LONG_PRESS_MS = 450
const MOVE_SLOP = 8

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
    /** Any user pan/zoom (the page stops auto-fitting after it). */
    onInteract?: () => void
  },
) {
  const view = useRef<View>({ x: 0, y: 0, k: 1 })
  const [zoom, setZoom] = useState(1)
  const raf = useRef(0)
  const h = useRef(handlers)
  h.current = handlers

  const apply = useCallback((animate = false): void => {
    const world = worldRef.current
    if (!world) return
    const { x, y, k } = view.current
    world.style.transition = animate ? 'transform 220ms cubic-bezier(.2,.7,.2,1)' : ''
    // 2D transform (no translate3d / will-change): the browser re-rasterizes
    // the iframes at the new scale instead of stretching a cached bitmap.
    world.style.transform = `translate(${x}px, ${y}px) scale(${k})`
    world.style.setProperty('--inv-k', String(1 / k))
    // dotted canvas background follows the view (coarser dots when zoomed out)
    const vp = viewportRef.current
    if (vp) {
      const step = 24 * k * (k < 0.35 ? 4 : 1)
      vp.style.backgroundSize = `${step}px ${step}px`
      vp.style.backgroundPosition = `${x}px ${y}px`
    }
    cancelAnimationFrame(raf.current)
    raf.current = requestAnimationFrame(() => setZoom(k))
  }, [viewportRef, worldRef])

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
    setView({
      k,
      x: inset.left + (vw - rect.w * k) / 2 - rect.x * k,
      // tall content: align its top instead of centering it
      y: inset.top + Math.max(0, (vh - rect.h * k) / 2) - rect.y * k,
    }, animate)
  }, [setView, viewportRef])

  /**
   * World-space rect of an element inside the world, from layout offsets (the
   * world is the offset parent) — unaffected by the transform, even mid-animation.
   */
  const rectOf = useCallback((el: HTMLElement): { x: number; y: number; w: number; h: number } | null => {
    const world = worldRef.current
    if (!world) return null
    let x = 0
    let y = 0
    let node: HTMLElement | null = el
    while (node && node !== world) {
      x += node.offsetLeft
      y += node.offsetTop
      node = node.offsetParent as HTMLElement | null
    }
    if (node !== world) return null
    return { x, y, w: el.offsetWidth, h: el.offsetHeight }
  }, [worldRef])

  useEffect(() => {
    const vp = viewportRef.current
    if (!vp) return
    apply()

    const pointers = new Map<number, { x: number; y: number }>()
    let start: { x: number; y: number; vx: number; vy: number; target: string | null; additive: boolean } | null = null
    let moved = false
    let pinch: { dist: number; mx: number; my: number; view: View } | null = null
    let pressTimer = 0
    let menuFiredAt = 0

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

    const onMove = (e: PointerEvent): void => {
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
      cancelAnimationFrame(raf.current)
    }
  }, [apply, setView, zoomBy, viewportRef, worldRef])

  return { view, zoom, setView, zoomBy, fitRect, rectOf }
}
