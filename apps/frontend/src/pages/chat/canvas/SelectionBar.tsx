/**
 * Floating toolbar for the current selection — the essentials only (the page
 * passes them: vision mode, describe a change, preview, delete, clear); every
 * other action lives in the context menu behind More. One icon button per
 * action, sections separated by hairlines. Same `MenuAction`s
 * as the context menu.
 *
 * Fixed thickness (only the long axis scrolls, scrollbar hidden). The grip
 * moves it anywhere on screen; the orientation button flips it vertical ↔
 * horizontal (same action is also in More's menu). Both are remembered
 * (localStorage, all chats); the default is the top-right
 * corner, vertical. The position is clamped into the window on every render.
 */
import { useEffect, useRef, useState, type CSSProperties, type PointerEvent as ReactPointerEvent } from 'react'
import { ArrowLeftRight, ArrowUpDown, Ellipsis, GripHorizontal, GripVertical } from 'lucide-react'
import { cx } from '../../../ui'
import type { MenuAction } from './ContextMenu'

type Orientation = 'vertical' | 'horizontal'
interface BarPlace {
  /** Top-left corner in screen px; null = default corner. */
  at: { x: number; y: number } | null
  orientation: Orientation
}

/** The toolbar's own setting, offered in the menu opened by More. */
export interface BarControls {
  vertical: boolean
  toggle: () => void
}

const STORAGE_KEY = 'wc-canvas-toolbar'
const MARGIN = 12
/** Top of the default place: under the top bar. */
const DEFAULT_TOP = 64

function loadPlace(): BarPlace {
  try {
    const v = JSON.parse(localStorage.getItem(STORAGE_KEY) || 'null') as Partial<BarPlace> | null
    return { at: v?.at ?? null, orientation: v?.orientation === 'horizontal' ? 'horizontal' : 'vertical' }
  } catch {
    return { at: null, orientation: 'vertical' }
  }
}

const BUTTON = 'grid size-9 shrink-0 cursor-pointer place-items-center rounded-lg text-ink-2 outline-none transition-colors duration-100 hover:bg-app hover:text-ink focus-visible:ring-2 focus-visible:ring-accent/40 pointer-coarse:size-11'

export function SelectionBar({ groups, onMore }: {
  /** "More actions" — opens the full context menu next to the button. */
  groups: MenuAction[][]
  onMore: (anchor: DOMRect, toolbar: BarControls) => void
}) {
  const [place, setPlace] = useState<BarPlace>(loadPlace)
  const ref = useRef<HTMLDivElement>(null)
  const [, setWin] = useState(0)
  const vertical = place.orientation === 'vertical'

  useEffect(() => {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(place))
    } catch {
      // not remembered
    }
  }, [place])
  // re-clamp when the window changes (rotation, keyboard)
  useEffect(() => {
    const onResize = (): void => setWin((n) => n + 1)
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  const drag = useRef<{ id: number; dx: number; dy: number } | null>(null)
  const onGripDown = (e: ReactPointerEvent<HTMLButtonElement>): void => {
    const r = ref.current?.getBoundingClientRect()
    if (!r) return
    e.preventDefault()
    e.currentTarget.setPointerCapture(e.pointerId)
    drag.current = { id: e.pointerId, dx: e.clientX - r.left, dy: e.clientY - r.top }
  }
  const onGripMove = (e: ReactPointerEvent<HTMLButtonElement>): void => {
    const d = drag.current
    if (!d || d.id !== e.pointerId) return
    setPlace((p) => ({ ...p, at: { x: e.clientX - d.dx, y: e.clientY - d.dy } }))
  }
  const onGripUp = (e: ReactPointerEvent<HTMLButtonElement>): void => {
    if (drag.current?.id === e.pointerId) drag.current = null
  }

  // Clamp into the window (the bar's own size limits the long axis).
  const vw = window.innerWidth
  const vh = window.innerHeight
  const thick = ref.current ? (vertical ? ref.current.offsetWidth : ref.current.offsetHeight) : 56
  const style: CSSProperties = {}
  if (place.at) {
    const x = Math.max(MARGIN, Math.min(place.at.x, vw - MARGIN - (vertical ? thick : 120)))
    const y = Math.max(MARGIN, Math.min(place.at.y, vh - MARGIN - (vertical ? 120 : thick)))
    style.left = x
    style.top = y
    if (vertical) style.maxHeight = vh - y - MARGIN
    else style.maxWidth = vw - x - MARGIN
  } else {
    style.right = `max(${MARGIN}px, env(safe-area-inset-right))`
    style.top = `calc(${DEFAULT_TOP}px + env(safe-area-inset-top))`
    if (vertical) style.maxHeight = `calc(100dvh - ${DEFAULT_TOP + 180}px)`
    else style.maxWidth = `calc(100vw - ${2 * MARGIN}px)`
  }

  const sections = groups.filter((g) => g.length > 0)
  const sep = vertical ? 'my-0.5 h-px w-6 shrink-0 bg-line' : 'mx-0.5 h-6 w-px shrink-0 bg-line'
  return (
    <div
      ref={ref}
      role="toolbar"
      aria-orientation={place.orientation}
      aria-label="Selection actions"
      className={cx(
        'fixed z-30 flex animate-pop items-center gap-0.5 rounded-xl border border-line bg-surface/95 p-1 shadow-pop backdrop-blur select-none [scrollbar-width:none] [&::-webkit-scrollbar]:hidden',
        vertical
          ? 'w-[46px] flex-col overflow-x-hidden overflow-y-auto overscroll-contain pointer-coarse:w-[54px]'
          : 'h-[46px] flex-row overflow-x-auto overflow-y-hidden overscroll-contain pointer-coarse:h-[54px]',
      )}
      style={style}
    >
      <button
        type="button"
        title="Drag to move the toolbar"
        aria-label="Move toolbar"
        onPointerDown={onGripDown}
        onPointerMove={onGripMove}
        onPointerUp={onGripUp}
        onPointerCancel={onGripUp}
        onDoubleClick={() => setPlace((p) => ({ ...p, at: null }))}
        className={cx('grid shrink-0 cursor-grab touch-none place-items-center rounded-md text-ink-3 hover:text-ink-2 active:cursor-grabbing', vertical ? 'h-5 w-9 pointer-coarse:h-7 pointer-coarse:w-11' : 'h-9 w-5 pointer-coarse:h-11 pointer-coarse:w-7')}
      >
        {vertical ? <GripHorizontal size={15} /> : <GripVertical size={15} />}
      </button>
      {sections.map((items, i) => (
        <div key={i} className={cx('flex shrink-0 items-center gap-0.5', vertical ? 'w-full flex-col' : 'h-full flex-row')}>
          <div role="separator" className={sep} />
          {items.map(({ key, label, icon: Icon, disabled, active, onSelect }) => (
            <button
              key={key}
              type="button"
              title={label}
              aria-label={label}
              aria-pressed={active}
              disabled={disabled}
              onClick={(e) => {
                e.currentTarget.blur()
                onSelect()
              }}
              className={cx(BUTTON, active && 'bg-accent-soft text-accent hover:bg-accent-soft hover:text-accent', disabled && 'pointer-events-none opacity-35')}
            >
              {Icon && <Icon size={17} />}
            </button>
          ))}
        </div>
      ))}
      <div role="separator" className={sep} />
      <button
        type="button"
        title={vertical ? 'Make horizontal' : 'Make vertical'}
        aria-label={vertical ? 'Make horizontal' : 'Make vertical'}
        onClick={(e) => {
          e.currentTarget.blur()
          setPlace((p) => ({ ...p, orientation: p.orientation === 'vertical' ? 'horizontal' : 'vertical' }))
        }}
        className={BUTTON}
      >
        {vertical ? <ArrowLeftRight size={17} /> : <ArrowUpDown size={17} />}
      </button>
      <button
        type="button"
        title="More actions"
        aria-label="More actions"
        aria-haspopup="menu"
        onClick={(e) => onMore(e.currentTarget.getBoundingClientRect(), {
          vertical,
          toggle: () => setPlace((p) => ({ ...p, orientation: p.orientation === 'vertical' ? 'horizontal' : 'vertical' })),
        })}
        className={BUTTON}
      >
        <Ellipsis size={17} />
      </button>
    </div>
  )
}
